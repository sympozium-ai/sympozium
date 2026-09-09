# Native Celln parent-only controller

The existing controller binary supports `--celln-parent-only`. This explicitly
registers only AgentRun and AgentRunTurn controllers, with no pod builder,
schedule/channel controllers, inventory, or owned Job/Deployment/Service watches.
It requires an explicit `--watch-namespace`, `CELLN_PARENT_CONFIG` and
`CELLN_PARENT_REGISTRATIONS`; missing inputs refuse startup.

New non-Celln, one-shot, and already pod/one-shot-owned runs are ignored before
status or finalizer writes. Already-bound parents remain eligible for original
owner recovery/cleanup even if their current spec has changed. Do not run a
general manager over the same run namespace: these modes are not an automatic
controller-ownership partition.

## Local-host configuration

Build the existing controller with `go build -o target/celln-parent-controller
./cmd/controller`. With an independently prepared owner/template/configuration:

```sh
KUBECONFIG=/absolute/operator/scoped.kubeconfig \
CELLN_PARENT_CONFIG=/absolute/operator/parent-approvals \
CELLN_PARENT_REGISTRATIONS=/absolute/operator/parent-registration.json \
target/celln-parent-controller \
  --celln-parent-only \
  --watch-namespace celln-parent-demo \
  --leader-elect=false \
  --health-probe-bind-address 127.0.0.1:8081 \
  --metrics-bind-address 127.0.0.1:8080
```

Optional `NATS_URL` (or `--nats-url`) enables the event bus without starting
channel routers. Its connection must succeed when explicitly configured. Health
and readiness probes report controller process readiness, not model credentials,
artifact availability or an individual parent's readiness.

Use the opt-in
[namespace RBAC sample](../../config/samples/celln-parent-controller-rbac.yaml),
setting all namespaces and the three grant ConfigMap names deliberately. It
does not grant access to secrets, pods, jobs, deployments, services, unrelated
ConfigMaps or other namespaces. It is not the general controller's role.
Leader election is disabled in the example; enabling it also requires explicitly
scoped Lease permissions. All replicas must share the same durable issuance and
assignment storage; leader election does not replace those replay barriers.

The sample ServiceAccount disables implicit token mounting. A long-running
deployment needs explicitly projected/rotated credentials or a protected local
kubeconfig using a rotating token file. The standalone live-proof supervisor
renews its ten-minute ServiceAccount token through the operator identity and
atomically replaces the controller's private token file. The controller never
receives that operator credential. This is local test-supervisor plumbing, not a
production credential-distribution solution. Never copy an admin kubeconfig into
this controller just to bypass a permission failure.

Local provisioning executes the operator-configured Celln binary against its
local authority root. That root must be the same one served by the configured
owner. A generic controller container does not automatically contain the Celln
binary, host authority store, signed motes, or persistent journals. This guide
does not install them or claim remote provisioning. Do not mount those stores
into tenant agents, nor expose the provisioning command as a tenant HTTP route.

See [the implementation evidence](../design/celln-parent-implementation-notes.md)
for live-proof scope and remaining deployment qualifications.

## TLS parent-owner edge

Keep the Celln dispatcher on a literal loopback HTTP address. Build
`go build -o target/celln-parent-proxy ./cmd/celln-parent-proxy`, then run with
independently provisioned operator certificate/key files:

```sh
target/celln-parent-proxy \
  --listen 127.0.0.1:9443 \
  --backend http://127.0.0.1:8787 \
  --tls-cert /absolute/operator/owner-chain.pem \
  --tls-key /absolute/operator/owner-key.pem
```

This edge requires TLS 1.3, forwards only parent-protocol routes, buffers bounded
requests before forwarding, and preserves bearer authorization for the Celln
owner to validate. It does not issue authority, authenticate Kubernetes tenants,
balance owners or retry POSTs. The backend must be a fixed literal loopback
origin: DNS names and remote plaintext backends are refused. Local processes
remain inside the host trust boundary; do not expose the backend port externally.

Configure the registration/provisioner `target` as the exact HTTPS owner origin
and `caFile` as its explicitly trusted PEM certificate authority when not using
system roots. Never disable certificate verification. The certificate must cover
the configured hostname/IP. Serve the same owner/root after proxy restarts;
changing origins is not permission to replace an existing parent.

The example listens locally. External exposure requires deliberate interface,
firewall and certificate provisioning. Keys are loaded at startup; rotation
requires restarting the edge, not the parent owner. An interrupted request must
be reconciled against its original incarnation/turn. This command does not
automatically provision certificates or deploy the surrounding environment.
