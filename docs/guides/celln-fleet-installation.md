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
export DEEPSEEK_API_KEY=...        # every present key (DeepSeek, OpenAI, Anthropic) becomes a backend;
                                   # the first is the default, the rest are named after their provider
export SYMPOZIUM_CELLN_BACKEND='name=native,provider=llama-server,model=M,endpoint=http://H:8080/v1/chat/completions,allow-insecure=true'
                                   # explicit specs, several separated by semicolons, come first
sympozium install
```

Several providers side by side is the normal case: each backend is a
wrapper in every namespace (`celln-<name>`) and each Agent picks its
backend in the wizard. Rerunning the install with one more backend adds it
to the running fleet without restarting anyone's conversation.

In a terminal with none of those set, the install asks for a provider and a
key; without a terminal it installs the one-shot router only and says what to
set. The default picks scope `starter`, keeps its records under
`~/.sympozium/celln-fleet/starter`, approves the starter tools (say so in the
output), and the node probe labels every node that has `/dev/kvm` and a
kernel under `/boot` with `celln.dev/kvm=true`; a label an operator set is
never changed. To keep such a node out of the fleet, or to drain it, set
`celln.dev/kvm=false` explicitly: a removed label is added back. Every flag below still works and overrides the corresponding
default; `--celln-fleet` with your own package keeps the reviewed path.
The same command installs [ergoz](https://github.com/sympozium-ai/ergoz)
(accelerator power telemetry, pinned in `config/ergoz/release.json`) into
`ergoz-system`; `--no-ergoz` skips it.

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
   `/lib/modules` directory (the dispatcher's readiness gate checks for one),
   and an enforcing CNI if you rely on the rendered NetworkPolicies. The node
   probe labels a node that has both `celln.dev/kvm=true`; the fleet
   DaemonSets run only there, and the install waits for the first such node.
2. **Kind is a development environment only.** Each cell is a microVM and
   the VMM boots it from a kernel image file on the node, matched to
   `/lib/modules`. On a Linux host, Kind nodes already see `/dev/kvm` and
   mount the host's `/lib/modules`, but ship no `/boot`, so nothing gets
   labelled and a plain install waits for a node that never qualifies. The
   mitigation is to copy the host's **running** kernel (its version matches
   the mounted modules) into each node, before or during the install; the
   probe labels the node within seconds:

   ```sh
   docker cp /boot/vmlinuz-$(uname -r) kind-control-plane:/boot/   # and each worker
   ```

   Kind on macOS or Windows runs inside a VM without `/dev/kvm` and cannot
   run the fleet. The installer prints this hint when no node has qualified
   within the first minute of its wait. Real nodes need nothing of the sort.
3. A reviewed starter package, built **once** on any Linux host with the
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
4. The package published as a digest-pinned OCI image whose only content is
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
5. The model provider credential in a local file (one line, at least 24
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
  backends; changing an existing backend's route needs a new scope (see
  **Moving to a new package or scope** below).

### Model parameters

A backend may carry **model parameters**: a JSON object the Celln host merges
into every request it sends to that backend's provider. The host injects
them after the guest has built its request, so the agent (the guest) cannot
see, set or change them, and they apply to every conversation on the backend.
Use them for provider switches Celln has no field for — sampling settings,
or turning a reasoning phase off.

The case that needs them: Celln allows **512 output tokens per model
request**. A reasoning model (Qwen3, DeepSeek-R1 and the like) served by
llama-server can spend all of them thinking and return an empty answer
(`finish_reason: length`), which fails the turn with `final answer is
empty…`. The server flag `--reasoning-budget 0` does not help; the switch has
to travel with each request:

```sh
cat >/etc/sympozium/qwen-parameters.json <<'JSON'
{"chat_template_kwargs": {"enable_thinking": false}}
JSON

sympozium install --celln-fleet --celln-native-approve-starter-tools \
  --celln-fleet-backend name=native,provider=deepseek,model=deepseek-chat,credential-file=/path/to/deepseek-key \
  --celln-fleet-backend name=qwen,provider=llama-server,model=MODEL.gguf,endpoint=http://HOST:8080,allow-insecure=true,parameters-file=/etc/sympozium/qwen-parameters.json
```

`parameters-file` is an absolute path to a file holding the object (a file,
because JSON contains the commas that separate the flag's pairs). With the
single-backend flags use `--celln-fleet-model-parameters-file`. Through the
API, send the object as `parameters`
(`{"name":"qwen","provider":"llama-server","endpoint":"http://HOST:8080","allowInsecure":true,"parameters":{"chat_template_kwargs":{"enable_thinking":false}}}`).
In the console, both add-a-fleet-backend forms (the Create Agent wizard's
Provider step and an Agent's Harness tab) have **Advanced: model parameters**:
tick **Disable thinking (reasoning models)** — offered for llama-server and
for Custom with the OpenAI chat protocol — or write the JSON; the two edit
the same object. `GET /api/v1/celln-platform/backends` returns each backend's
`parameters`, and the console shows them next to the backend.

Rules (Celln's own; the installer, the API and the console check them first
and name the rule that is broken, and Celln checks again on every node):

- at most 16 top-level keys; every key, at any depth, matches
  `^[a-z][a-z0-9_]{0,63}$`;
- values are booleans, finite numbers, strings of at most 256 bytes (no NUL),
  objects under the same rules, or arrays of at most 8 scalars; no `null`;
  at most 3 levels of nesting, the object itself being the first;
- at most 2048 bytes serialized;
- these top-level keys are reserved, because Celln sets them on every request:
  `model`, `messages`, `system`, `stream`, `stream_options`, `max_tokens`,
  `max_completion_tokens`, `n`, `tools`, `tool_choice`, `functions`,
  `function_call`, `parallel_tool_calls`, `user`. Parameters therefore cannot
  raise the 512-token allowance.

The one-token probe sends the parameters too, so one the provider rejects
(HTTP 400) is reported at install or add time rather than as lost turns.

**Celln version.** Parameters need a Celln release **newer than v0.5.22** on
the fleet nodes; the installer image (`celln-node-configure`) carries Celln,
so that means a Sympozium release that pins such a Celln. An older Celln
refuses a plan that names `parameters`: the backend is never configured, the
`celln-node-configure` log shows Celln's refusal followed by `backend NAME
sets model parameters: they need a Celln release newer than v0.5.22…`, and
the installer's wait (or the API's `error:` state) says the same. A backend
without parameters gets exactly the plan it always got, on any Celln.

**Parameters cannot be changed in place.** A node configures a backend once,
and a published backend's configuration is never rewritten, so an install
that asks for other parameters on a published backend (adding, changing or
removing them) stops with `model parameters of a published backend cannot
change` instead of ignoring the change. Add a backend under another name
with the parameters you want (`--celln-fleet-backend name=qwen-2,…` or the
API; the API refuses a name that exists) and move Agents to it on their
Harness tab, or move the fleet to a new scope with `--celln-fleet-scope` and
`--celln-fleet-replace-package`, which configures every backend afresh and
ends every live parent.

In a values file, `celln.fleet.backends[].parameters` takes the object
itself.

#### Output tokens per request

Celln lets one model request produce **512 output tokens** by default. That
suits chat with thinking disabled, and caps an answer near 2 KB. A backend
may set its own cap, **256–4096**, on all three paths:

```bash
# the installer, in a backend's spec …
sympozium install \
  --celln-fleet-backend name=native,provider=deepseek \
  --celln-fleet-backend name=thinker,provider=llama-server,model=qwq.gguf,endpoint=http://10.0.0.5:8080,allow-insecure=true,max-output-tokens=2048

# … or for the single --celln-fleet-model-* backend
sympozium install --celln-fleet-model-provider llama-server … \
  --celln-fleet-model-max-output-tokens 2048
```

| Path | Field |
| --- | --- |
| Installer | `max-output-tokens=N` in `--celln-fleet-backend`, or `--celln-fleet-model-max-output-tokens N` |
| Chart values | `celln.fleet.backends[].maxOutputTokens` (or `celln.fleet.model.maxOutputTokens`) |
| API / console | `maxOutputTokens` in `POST /api/v1/celln-platform/backends`; "Max output tokens per request" under **Advanced** in both add-a-fleet-backend forms |

Absent, `0` and `512` all mean the default, and a default backend carries
nothing: its values, its entry in `FLEET_BACKENDS` and its
`starter-configure` plan are byte for byte what they were before the field
existed, so nodes do not roll and any Celln configures it. A non-default
value becomes `modelConnection.maxOutputTokens` in the plan and
`model.maxOutputTokens` in the published `configured.json`.

**Which value.** 512 suits chat with thinking disabled. A reasoning model
left thinking needs 2048–4096, which costs 4–8× the tokens per turn and may
exceed the 60-second turn limit on a slow local model. If the model only
needs to stop thinking, prefer the parameter above; raise the cap when you
want it to think, or need answers longer than about 2 KB (one committed
answer is still at most 8192 bytes; a longer one fails the turn with `final
answer exceeds …`, and the remedy is a shorter answer or a lower cap).

**The 6× rule.** A turn reserves 6 model requests and **6 × the backend's
cap** in output tokens from its conversation's lifetime totals: 3072 at the
default, 12288 at 2048, 24576 at 4096. The runtime profile publishes it as
`spec.native.turnModelRequests` / `turnOutputTokens`. A scope has **one**
policy with one set of ceilings, while its backends may now differ in what a
turn costs, so:

- The installer checks `--celln-fleet-max-output-tokens` against the **most
  expensive** install-time backend: it must be at least 6 × the largest cap,
  and the refusal names the backend. The maximum is 25165824
  (1024 turns × 6 × 4096).
- When you do not pass `--celln-fleet-max-output-tokens`, the installer
  sizes it so the default 256 turns stay reachable on that backend
  (256 × 6 × the largest cap: 3145728 at 2048, 6291456 at 4096) and prints
  one line saying so. Model requests stay 256 × 6.
- `sessionDefaults`, the sample run and the console's defaults are computed
  **per profile**: the largest turn count up to 64 whose requests and tokens
  the ceilings pay for at that profile's own allowance.
- A backend added later through the API or console, with a higher cap than
  the ceilings were sized for, still works but buys fewer turns. The API
  answers with a `warning` when the ceilings pay for fewer than the usual 64
  turns on it (the console shows it), refuses a cap whose single turn the
  ceilings cannot pay for, and `sympozium doctor`'s "Fleet turn budget"
  check reports every profile's allowance and the turns it affords, with
  the ceilings to install for the costliest one.

**Celln version and package.** A non-default cap needs a Celln release
**newer than v0.5.23** on the fleet nodes **and a starter package built by
that Celln** (the guest worker has to send the configured value; Celln
refuses at configure time otherwise). Sympozium's release builds the package
from the Celln it pins, so a new install of such a release has both. An
**existing** fleet keeps the package it was installed with: move it with
`--celln-fleet-replace-package` (see [Moving to a new package or
scope](#moving-to-a-new-package-or-scope); live parents are lost) before
adding a backend with its own cap. On an older Celln the plan is refused,
the backend is never configured, and the `celln-node-configure` log, the
installer's wait and the API's `error:` state all name the requirement
(`… needs a Celln release newer than v0.5.23 …`).

**It cannot be changed in place**, for the same reason as parameters: an
install that asks for another cap on a published backend (raising, lowering
or removing it) stops with `max output tokens per request of a published
backend cannot change` and the same three ways forward: the backend under
another name through the installer or the API, or a new scope with
`--celln-fleet-replace-package`.

The one-token preflight probe is unchanged: it always asks for one token,
whatever the backend's cap.

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

### Adding a backend from the API or the UI

A running fleet takes a new backend without the installer:

```sh
curl -X POST -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  http://SYMPOZIUM/api/v1/celln-platform/backends \
  -d '{"name":"claude","provider":"anthropic","model":"claude-sonnet-5","credential":"sk-ant-..."}'
```

The API probes the endpoint with the key (`skipPreflight: true` when only
the nodes can reach it), publishes the key as that backend's entry in the
fleet's credential Secret, appends the backend to the
`celln-fleet-backends-extra` ConfigMap in `celln-system` and rolls the
configure DaemonSet. Every node then configures the backend from the same
admitted package; owners and their conversations are untouched. Once the
nodes have published its configuration, the API server installs the
backend's runtime profile, policy route and wrappers, and
`GET /api/v1/celln-platform/backends` reports it `ready`; until then it
shows `pending` or `configuring`, or `error: …` with the reason. The
`configuring` state includes a deliberate wait of about 90 seconds for the key
to reach the running dispatchers; the state text and the Create Agent wizard
say so while it lasts. The Agent
page's backend picker has the same form. A backend named at install cannot
be added again, and a key already published for a name is never replaced.

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
3. Rebuild the starter package with `--tool-image NAME` and install it with
   `--celln-fleet-replace-package` (see **Moving to a new package or scope**);
   the fleet's catalogue, policy and every namespace's wrappers follow from
   the package.

## Conversations survive their node

A parent's live context lives in its cell, on one node. When that node leaves
the fleet or the owner process is replaced, the run reports `ContextLost` and
the conversation carries on in a **new run**: the controller creates it with
the same Agent, backend, tools and limits, seeded with the exchanges recorded
so far (the initial task and its answer, then every succeeded turn), and the
gateway places its parent on any node with capacity. The lost run ends with
`status.cellnParent.continuedBy` naming the continuation; the continuation
carries `spec.conversation.continuesFrom` and `spec.conversation.seed`. Its
first turn is a fixed resume message, so the new parent shows what it
remembers before you send anything.

The same move is available on request: **Restart elsewhere** in the UI, or
`POST /api/v1/runs/{name}/continue?namespace=…&uid=…`, creates the seeded
continuation and deletes the old run (`keep=true` leaves it). Memory is
bounded by the worker task the fleet's starter package takes, which the
scope's `CellnRuntimeProfile` reports as `spec.limits.taskBytes`: about
15 KiB of text (16384 − 512 bytes, at most 16 exchanges) on a current
package, about 1.5 KiB on an older one, whose guest refuses a larger seed
and would fail to start. A long conversation keeps its most recent
exchanges, and only committed answers are remembered, never failed turns,
tool output or instructions. A run with
`spec.conversation.continuation: none` is not re-created; a chain stops after
16 automatic continuations.

## Leases and budgets

A parent lives for its run's `leaseSeconds` and may spend up to its run's
turn, model-request and output-token budget. The scope's ceilings bound
every run and are configured on every node at install time:

| Flag | Default | Range |
| --- | --- | --- |
| `--celln-fleet-max-lease-seconds` | 86400 (24 h) | 60–86400 |
| `--celln-fleet-max-turns` | 256 | 1–1024 |
| `--celln-fleet-max-model-requests` | 1536 | 6–6144 |
| `--celln-fleet-max-output-tokens` | 786432 (256 × 6 × the largest backend cap when that is above 512) | 3072–25165824 |

One turn of the current starter package may make 6 model requests (up to 4
tool calls, then an answer) and produce 3072 output tokens, and **every turn
reserves that whole allowance** from the parent's lifetime totals whether or
not it spends it. Size the totals as turns × allowance: the defaults are
256 × 6 and 256 × 3072, and the minima are one turn's worth. Totals that
afford fewer turns than `max-turns` end the conversation early, at
`min(requests / 6, tokens / 3072)` turns. A backend that sets its own
[output tokens per request](#output-tokens-per-request) reserves 6 × that
instead of 3072, and the ceilings are sized and checked for the most
expensive backend of the scope.

One message is at most 2048 bytes and one committed answer at most 8192
bytes (2048 on a fleet still running an older starter package, where a
longer answer is a failed turn).

A new conversation asks for a working session inside those ceilings by
default (four hours, 64 turns, 384 requests, 196608 tokens at the default
allowance; the API reports them per profile as `sessionDefaults`, with the
largest turn count up to 64 that the ceilings pay for at that profile's own
per-turn allowance). A run asking for more than a ceiling
is refused with `AUTH_LIMIT_RANGE`. When a lease ends no new turn is admitted
and the parent stops; the conversation view shows the deadline and asks for a
new conversation. Leases are not extended in place. Every live parent holds
two cells and its declared memory for its whole lease, so long defaults cost
node capacity while conversations sit idle.

### Ceilings sized for an older package

Before the per-turn allowance doubled, a turn reserved 3 requests and 1536
tokens, and the default ceilings were 768 requests and 393216 tokens for 256
turns. Those totals buy **half the turns** at 6 and 3072: a conversation
under them ends after 128 turns, and an Agent that saved the old session
defaults (64 turns, 192 requests, 98304 tokens) ends after 32.

Ceilings are configured on the nodes together with the package and are
never rewritten for an unchanged package. They are rewritten when the scope
moves to another package, which is how the current allowance arrives in the
first place:

```sh
sympozium install --celln-fleet --celln-fleet-replace-package
# or, to choose the totals yourself (turns × 6, turns × 3072):
sympozium install --celln-fleet --celln-fleet-replace-package \
  --celln-fleet-max-turns 512 --celln-fleet-max-model-requests 3072 \
  --celln-fleet-max-output-tokens 1572864
```

The move takes the ceilings from the `--celln-fleet-max-*` flags (the new
defaults when omitted), so passing the old totals explicitly keeps the
shortfall. `sympozium doctor` reports it as **Fleet turn budget** with the
turn a conversation would end at. Agents keep the budget they were saved
with: raise `spec.execution.enduring.maxModelRequests` and `maxOutputTokens`
on an Agent created before the move (the wizard offers the new session
defaults for new ones). The alternative is to accept fewer turns; a
conversation that runs out can be carried on with **Restart elsewhere**.

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

### Seeing cells in the console

The Harnesses page and the topology show `celln ps` for every fleet node
(`GET /api/v1/celln-platform/cells`), and the card says where the data came
from. The API server first asks the Celln gateway for `GET /v1/cells` with the
same read-only capability token it uses for `/v1/capabilities`
(**source: gateway**); this also carries each parent's live owner status
(`Ready`, `TurnActive`, `ContextLost`, …), which the console shows in place of
the run's phase. Celln releases that predate that endpoint answer 404, which
the API server remembers for five minutes, and it then reads the
`celln-fleet-cells` ConfigMap that every node's `celln-node-configure` pod
publishes every two seconds (**source: node reports**). The ConfigMap is also
used when the gateway is busy or unreachable, and for a single node whose
dispatcher the gateway could not list; a backend with neither appears as
`node-<index>` with the gateway's reason. The node reporter and its chart
wiring stay installed for older Celln releases and become redundant once the
pinned Celln release serves `/v1/cells`.

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
- **New package or scope:** see below. One scope carries exactly one package
  at a time; nodes refuse to publish a differing configuration unless the
  installer approved the move.
- **Uninstall** retains `/var/lib/sympozium-celln/<scope>` on each node and the
  journal claim; remove them only after every run has been deleted and
  cleanup confirmed.

## Troubleshooting an install

On a machine that has been used before, start with `sympozium doctor`. It only
reads, prints one `PASS`/`WARN`/`FAIL` line per check with the exact command
that fixes each problem, and exits 1 when anything fails (`--json` for
scripts). `sympozium install` runs the same leftover checks before it changes
anything and stops with the complete list rather than failing part-way.

- **`… exists and cannot be imported into the current release: invalid
  ownership metadata`.** Objects of an older, partly removed install are in the
  way, and Helm names only the first. `doctor` (check *Ownership*) lists every
  one at once with a single remedy each: the `kubectl label`/`annotate` pair
  that adopts it, or `kubectl delete`. `sympozium install --adopt-existing`
  adopts the safe kinds for you (Namespace, ServiceAccount, ConfigMap, Secret,
  Service, NetworkPolicy, PersistentVolumeClaim and the chart's own
  `sympozium.ai` resources). Deployments, DaemonSets and other kinds are never
  adopted, because an old spec may be incompatible: delete them and the chart
  recreates them. An object another Helm release claims is never taken
  automatically.
- **A CRD or namespace stuck `Terminating`** (the install used to print only
  `Detected changes to resource agentruns.sympozium.ai which is currently being
  deleted`). Custom resources still hold a finalizer such as
  `sympozium.ai/agentrun-finalizer` and no controller is left to remove it.
  `doctor` (check *Terminating*) names each holder and prints its `kubectl patch
  … '{"metadata":{"finalizers":null}}'` command. Clearing a finalizer skips
  the controller's cleanup for that object, so only do it for an install that
  is gone, and remove leftover run pods by hand.
- **`celln-node` never starts and the install sits in its wait.** `doctor`
  (check *Fleet pods*) reports an init container that has run for more than two
  minutes, or is backing off, with its last log lines. The usual cause is a
  state directory `/var/lib/sympozium-celln/<scope>` left by an older install
  under another uid: `wait-prepared` now fails at once with `cannot read the
  Celln state directory …`, naming the path, its owner and mode, instead of
  waiting for ever, and `celln-node-configure` resets the owner to root (mode
  0700, nothing opened to other users) on its next pass, after which the pod
  starts by itself. If it does not, move the directory aside on the node
  (`sudo mv /var/lib/sympozium-celln/<scope>{,.old}`) or `sudo chown 0:0` it;
  with SELinux enforcing also `sudo chcon -R -t container_file_t` it.
- **A CLI built from source ran an old installer image** (`error: exact
  five-bundle starter package required`). A build without a release version
  used to default the installer image to the mutable `latest` tag, which a
  node may have cached months ago. It now uses the embedded chart's
  `appVersion` (`v`-prefixed), like the control-plane images, and says so.
  `--image-tag` still overrides the control-plane images and
  `--celln-installer-image` the installer image.

`doctor` also reports whether any node carries `celln.dev/kvm=true`, whether
every model backend has its key in `celln-fleet-model-credentials`, and whether
the published fleet package differs from the one this CLI installs, in which
case the upgrade needs `--celln-fleet-replace-package` (next section).

## Moving to a new package or scope

A Sympozium release carries a new starter package only when the package's
inputs changed: the pinned Celln release (`config/celln/release.json`), the
packaging recipe (`hack/build-celln-starter.sh`) or the tool images it
packages. Every other release republishes the previous release's package
unchanged (the same image digest, package hash and publisher key), so
upgrading `sympozium` and rerunning `sympozium install` keeps the fleet's
package, restarts no dispatcher and says so:

```console
$ sympozium install ...
  Celln fleet package unchanged (blake3:d365…); live conversations are kept
```

To see whether a release changes the package before you upgrade, compare the
`celln-starter.json` asset of the two releases. `inputs` is the fingerprint of
the package's inputs (`hack/celln-starter-inputs.sh` prints it, and
`--manifest` shows what it covers); when it and `packageHash` are equal, the
package is the same one:

```console
$ for tag in v0.10.80 v0.10.81; do gh release download "$tag" --repo sympozium-ai/sympozium \
    --pattern celln-starter.json --output - | jq -c '{inputs, packageHash}'; done
```

Releases published before the fingerprint was recorded have no `inputs`
field, and each of them carries its own package. A maintainer can also force
a release to build a new package (`force_starter_rebuild` on the release
workflow), which changes `packageHash` while `inputs` stays the same, so
`packageHash` is the field that decides.

`--celln-fleet-replace-package` is therefore only needed when upgrading across
a release whose package changed, for a package you rebuilt yourself, or for a
changed `--celln-fleet-scope`. Because a move ends every live parent on the
fleet, the installer checks the published configuration before it changes
anything and refuses unless you approve the move:

```console
$ sympozium install ...
Error: the Celln fleet runs package blake3:d365… in scope starter, and this
install would move it to package blake3:e75a… in scope starter.
```

Choose one:

- **Move the fleet:** rerun the same command with
  `--celln-fleet-replace-package`. The installer records the approved package
  and scope on `celln-fleet-configuration` before upgrading the chart. The
  node-configure pods admit the new package next to the old one and publish
  the new configuration: one node replaces the whole configuration with a
  conditional update, and the others verify it. A node still running the old
  package never publishes over an approved replacement. The installer then
  waits for that publication, replaces the scope's runtime profiles and
  cluster tools (they are immutable, so they are deleted and created again),
  rewrites the policy, removes tools the new package dropped (and the old
  policy after a scope change), and points every namespace's platform-managed
  wrappers at the new profiles. Wrappers a namespace created itself are left
  alone. The owner DaemonSet rolls because its package changed, so every live
  parent reports `ContextLost`; conversations continue in new runs.
- **Keep the installed package:** pin it with `--celln-fleet-package-image`,
  `--celln-fleet-package-hash` and `--celln-fleet-publisher`, using the values
  from the `celln-starter.json` asset of the release that installed it.

Each node only trusts the current package's publisher key, so any parent
still running from the old package fails at its next turn even before its
owner restarts. Old package files stay in the node's store (they are
content-addressed and harmless).

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
