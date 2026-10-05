# Celln Backend (Hardware-Isolated Execution)

[Celln](https://github.com/sympozium-ai/celln) runs agent work in KVM
microVMs ("cells") instead of Kubernetes pods. An `AgentRun` selects it with
`spec.backend: celln` (or inherits it from the Agent's
`spec.execution.backend`). The default `job` backend is unchanged.

Celln comes in two forms. Which one a cluster runs is decided by
`sympozium install`:

| Form | Installed when | What it serves |
| --- | --- | --- |
| **Fleet** (default) | `sympozium install` finds a model backend: `DEEPSEEK_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` or `SYMPOZIUM_CELLN_BACKEND` in the environment, or a provider and key entered at the terminal prompt | One-shot **and** enduring runs on every KVM node, from any authorised namespace, each Agent on the model backend it chooses |
| **One-shot router** | No backend found and no terminal to ask in | Single bounded executions through one in-cluster dispatcher (see [One-shot router](#one-shot-router-without-a-fleet)) |

`sympozium install --no-celln` installs neither. The full fleet reference
(flags, backends, toolbox, capacity, operations) is
[Celln Fleet Installation](../guides/celln-fleet-installation.md).

## Nodes

Every cell is a microVM booted from a kernel image on the node, so a Celln
node needs `/dev/kvm` and a kernel under `/boot` (with its `/lib/modules`).
The node probe (`nodeProbe.labelKVMNodes`, on by default) labels such nodes
`celln.dev/kvm=true`; fleet workloads and the host installer schedule only
there. Set
`celln.dev/kvm=false` to keep a node out (a removed label is added back).

Kind is a development environment only: on a Linux host, copy the running
kernel into each node (`docker cp /boot/vmlinuz-$(uname -r) kind-control-plane:/boot/`).
Kind on macOS or Windows has no `/dev/kvm` and cannot run Celln.

## Lifecycles

`spec.executionLifecycle` chooses how long the work lives:

- **`one-shot`** — a single-turn parent answers the task once; the run
  succeeds with the answer in `status.result` and the parent is stopped.
- **`enduring`** — a leased **parent** cell keeps the conversation's live
  context and lends each turn to a disposable **child** cell. Follow-up
  messages are `AgentRunTurn` objects (the UI's conversation panel and
  `POST /api/v1/runs/{name}/turns` create them). `spec.enduring` bounds the
  lease, turns, model requests and output tokens.

The UI offers both from an Agent's **Harness** tab: the conversation panel
for enduring work and **Answer once** for a one-shot.

### Conversations survive their node

Live context lives in one cell on one node. When that node leaves the fleet
or its owner is replaced, the run reports `ContextLost` and the controller
creates a **continuation**: a new run with the same Agent, backend, tools and
limits, seeded with the exchanges recorded so far
(`spec.conversation.continuesFrom`, `spec.conversation.seed`), placed on any
node with capacity. The lost run records it in
`status.cellnParent.continuedBy`. **Restart elsewhere** in the UI, or
`POST /api/v1/runs/{name}/continue`, does the same on request. Set
`spec.conversation.continuation: none` to opt out. The seed is bounded by the
starter package (about 15 KiB, at most 16 recent committed exchanges, on a
current package), so this restores the conversation, not the VM.

## Model backends

A run's model never comes from a key in the run. On the fleet, each backend
(DeepSeek, OpenAI, Anthropic, llama-server or any OpenAI-/Anthropic-compatible
endpoint) is configured once per scope; its key lives in the
`celln-fleet-model-credentials` Secret in `celln-system`, mounted read-only
into the node dispatchers and never into guests, the controller or tenant
namespaces. Each backend appears in every authorised namespace as a wrapper
`AgentRuntime` and Agent (`celln-<name>` / `celln-agent-<name>`) and a
host-profile `ModelConnection`; an Agent picks its backend from the
**Model backend** picker or by selecting that wrapper. Backends can be added
to a running fleet with the installer, `POST /api/v1/celln-platform/backends`
or the UI, without restarting any conversation, and each may carry model
parameters and its own output-token cap per request.

Every Agent uses **its own** provider key: with
[mediated model access](../guides/celln-mediated-model-access.md)
(on by default since `sympozium install` mediates; opt out with
`--no-celln-mediation`) a `ModelConnection` with a
`secretRef` in the Agent's namespace sends model requests through the model
gateway, which adds the key, so the nodes never hold it. Fleet backends and
mediated connections run side by side. Which providers an Agent may bring a
key for are the operator's routes: provider, protocol and endpoint origin match
exactly (never a wildcard origin), and models match exactly unless a route
declares `["*"]` (any model of that provider). With no route declared, the
built-in routes admit any model of OpenAI, Anthropic and DeepSeek at their
public API origins (`celln.mediation.defaultRoutes`).

## Selecting Celln in YAML

An enduring run in an authorised namespace, using the default backend's
wrappers and the shared tool catalogue:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  generateName: notes-
  namespace: team-a
spec:
  agentRef: celln-agent
  backend: celln
  executionLifecycle: enduring
  model:
    connectionRef: celln-native
    model: deepseek-chat
  cellnSelection:
    runtimeRef: celln-native
    toolRefs: []
    clusterToolRefs:
      - name: celln-starter-workspace-write
        revision: "<revision from kubectl get clustercellntool>"
  enduring:
    leaseSeconds: 14400
    maxTurns: 64
    maxModelRequests: 384
    maxOutputTokens: 196608
  task: "Write violet to notes.txt, then reply done."
```

The controller resolves the namespace's `CellnExecutionPolicy`, the backend's
`CellnRuntimeProfile`, the tools and the model route into one immutable
decision before issuing the parent, and re-checks it before every later turn.
The installer leaves a ready-made `run.json` in its output directory, and the
API fills in the profile's persona when `systemPrompt` is omitted.

## What runs inside a cell

Cells borrow only the tools the policy lends: eight brokered Celln tools
(`workspace-read/-write/-list/-append/-search/-delete`, `https-fetch`,
`https-post-json` to the scope's approved hosts) and commands borrowed from
digest-pinned images (busybox, jq, …), listed as `ClusterCellnTool` objects.
See [the toolbox](../guides/celln-fleet-installation.md#the-toolbox).

Celln deliberately does **not** support SkillPacks, Agent MCP servers,
ensembles, delegation, shared memory, NATS/IPC or sub-agent spawns. The API,
the run form and the admission webhook reject those combinations rather than
silently dropping them; use the `job` backend (optionally with
[Agent Sandboxing](agent-sandbox.md)) for that work. `task.mode: harness`
with `backend: celln` is also rejected — see [Harness Mode](../modes/harness.md).

## Trust model

Task text never grants tool authority. A model-driven run gets only the
tool revisions its policy lends, through host brokers with quotas; every
write-like operation is an approved effect.

For an existing immutable program, `spec.celln` names it explicitly:
`mote: {hash}`, `tools: [{alias, hash}]`, optional bounded `inputs`,
`invocation: {alias, args}`, `lane` and `capabilities` (workspace, egress,
memoryBytes, outputBytes). Task text is then not sent to a model and does
not influence the executable or its arguments; the dispatcher independently
verifies every artifact. The controller freezes the request in
`status.cellnRequest` before the first submission and retains the validated
receipt in `status.cellnReceipt`. `status.result` is lossy display text;
use the receipt's output reference as the artifact identity.

Deleting a dispatched run requests authenticated remote cancellation. The
controller keeps its finalizer until Celln confirms teardown; acknowledgement
alone is not proof.

## One-shot router without a fleet

With no backend configured, the install deploys an unprivileged
`celln-router` Deployment and an in-cluster `celln-dispatcher` (a privileged
pod with no node selector; it mounts the node's `/dev/kvm`, so the node it
lands on must provide KVM). This path runs single bounded
executions only; the in-cluster dispatcher is given no model credential, so
use it for explicit `spec.celln` programs, or add a backend (rerun
`sympozium install` with a key, or use the UI) to get model-driven runs.

The controller authenticates to the router with the `celln.tokenSecret`
credential (generated by the installer). The chart's `celln-router-ingress`
NetworkPolicy admits only controller pods from the control-plane namespace;
it needs an enforcing CNI and is not authentication. The capability probe
behind the Runs page's Celln banner is `GET /api/v1/capabilities`.

A bare-metal alternative, a host systemd dispatcher installed by the
privileged `celln-installer` DaemonSet, remains available with
`sympozium install --celln-host-installer --celln-backend …`. Only that path
uses `celln.anthropicApiKey` / `openaiApiKey` / `deepseekApiKey` (written to
`/etc/celln/agent-key` on the host; Anthropic and OpenAI also need the
`claude` / `codex` CLI there), and its code-generating `forge` step needs a
musl linker on the host (`apt-get install -y musl-tools`).

## Disabling

```bash
sympozium install --no-celln
# or, with Helm
helm upgrade sympozium sympozium/sympozium --reuse-values --set celln.enabled=false
```

Disabling does not remove `celln` as a valid `backend` value; such runs are
schema-valid but refused at dispatch. There is never a fallback to a Job.

## See Also

- [Celln Fleet Installation](../guides/celln-fleet-installation.md) — the full operator reference.
- [Native Celln Installation](../guides/celln-native-installation.md) — the single-node native path.
- [Skills, tools and execution](skills-tools-and-execution.md) — how skills, tools, backends and lifecycles relate.
- [Model connections](../guides/model-connections.md) — reusable model routes.
- [Mediated model access](../guides/celln-mediated-model-access.md) — an Agent's own key through the model gateway.
- [Celln repository](https://github.com/sympozium-ai/celln) — the runtime itself.
