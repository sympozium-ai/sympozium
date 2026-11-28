# Celln KVM lane

A scheduled CI lane that boots real Celln cells. It installs the build with
`sympozium install` onto a throwaway multi-node Kind cluster with an
enforcing CNI, runs the fleet journeys and publishes the evidence with every
release. Workflow: `.github/workflows/celln-kvm-lane.yml` (issue #685).

## What it proves

Each run, on a fresh cluster (one control plane, two KVM workers, Calico):

| Step | Script | Proves |
|------|--------|--------|
| One-liner | `test/integration/test-celln-oneliner.sh` | A bare `sympozium install` brings up the fleet, router and model gateway with no model input; the node probe labels the KVM nodes; `sympozium doctor` passes |
| Fleet | `test/integration/test-celln-fleet.sh` | Two teams' own connections and keys; the key canary (each Agent's key and no other reaches the provider); refused key reuse and origin moves; the package gate; node loss ends only the conversation on that node |
| NetworkPolicy | `test/integration/test-celln-netpol-enforced.sh` | `celln-router-ingress` and `celln-node-ingress` are enforced, not just present: a positive control reaches each target, then probes from peers the policy does not admit (a tenant pod, a copied label in another namespace, an unlabelled control-plane pod) must fail to connect. On kindnet these probes connect, so the step fails |
| Three-way | `test/integration/test-celln-three-way.sh` | Two Agents' conversations and a one-shot run, asked the same question at the same time, all answer it; per-run latency recorded |

The evidence (versions, the Sympozium commit, the pinned Celln release, image
IDs, the starter package identity, per-step timings and exit codes, probe
results, three-way timings and redacted journey logs) is gathered by
`hack/celln-lane/collect-evidence.sh`. It is uploaded as a workflow artifact
on every run (kept 90 days) and, on a `v*` tag, attached to the GitHub
release as `celln-kvm-lane-evidence-<tag>.tar.gz` plus a Markdown summary.
Keys, CA material, kubeconfigs and the fleet's materialized configuration are
never copied into the evidence.

The lane builds the images from the checked-out ref (on a tag, the release
commit) rather than pulling the published release images, the same way the
journeys run locally; the release's own `celln-starter.json` is not reused.

Triggers: nightly at 02:30 UTC, on `v*` tags, and manual
`workflow_dispatch`. One run at a time (`concurrency: celln-kvm-lane`, queued,
never cancelled mid-journey); 180-minute job timeout.

## Why a self-hosted host

Celln cells are KVM microVMs, and the journeys need `/dev/kvm` inside the Kind
nodes plus a readable host kernel to boot guests. GitHub's hosted runners
expose `/dev/kvm` only on some larger runners, and nested-virt cloud runners
add cost and an extra virtualisation layer that changes timings. The lane
therefore runs on a dedicated KVM host on Alex's network: **framework**, the
Framework desktop (AMD Ryzen AI MAX+ 395, Fedora 43). The runner carries the
labels `self-hosted, linux, x64, kvm, celln-lane`.

## Isolation from the rest of the host

framework also hosts a live kubeadm cluster, Agent Substrate and other Kind
clusters. The lane must never touch them:

- **Kubeconfig**: the workflow sets `KUBECONFIG` to a per-run file under
  `$RUNNER_TEMP/celln-lane` before any `kind` or `kubectl` call, checks it is
  empty, and `hack/celln-lane/kind-cluster.sh` refuses to run unless that file
  holds exactly one context, `kind-<lane cluster>`. The host's
  `~/.kube/config` is never read or written. The runner user is a separate
  account, so Alex's own kubeconfigs are not readable by it anyway.
- **Names**: every cluster, Docker network and registry the lane creates or
  deletes starts with `celln-lane` (`celln-lane-oneliner`, `celln-lane-fleet`,
  network `celln-lane`, registry `celln-lane-registry` on `127.0.0.1:5101`);
  the scripts refuse any other name, so cleanup can never delete another
  cluster.
- **Subnets**: the lane's Docker network is `172.29.0.0/24`, pods
  `10.123.0.0/16` (Calico's IPv4 pool is set to match), services
  `10.124.0.0/16`, IPv4 only. These avoid framework's ranges as read on
  2026-10-30: kubeadm/flannel pods `10.244.0.0/16`, the kubeadm default
  service range `10.96.0.0/12`, Docker `172.17-172.19.0.0/16`, the LAN
  `192.168.1.0/24` and tailscale `100.64.0.0/10`. `kind-cluster.sh` checks
  the chosen subnets against `ip -4 route` and the kubeadm service range
  before creating anything; override with `CELLN_LANE_DOCKER_SUBNET`,
  `CELLN_LANE_POD_SUBNET`, `CELLN_LANE_SERVICE_SUBNET`.
- **Resources**: Kind has no resource settings, so each node container is
  capped with `docker update`: control plane 4 CPUs / 6 GiB, each worker
  8 CPUs / 12 GiB (guest microVM memory counts against the worker), about
  30 GiB in total at peak. Tune with `CELLN_LANE_CP_CPUS`,
  `CELLN_LANE_CP_MEMORY`, `CELLN_LANE_WORKER_CPUS`, `CELLN_LANE_WORKER_MEMORY`.
  Image builds run outside these caps; the schedule is off-hours for that
  reason.
- **Cleanup**: `if: always()` steps delete both clusters, the registry, the
  Docker network, the per-run images and the work directory, and the next run
  deletes any leftovers (by the lane's own names only) before starting.
- **Tools**: the workflow downloads a pinned, sha256-checked `kind` (v0.30.0)
  into the job's temp directory; Go comes from `actions/setup-go`. Nothing is
  installed system-wide.

## Security model

- **No pull-request triggers.** The workflow runs only on `schedule`, `v*`
  tag pushes and `workflow_dispatch` (which needs write access). It has no
  `pull_request` or `pull_request_target` trigger and must never get one: a
  self-hosted runner on a public repository would otherwise run fork code on
  framework. The job is also guarded with
  `if: github.repository == 'sympozium-ai/sympozium'`.
- **Repository-scoped runner.** Registered to `sympozium-ai/sympozium` only,
  not the organisation.
- **Environment gate.** The job runs in the `celln-kvm-lane` environment,
  limited to `main` and `v*` tags, so a dispatch from any other branch never
  reaches the host or the secret.
- **Least privilege token.** The lane job has `contents: read`. Attaching
  evidence to a release runs in a separate job on a GitHub-hosted runner with
  `contents: write`, so the write token never reaches framework. Checkout uses
  `persist-credentials: false`.
- **Dedicated, unprivileged user.** The runner runs as its own account
  (`celln-lane`), with no sudo, in the group that owns `/dev/kvm` and in
  `docker`.
- **Docker is root-equivalent.** Membership of `docker` lets the runner user
  control every container on the host, including the other Kind clusters and
  Agent Substrate, and effectively become root. The lane's scripts only touch
  `celln-lane*` objects, but a malicious workflow change could do more. This
  is accepted and mitigated by the trigger rules, the environment gate and
  review of every change under `.github/workflows/` (consider a CODEOWNERS
  entry). Rootless Docker for the runner user would remove the root
  equivalence and the shared daemon, but Kind's privileged nodes, KVM and the
  fleet's privileged DaemonSets have not been tested under rootless Docker;
  try it before relying on it.
- **Any workflow can target the runner.** A repository runner accepts jobs
  from any workflow in the repository that names its labels. Keep the fork
  approval setting below on, and review workflow changes from collaborators.
- **Ephemeral cluster.** Every run starts from a new cluster and deletes it.
- **Secrets.** Only the three-way step sees the optional
  `CELLN_LANE_ACCEPT_API_KEY`; the script reads it from the environment,
  posts it once on stdin and never prints it. The fleet journey drops every
  provider key from its environment by design.

## Admin runbook (framework)

These steps need root on framework and repository admin rights on GitHub.
The lane fails fast in its preflight until they are done.

### 1. Runner user and device access

```bash
sudo useradd --create-home --shell /bin/bash celln-lane
# /dev/kvm on framework is root:qat 0660 (not group kvm).
sudo usermod -aG qat celln-lane
# Docker access (root-equivalent; see Security model).
sudo usermod -aG docker celln-lane
```

Alternatively, give `/dev/kvm` to the `kvm` group with a udev rule and add
the user to `kvm` instead of `qat`:

```bash
echo 'KERNEL=="kvm", GROUP="kvm", MODE="0660"' | sudo tee /etc/udev/rules.d/65-kvm.rules
sudo udevadm control --reload && sudo udevadm trigger --name-match=kvm
sudo usermod -aG kvm celln-lane
```

Check: `sudo -u celln-lane test -r /dev/kvm -a -w /dev/kvm && echo ok`. The
host kernel `/boot/vmlinuz-$(uname -r)` must stay world-readable (it is on
Fedora). The journeys also use `python3`, `openssl`, `jq`, `curl`, `skopeo`,
`cpio`, `gcc`, `mke2fs`, `debugfs`, `docker buildx` and `kubectl` from the
system packages (all present on framework).

### 2. Register the runner (repository-scoped)

Get a registration token as a repository admin, either from
**Settings → Actions → Runners → New self-hosted runner** or:

```bash
gh api -X POST repos/sympozium-ai/sympozium/actions/runners/registration-token --jq .token
```

The token is valid for one hour; do not paste it anywhere else. As
`celln-lane`, download the current runner from
<https://github.com/actions/runner/releases> (verify its sha256 against the
release page), then:

```bash
sudo -iu celln-lane
mkdir actions-runner && cd actions-runner
curl -fsSLO https://github.com/actions/runner/releases/download/v<VERSION>/actions-runner-linux-x64-<VERSION>.tar.gz
echo "<SHA256>  actions-runner-linux-x64-<VERSION>.tar.gz" | sha256sum -c
tar xzf actions-runner-linux-x64-<VERSION>.tar.gz
./config.sh --url https://github.com/sympozium-ai/sympozium \
  --name framework-celln-lane --labels kvm,celln-lane \
  --unattended --token <REGISTRATION_TOKEN>
exit
```

`config.sh` adds `self-hosted, Linux, X64` itself. A persistent runner is
recommended: the lane cleans up after itself and before each run. An
`--ephemeral` runner (one job, then deregistered) gives a cleaner host per
run but needs something to re-register it with a fresh token after every job.

### 3. Install it as a systemd service under that user

```bash
cd ~celln-lane/actions-runner
sudo ./svc.sh install celln-lane
sudo ./svc.sh start
sudo ./svc.sh status
```

### 4. Environment, variables and secret

In **Settings → Environments**, create `celln-kvm-lane`:

- **Deployment branches and tags**: selected only, `main` and the tag
  pattern `v*`.
- **Variables** (required): `CELLN_LANE_LLAMA_ORIGIN`, the
  `scheme://host:port` of a keyless OpenAI-compatible server (e.g.
  llama-server) reachable from the Kind nodes on a private address, and
  `CELLN_LANE_LLAMA_MODEL`, the model it serves. The fleet install approves
  this origin as a private origin for the gateway.
- **Optional keyed acceptance**: secret `CELLN_LANE_ACCEPT_API_KEY` with
  variables `CELLN_LANE_ACCEPT_ENDPOINT` (an `https://` chat-completions
  URL), `CELLN_LANE_ACCEPT_MODEL` and `CELLN_LANE_ACCEPT_PROVIDER` (default
  `openai`). When the secret is unset, the three-way step uses the keyless
  server.

### 5. Repository settings

- **Settings → Actions → General → Approval for running fork pull request
  workflows from contributors**: *Require approval for all outside
  collaborators*.
- Keep **Workflow permissions** at read-only by default (the lane asks for
  `contents: write` only in its GitHub-hosted release job).
- Optionally add `.github/workflows/ @sympozium-ai/maintainers` to CODEOWNERS
  so workflow changes always get a maintainer review.

### 6. First run

Run **Actions → Celln KVM lane → Run workflow** on `main` and check the job
summary and the `celln-kvm-lane-evidence-<run id>` artifact. Afterwards
`docker ps -a --filter name=celln-lane` and
`docker network ls --filter name=celln-lane` on framework should be empty.

## Running the lane by hand

On any Linux host with `/dev/kvm`, Docker, Go and kind:

```bash
export CELLN_LANE_WORK=/tmp/celln-lane KUBECONFIG=/tmp/celln-lane/kubeconfig TAG=celln-lane-local
export KIND_EXPERIMENTAL_DOCKER_NETWORK=celln-lane FLEET_REGISTRY_NAME=celln-lane-registry FLEET_REGISTRY_PORT=5101
mkdir -p "$CELLN_LANE_WORK" && : > "$KUBECONFIG"
hack/celln-lane/prepare.sh
hack/celln-lane/kind-cluster.sh up celln-lane-fleet
FLEET_CLUSTER=celln-lane-fleet KEEP_CLUSTER=1 SYMPOZIUM_IMAGE_TAG=$TAG CELLN_IMAGE=celln:$TAG \
  CELLN_BUNDLE=$CELLN_LANE_WORK/bundle LLAMA_ORIGIN=http://<host>:<port> LLAMA_MODEL=<model> \
  test/integration/test-celln-fleet.sh
FLEET_CLUSTER=celln-lane-fleet test/integration/test-celln-netpol-enforced.sh
hack/celln-lane/kind-cluster.sh down celln-lane-fleet
```
