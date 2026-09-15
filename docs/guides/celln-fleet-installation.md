# Celln fleet installation — enduring parents on every KVM node

The fleet is the multi-node form of the native Celln plane. One reviewed
starter package and one scope are installed once; after that, joining a node
is a single label and no namespace needs its own installation. It replaces
the single pinned dispatcher of the
[native installation](celln-native-installation.md), which remains the
small-cluster path.

What runs where:

| Component | Placement | Role |
| --- | --- | --- |
| `celln-node` DaemonSet | every node labeled `celln.dev/kvm=true` | init step prepares `/var/lib/sympozium-celln/<scope>` from the package; main container is the dispatcher serving `/v1/executions` and `/v1/parents` |
| `celln-router` | any node | gateway; discovers owners through the headless `celln-node` Service and binds each parent to the owner that issued it |
| controller | any node | issues parents through the gateway (`POST /v1/parents/provision`); keeps only its own journal/approvals on a `ReadWriteOnce` claim |

Nothing mounts a host path outside `/var/lib/sympozium-celln/<scope>`, the
controller mounts no host state at all, and no node is ever named in values.

## The default install

A released `sympozium` binary pins a starter package the project built and
signed for that release (`hack/build-celln-starter.sh` in the release
workflow: the pinned Celln, its eight brokered tools, busybox and jq, signed
with a key generated in CI and discarded; the identity is the
`celln-starter.json` release asset). With that, the fleet is the default
Celln plane and a bare install is enough:

```sh
export DEEPSEEK_API_KEY=...        # or OPENAI_API_KEY, ANTHROPIC_API_KEY, or
                                   # SYMPOZIUM_CELLN_BACKEND=name=native,provider=llama-server,model=M,endpoint=http://H:8080/v1/chat/completions,allow-insecure=true
sympozium install
```

In a terminal with none of those set, the install asks for a provider and a
key; without a terminal it installs the one-shot router only and says what to
set. The default picks scope `starter`, keeps its records under
`~/.sympozium/celln-fleet/starter`, approves the starter tools (say so in the
output), and the node probe labels every node that has `/dev/kvm` and a
kernel under `/boot` with `celln.dev/kvm=true`; a label an operator set is
never changed. Every flag below still works and overrides the corresponding
default; `--celln-fleet` with your own package keeps the reviewed path.

## Trust and credentials

- **Publisher.** The package is signed with an operator seed. Its publisher key
  (from `celln starter-inspect`) is approved once in values; every node writes
  it into `trusted-closures.json` before admission. The package cannot install
  its own trust.
- **Parent principal.** `sympozium install --celln-fleet` generates one bearer
  credential for the gateway (`celln-router-parent`) and publishes only its
  BLAKE3 hash as the client policy every node installs. Owners never see the
  token; the controller never sees the policy. Rerunning the installer verifies
  the pair and refuses to rotate it by replacement.
- **Model credentials.** Each backend's model profile references an absolute
  file path. In fleet mode that is one Secret (`celln-fleet-model-credentials`,
  one key per backend) mounted read-only into each dispatcher; guests and the
  controller cannot read it.
  This keeps the key inside the cluster trust boundary until the dedicated
  model gateway (#502) is attached to native parents; treat it as interim.
- **Configuration publication.** Only the DaemonSet's init step holds a
  projected ServiceAccount token, scoped to creating and reading one ConfigMap
  in `celln-system`. The dispatcher container has no API credential.

## Prerequisites

1. Nodes with `/dev/kvm`, a readable kernel under `/boot` with its
   `/lib/modules` directory (the dispatcher's readiness gate checks for one;
   Kind nodes ship without a kernel, so copy the host's in for development),
   and an enforcing CNI if you rely on the rendered NetworkPolicies.
2. A reviewed starter package, built **once** on any Linux host with the
   pinned Celln release (`config/celln/release.json`):

   ```sh
   celln starter-package --runtime-dir /opt/celln/runtime \
     --guest-dir /opt/celln/runtime/pilot --kernel /boot/REVIEWED_KERNEL \
     --signing-key /etc/celln-publisher/private.seed \
     --output /var/lib/celln-packages/starter-2026-09
   celln starter-inspect /var/lib/celln-packages/starter-2026-09   # packageHash, publisher
   ```

   Record `packageHash` and the bundle `publisher`. Guard the seed; it is
   never copied into the package or the cluster.
3. The package published as a digest-pinned OCI image whose only content is
   the package directory at `/package`:

   ```sh
   printf 'FROM scratch\nCOPY package /package\n' > Dockerfile
   cp -r /var/lib/celln-packages/starter-2026-09 package
   docker build -t registry.example/celln/starter:2026-09 . && docker push registry.example/celln/starter:2026-09
   docker inspect --format '{{index .RepoDigests 0}}' registry.example/celln/starter:2026-09
   ```

   Use the `repository@sha256:...` form; tags are refused. A private registry
   needs a `.dockerconfigjson` Secret in `celln-system` referenced by
   `celln.fleet.package.pullSecret`.
4. The model provider credential in a local file (one line, at least 24
   characters), or an existing entry for the backend in the `celln-fleet-model-credentials` Secret in
   `celln-system`. Keyless backends such as llama-server need neither; see
   [Model backends](#model-backends).

## Install

```sh
sympozium install -n celln-agents --celln-fleet \
  --celln-fleet-scope starter \
  --celln-fleet-package-image registry.example/celln/starter@sha256:REVIEWED_DIGEST \
  --celln-fleet-package-hash blake3:REVIEWED_PACKAGE_JSON_HASH \
  --celln-fleet-publisher REVIEWED_PUBLISHER_KEY \
  --celln-fleet-model-credential-file /path/to/model-token \
  --celln-fleet-output-dir /ABS/PRIVATE/fleet-starter \
  --celln-native-approve-starter-tools
kubectl label node kvm-a kvm-b celln.dev/kvm=true   # only for nodes the probe does not label
```

Before it touches the cluster the installer sends each backend a one-token
chat request with the key it is about to publish. A provider that is down,
an endpoint that answers the wrong protocol, a wrong model name or a bad key
stops the install with that answer, rather than surfacing later as a lost
parent. Pass `--celln-fleet-skip-preflight` when only the nodes can reach
the endpoint (for example a LAN llama-server the operator's machine cannot
see).

The command runs two phases and is safe to rerun:

1. Publishes the parent principal and model credential, then installs the
   chart with `celln.fleet.*` set. Labeled nodes pull the package by digest,
   verify `package.json` against the hash, run `starter-admit` (real guest
   member checks on KVM) and `starter-configure` once per model backend (the
   `celln-node-configure` DaemonSet; the `celln-node` owner only waits for
   admission), and the first node publishes each backend's `catalogue.json`,
   `configured.json` and `native-template.json` (keys `<backend>.<file>`) as
   the `celln-fleet-configuration` ConfigMap; a later node adds any backend
   the ConfigMap lacks and verifies the rest. Later nodes verify they derived
   identical files; a different package under the same scope is refused.
2. Waits (default 15 minutes, `--celln-fleet-wait`) for that ConfigMap,
   materializes it under the output directory, installs the catalogue and
   grant layers in the target namespace exactly as `celln-tool install-native`
   does, and publishes `celln-parent-config` with a `remoteProvisioner`. The
   final upgrade wires the controller and creates the journal claim.

If the wait expires, label a node, inspect the `prepare` init container's log
in `celln-system`, and rerun the same command: existing trust, credential,
configuration and installation records are verified, never replaced.

## Model backends

A scope carries one or more model backends, all set at install time and
configured on every node. Each backend becomes a runtime profile
(`celln-native-<scope>[-<name>]`) admitted by the scope's single policy, and
every namespace gets one AgentRuntime wrapper and one Agent per backend
(`celln-native` / `celln-agent` for the backend named `native`,
`celln-<name>` / `celln-agent-<name>` for the others), so dozens of enduring
instances on different providers run side by side in the same namespace. The
API lists every backend a namespace may use
(`GET /api/v1/celln-platform/profiles`, fields `backend`, `wrapper`, `agent`)
and the wizard offers each wrapper as a runtime.

Declare backends with `--celln-fleet-backend` (repeatable):

```sh
sympozium install -n celln-agents --celln-fleet ... \
  --celln-fleet-backend name=native,provider=deepseek,model=deepseek-chat,credential-file=/path/to/deepseek-key \
  --celln-fleet-backend name=claude,provider=anthropic,model=claude-sonnet-5,credential-file=/path/to/anthropic-key \
  --celln-fleet-backend name=local,provider=llama-server,model=MODEL.gguf,endpoint=http://HOST:8080/v1/chat/completions,allow-insecure=true
```

Keys: `name` (DNS label, at most 32 characters, unique), `provider`, `model`,
`endpoint`, `protocol` (`openai-chat` or `anthropic-messages`),
`credential-file`, `allow-insecure`. Provider presets:

| Provider | Defaults | Credential |
| --- | --- | --- |
| `deepseek` | `openai-chat`, `https://api.deepseek.com/chat/completions`, model `deepseek-chat` | the DeepSeek API key |
| `openai` | `openai-chat`, `https://api.openai.com/v1/chat/completions` | the OpenAI API key |
| `anthropic` | `anthropic-messages`, `https://api.anthropic.com/v1/messages` | the Anthropic API key |
| `llama-server` | `openai-chat`; `endpoint` required, e.g. `http://HOST:8080/v1/chat/completions` | none |

Any other OpenAI- or Anthropic-compatible service works with a custom
provider name plus `endpoint` and `protocol`. Without `--celln-fleet-backend`
the `--celln-fleet-model-*` flags and `--celln-fleet-model-credential-file`
define the single backend named `native`.

- Each backend's key is published once as its entry in the
  `celln-fleet-model-credentials` Secret in `celln-system` (key = backend
  name) and mounted read-only into every dispatcher as
  `/etc/celln-native/credentials/<name>`. Omit `credential-file` to keep an
  existing entry; keyless backends get a placeholder.
- Every node configures every backend from the same package, so all backends
  share the package's tools, persona and ceilings and differ only in their
  model route and credential profile (`<scope>` or `<scope>-<name>`). Two
  backends may even share an origin and model over different protocols (a
  llama-server's OpenAI and Anthropic routes): each profile is annotated
  with its protocol and its wrappers bind to the matching policy route.
- The endpoint must be reachable from the KVM nodes. Use an IP address for a
  host on a VPN or LAN if the cluster cannot resolve its name.
- Plain HTTP or a private address needs `allow-insecure=true`. The approval
  is recorded on the node's model profile and on each tenant's connection; a
  cluster Secret is never sent over plain HTTP.
- A model request may run for the whole turn, so slow local models are fine
  within the turn deadline.
- **Adding a backend later:** rerun the same install command with one more
  `--celln-fleet-backend`. The node-configure DaemonSet rolls and configures
  the new backend on every node from the already-admitted package, publishes
  its configuration, and the installer adds its profile, route and wrappers.
  The owner DaemonSet is not restarted, so running conversations keep going,
  and namespaces get the new wrapper on first use. A scope holds at most 32
  backends; changing an existing backend's route needs a new scope.

## One-shot runs

A run without `executionLifecycle: enduring` is a one-shot: the same
selection (a backend's wrapper runtime, the cluster tools, the namespace's
model connection) admitted by the same policy, served by a single-turn
native parent. The parent is issued on the least-loaded owner, answers the
task once, the run succeeds with that answer as its `status.result`, and
the parent is stopped so its cells return to the node. A one-shot takes no
follow-up turns; ask again with a new run.

In the UI, an Agent's Harness tab has a **Model backend** picker listing
every backend the namespace's fleet offers (provider, model, protocol);
choosing one creates the backend's wrapper objects on first use and rebinds
the Agent to it. The conversation panel's **Answer once** box sends the next
message as a one-shot. The API call is an enduring conversation minus the
lifecycle and lease (the profile's persona is filled in when omitted):

```sh
curl -X POST "$API/api/v1/runs?namespace=team-a" -d '{
  "agentRef": "celln-agent-claude", "backend": "celln", "executionLifecycle": "one-shot",
  "task": "Where is Botswana?", "systemPrompt": "...", "model": "claude-sonnet-5",
  "modelConnectionRef": "celln-claude",
  "cellnSelection": {"runtimeRef": "celln-claude", "toolRefs": [], "clusterToolRefs": [...]}
}'
```

A one-shot's parent lease is the profile's turn allowance plus two minutes
for admission, within the policy's parent ceiling; its model budget is one
turn of the profile's allowance. Every backend of the scope serves one-shots
the same way, so an Agent picks its provider per run regardless of
lifecycle.

## The toolbox

Every run on the fleet borrows tools from the scope's package, and the
namespace's policy lends exactly those revisions. Two kinds live side by side:

- **Brokered tools** are Celln's own, eight of them: the run's files through
  `workspace-read`, `workspace-write`, `workspace-list`, `workspace-append`,
  `workspace-search` (exact substring, file and line back) and
  `workspace-delete`, and the network through `https-fetch` (GET) and
  `https-post-json` (a JSON object, given as text, posted with no credential).
  They are the only way a cell touches files or the network, through host
  brokers with the quotas the policy shows: every write-like operation is an
  approved effect, reads are not. The hosts the two HTTPS tools may reach are
  the scope's `--celln-fleet-https-host` list (default `example.com`); a
  backend approved with `allow-insecure` may also post over plain HTTP to a
  private host, for example a receiver inside the cluster.
- **Borrowed commands** are ordinary programs taken from container images
  pinned by digest in Celln's catalogue (`tools.toml`), for example busybox's
  grep, sed, awk, sort, uniq, wc, cut, head, tail, tr, base64, sha256sum and
  date, and jq. Nothing is reimplemented. Each command's static executable is
  extracted from the pinned image when the package is built and lent inside
  the signed worker closure; the model calls it through the `celln.argv/v1`
  binding (validated arguments become the command line, no shell), and gets
  its stdout and exit status back. Each such cluster tool names the image it
  came from in `spec.sourceImage`, so the catalogue of layers a fleet borrows
  from is exactly `kubectl get clustercellntool -o custom-columns=NAME:.metadata.name,SOURCE:.spec.sourceImage`.

Build the package with the commands you want:

```sh
celln --root /var/lib/celln-packaging starter-package ... \
  --tool-image busybox --tool-image jq
```

The images are pulled once into that root's image store and verified by
digest. A worker carries at most 24 tools (8 brokered plus 16 commands).

### Extending the toolbox

No recompilation. On the packaging machine:

1. Pin an image into your own catalogue (`~/.celln/tools.toml` under the
   root you package with): `celln image add ghcr.io/example/tool:1.2`. This
   resolves the tag to a digest and records it; a local entry adds a name,
   never authority.
2. Declare the commands the model may call, in that entry:

   ```toml
   commands = [
     { name = "csvq", exec = "/usr/bin/csvq", args = ["{query}"], stdin = "csv",
       description = "Run a SQL query over CSV text.",
       params = [ { name = "query", type = "string", required = true, max = 2048 },
                  { name = "csv", type = "string", required = true, max = 4096 } ] },
   ]
   ```

   Arguments are literals, `{field}` (the value), `{field?FLAG}` (FLAG when a
   boolean is true) or `{field:FLAG}` (FLAG then the value when present);
   `stdin` names the string fed to the program. String parameters and a
   command's output are bounded at 4096 characters each (the tool schema
   subset). The executable must be a static Linux amd64 binary; a
   dynamically linked one is refused with that reason (lending a whole image
   as a closure is the path for those).
3. Rebuild the starter package with `--tool-image NAME` and install it as a
   new scope; the fleet's catalogue, policy and every namespace's wrappers
   follow from the package.

## Leases and budgets

A parent lives for its run's `leaseSeconds` and may spend up to its run's
turn, model-request and output-token budget. The scope's ceilings bound
every run and are configured on every node at install time:

| Flag | Default | Range |
| --- | --- | --- |
| `--celln-fleet-max-lease-seconds` | 86400 (24 h) | 60–86400 |
| `--celln-fleet-max-turns` | 256 | 1–1024 |
| `--celln-fleet-max-model-requests` | 768 | 3–6144 |
| `--celln-fleet-max-output-tokens` | 393216 | 1536–3145728 |

A new conversation asks for a working session inside those ceilings by
default (four hours, 64 turns, 192 requests, 98304 tokens; the API reports
them per profile as `sessionDefaults`). A run asking for more than a ceiling
is refused with `AUTH_LIMIT_RANGE`. When a lease ends no new turn is admitted
and the parent stops; the conversation view shows the deadline and asks for a
new conversation. Leases are not extended in place. Every live parent holds
two cells and its declared memory for its whole lease, so long defaults cost
node capacity while conversations sit idle.

## Authorising namespaces

The installer publishes the reviewed starter configuration **once per scope**
as cluster-scoped objects — `CellnRuntimeProfile` `celln-native-<scope>`
(carrying the native parent/worker material), three `ClusterCellnTool`s
`celln-<scope>-<tool>`, and a `CellnExecutionPolicy` `celln-fleet-<scope>`
with a `host-profile` model route and the reviewed ceilings.

**By default every namespace is authorised** except the system exclusions
(`kube-system`, `kube-public`, `kube-node-lease`, `cert-manager`, the chart's
namespace and `celln-system`) and any namespace labeled
`celln.sympozium.ai/excluded=true`. Fence a namespace off with that label;
nothing else is needed to admit one. Pass `--celln-fleet-authorise=labeled`
to invert this for regulated clusters: then only namespaces labeled
`celln.sympozium.ai/scope=<scope>` are admitted and the installer labels the
`-n` namespace for you.

A namespace's runs select three ordinary workload objects — an `AgentRuntime`
wrapper `celln-native` referencing the profile, an `Agent` `celln-agent` and
a host-profile `ModelConnection` `celln-native`. **They are created on first
use**: the Agent wizard offers the platform profile on the Celln plane in any
authorised namespace and creates the wrappers when you finish, and the API
exposes the same step for automation:

```sh
curl -H "Authorization: Bearer $TOKEN" "$API/api/v1/celln-platform/profiles?namespace=team-b"
curl -H "Authorization: Bearer $TOKEN" -X POST -H 'Content-Type: application/json' \
  -d '{"profile":"celln-native-<scope>"}' "$API/api/v1/celln-platform/wrappers?namespace=team-b"
```

Existing objects are never modified, so a namespace that prefers to manage
its own wrappers in Git can apply them instead (copy them from the install
namespace with `kubectl -n <install-ns> get modelconnection,agentruntime,agent -o yaml`).
No per-namespace install, grant ConfigMaps or copied tools. Enduring runs in
that namespace select `runtimeRef: celln-native`, `clusterToolRefs` from the
shared catalogue, `model.connectionRef: celln-native` and the profile's
persona; the controller resolves policy, profile, tools and route into one
immutable decision, issues the parent through the gateway, and re-checks that
authority before every later turn. A run in an excluded namespace is held
with `CellnParentReady=AdmissionPending (AUTH_POLICY_WITHDRAWN)`; nothing is
issued. The UI wizard offers wrapper runtimes on the Celln plane, lists the
shared catalogue for them and uses the namespace's host-profile
`ModelConnection` instead of asking for a key.

The `ModelConnection`'s `credentialProfile` names the owner-installed model
credential (the scope); `CellnExecutionPolicy` routes with `auth: host-profile`
are the interim boundary until the model gateway (P1) attaches to native
parents and routes switch to `auth: secret`.

## Verify

```sh
kubectl -n celln-system get daemonset celln-node            # DESIRED = labeled nodes
kubectl -n celln-system get pods -l app.kubernetes.io/name=celln-node -o wide
kubectl -n celln-system logs -l app.kubernetes.io/name=celln-router | grep backends
```

Then create an enduring run in an authorised namespace with the shared starter
tools; the installer leaves a ready-made `run.json` under the output directory
(change its namespace for another authorised namespace).
The run's `status.cellnParent.binding.target` is the gateway; the gateway's
`provisions` and `parents` ledgers on the ownership claim record which owner
holds it, and that owner's `authority/parent-journal` carries the incarnation.
Follow-up turns created by hand must carry the run's controller
`ownerReference` (the API server adds it for you); a turn without one is
refused as unbound. `test/integration/test-celln-fleet.sh` runs the whole
journey on a three-node Kind cluster, including a node-leave drain.

## Operations

- **Join a node:** label it. The package is admitted on that node only.
- **Leave a node:** remove the label or drain it. The DaemonSet pod's preStop
  calls `/v1/drain`, which closes admission, stops every parent tree on that
  owner and confirms teardown; those runs report `ContextLost`. Once the
  address has left the fleet the gateway answers `original parent backend
  removed` for its identities, which the controller also treats as context
  loss (and as established teardown when the run is deleted); nothing is
  re-placed.
- **Rolling updates** replace one node's dispatcher at a time with the same
  drain semantics. A dispatcher restart loses live parents on that node.
- **New package:** use a new scope. One scope carries exactly one package
  hash; nodes refuse to publish a differing configuration.
- **Uninstall** retains `/var/lib/sympozium-celln/<scope>` on each node and the
  journal claim; remove them only after every run has been deleted and
  cleanup confirmed.

## Capacity

Each node sizes itself when its dispatcher starts (`celln.fleet.capacity:
auto`, the default): it may reserve `memoryPercent` (75) of the node's memory
— the container's cgroup limit when one is set — for guests, allows one cell
per `cellMemoryBytes` (640 MiB) up to `maxCellsCeiling`, and one broker slot
per cell. The dispatcher logs the result (`celln capacity: node=… cells=…`).
A native parent charges two cells, two broker slots and its declared memory
(1.25 GiB for the starter profile). For nominal node sizes (the kernel reports
slightly less, so real numbers come out a little lower):

| Node memory | Cells | Parents per node |
| --- | --- | --- |
| 16 GiB | 19 | 9 |
| 64 GiB | 76 | 38 |
| 256 GiB | 307 | 153 |

Set `capacity: fixed` with `maxCells`, `memoryBytes` and `egressSlots` to pin
exact numbers, or lower `memoryPercent` on nodes that run other workloads.

Per-parent broker charging arrived in Celln v0.5.12 (celln#112), which this
chart pins; releases before it hold one parent per node whatever the budget says.

The gateway provisions each new parent on the healthy owner with the most
spare cells (then memory), as the owners advertise on `/v1/health`; equally
free owners are chosen in hash order, so placement is deterministic. Once
provisioned, a parent is bound to its owner. An owner that still refuses a
create ends that run with `CellnParentReady` reason `CreateRefused` ("create a
new run"); the incarnation is never retried and the run deletes cleanly.

## Limits

Single active turn per parent, no parent migration or checkpoint recovery,
no live lease extension, and the model credential Secret mounted into every
dispatcher is an interim boundary until the model gateway attaches to native
parents (#464 "Path to production"). One model backend per scope; persona and
tool set are fixed by the starter package (#535).
