# Agent Substrate on a kubeadm cluster: install record (2026-10-30)

Phase 0a of the pluggable execution plane epic
([#692](https://github.com/sympozium-ai/sympozium/issues/692), task
[#694](https://github.com/sympozium-ai/sympozium/issues/694)). The question
was what it costs a non-GKE user to stand up
[Agent Substrate](https://github.com/agent-substrate/substrate) at all, and
whether its actor lifecycle behaves as advertised on ordinary hardware. Every
number below was measured on the maintainer's cluster the same day.

## The cluster

| | |
|---|---|
| Host | `framework`, single node, Fedora Linux 43, kernel 7.1.8 |
| Kubernetes | kubeadm v1.35.7, containerd 2.3.3, Calico-style default CNI, `local-path` StorageClass |
| Hardware | 32 CPUs, 62 GiB RAM, `/dev/kvm` present |
| Already running | Sympozium v0.12.2 with its Celln fleet, a Postgres, llama-server |

## Prerequisites that had to change on the cluster

Substrate's pod-certificate controller issues the certificates its components
present to each other through the Kubernetes `PodCertificateRequest` API and
distributes trust through `ClusterTrustBundle`. Both are beta and **off by
default on Kubernetes 1.35**. The cluster needed, with backups taken first
(`~/k8s-backup-20261030/` on the host):

- kube-apiserver static pod: `--feature-gates=ClusterTrustBundle=true,ClusterTrustBundleProjection=true,PodCertificateRequest=true` and `--runtime-config=certificates.k8s.io/v1beta1=true`;
- kube-controller-manager static pod: `--feature-gates=PodCertificateRequest=true`;
- kubelet config: `featureGates: {ClusterTrustBundle: true, ClusterTrustBundleProjection: true, PodCertificateRequest: true}`, then `systemctl restart kubelet`.

**Pitfall.** The kubelet panics at startup if `ClusterTrustBundleProjection`
is on without `ClusterTrustBundle` ("depends on features that are
disabled"). The first attempt set only the two gates Substrate's kind config
lists for the kubelet, and the API server was unreachable for about four
minutes until the base gate was added. Pods that crashed during the outage
restarted on their own.

Nothing else on the node changed. No gVisor install, no RuntimeClass: the
`gvisor` sandbox class runs `runsc` inside the worker pod, and atelet fetches
the gVisor release itself (see below). The `microvm` class would use
`/dev/kvm` through the `ate.dev/kvm` device plugin atelet ships; it was not
exercised here.

## Install

Pre-built images from `ghcr.io/agent-substrate/substrate` at **v1.0.0-rc.1**
(released 2026-10-29, commit `f86fde9d`), installer run from that tag's
checkout. The installer needs Go 1.27 (`GOTOOLCHAIN=go1.27.3` on a 1.26
machine). The release does not ship the installer as a binary; it does ship
`kubectl-ate` (checksum verified).

Outside kind the installer bundles PostgreSQL but not snapshot storage, so
the single-node "kind profile" was used against the kubeadm context: it adds
an in-cluster S3-compatible `rustfs` with a `gs://ate-snapshots/` location,
a laptop-sized PostgreSQL, an OpenTelemetry collector and Prometheus, and
removes the image-credential-provider host mounts a plain node lacks.

```bash
export GOTOOLCHAIN=go1.27.3 ATE_INSTALL_KIND=true KUBECONFIG=~/.kube/config-framework
go run -mod=vendor ./cmd/ate-setup deploy ate-system --no-dev-env \
  --context kubernetes-admin@kubernetes \
  --image-repo ghcr.io/agent-substrate/substrate --image-tag v1.0.0-rc.1 \
  --credential-provider='{"name":"k8s.io"}' \
  --ingress-allowed-spiffe-ids=spiffe://cluster.local/ns/ate-system/sa/atenet-client \
  --rollout-timeout 15m
```

| Step | Took |
|---|---|
| pod-certificate controller rollout and ClusterTrustBundles ready | 18 s |
| bundled PostgreSQL rollout | 38 s |
| ate-api-server rollout (image pulls on first install) | 196 s |
| atenet-router rollout | 42 s |
| whole install, start to exit 0 | about 5 minutes |

Result in `ate-system`: `ate-api-server` ×2, `ate-idp-server` ×2,
`ate-controller`, `atelet` (DaemonSet), `atenet-router`, `atenet-egress`,
`k8s-credential-provider`, `postgres-0`, `rustfs`; plus
`podcertificate-controller` in its own namespace. Two 1 Gi PVCs
(PostgreSQL, rustfs). `kubectl top` is unavailable on this cluster (no
metrics-server), so the idle footprint was not measured.

## Actor lifecycle

A `WorkerPool` of two gVisor workers (2 CPU / 2 GiB each) and an
`ActorTemplate` running `hashicorp/http-echo` with an HTTP wakeup probe.

| Event | Measured |
|---|---|
| WorkerPool applied to both workers Ready | 16 s |
| gVisor release 20261026.0 fetched by atelet (anonymous download from the public bucket after the S3 plugin declined the `gs://` asset) | 18 s |
| Golden snapshot built from the template (run, checkpoint, terminate; includes the image pull inside the sandbox) | 17 s |
| `create actor` (comes up `SUSPENDED` on the golden tag, no worker held) | under 1 s |
| `resume` from the golden snapshot to `RUNNING` | 0.65 s |
| `suspend --mode=sync` to `SUSPENDED` with the memory snapshot `COMPLETED` in storage | 0.44 s |
| `resume` from the actor's own memory snapshot, restored **on the other worker** | 0.42 s |

The actor's log shows runsc starting the container and the wakeup probe
answered (`GET / 200`) immediately after each restore, so the process state
survived the move between workers. These are single-actor numbers on an idle
node, not a density benchmark.

Two things the API enforces that a Sympozium backend would have to meet:
container images must be pinned by digest (a tag is refused because changing
the image invalidates snapshots), and templates are immutable.

## What did not work

`kubectl ate proxy actor` from the workstation fails before connecting:

```
creating PodCertificateRequest in ate-system: PodCertificateRequest.certificates.k8s.io
"kubectl-ate-…" is invalid: spec.pkixPublicKey: Invalid value: null
```

The CLI is built against `k8s.io/api v0.37.0`; this cluster serves
`certificates.k8s.io/v1beta1` from 1.35. Substrate supports the latest
stable Kubernetes and the previous minor, so 1.35 is outside its window and
this looks like that skew rather than an install fault. Requests from inside
the cluster through the router were not tried; the actor was verified
through its logs instead. Worth re-checking on 1.36 or after a CLI fix
before the backend relies on the router from outside the cluster.

## What this says for #692

- **Install cost on a non-GKE cluster is small**: one afternoon including
  reading, of which the only cluster-wide change is three beta feature gates.
  A user on Kubernetes 1.37 or newer would skip even that.
- **The lifecycle numbers hold up**: sub-second suspend and resume with
  memory state intact, across workers, on commodity hardware. That is the
  property Celln does not have and the reason the spike exists.
- **Costs to carry**: a PostgreSQL and an S3-compatible store per cluster,
  pre-1.0 APIs ("no backward-compatibility guarantees" in the README), a
  Kubernetes window of two minors, and a toolchain two Go versions ahead of
  Fedora's.
- **Left in place** for phase 0b: Substrate in `ate-system`, the `spike`
  WorkerPool in `ate-spike`, the `echo` template and the suspended `echo-1`
  actor. Next task: the `AgentRun` backend seam (an actor per run, with the
  Agent's key kept outside the sandbox).
