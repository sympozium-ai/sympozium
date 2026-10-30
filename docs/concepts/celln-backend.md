# Celln Backend (Hardware-Isolated Execution)

[Celln](https://github.com/sympozium-ai/celln) runs agent work in KVM
microVMs ("cells") instead of Kubernetes pods. An `AgentRun` selects it with
`spec.backend: celln` (or inherits it from the Agent's
`spec.execution.backend`). The default `job` backend is unchanged.

Celln is an opt-in execution backend: `sympozium install --celln-fleet`
deploys the per-node **fleet** on every node labeled `celln.dev/kvm=true`,
the router and the model gateway, and asks nothing about models. A plain
`sympozium install` deploys none of it, and Agents run on the Job backend. The fleet is privileged (two DaemonSets mount
`/dev/kvm`, `/boot`, `/lib/modules` and a state directory, because booting
micro-VMs needs them) and the install says so before it proceeds. The full
operator reference is [Celln Fleet Installation](../guides/celln-fleet-installation.md).

## Nodes

Every cell is a microVM booted from a kernel image on the node, so a Celln
node needs `/dev/kvm` and a kernel under `/boot` (with its `/lib/modules`).
The node probe (`nodeProbe.labelKVMNodes`, on by default) labels such nodes
`celln.dev/kvm=true`; the fleet schedules only there. Set
`celln.dev/kvm=false` to keep a node out (a label an operator set is never
changed). A cluster with no such node installs the same thing and simply has
no Celln capacity.

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
for enduring work and **Answer once** for a one-shot. Live context lives in
one cell on one node; if that node leaves the fleet the run ends with
`AUTH_CONTEXT_LOST` and you start a new one.

## Model access is bound to the Agent

Celln holds no model. Each Agent has a `ModelConnection` in its namespace
(any OpenAI-compatible or Anthropic-compatible endpoint, with its key in a
Secret), and that configuration is the binding between the key and the
running agent:

- the Secret is owned by exactly one Agent (`sympozium.ai/model-key-owner`)
  and pinned to the one endpoint origin it was created for
  (`sympozium.ai/model-key-origin`); another Agent, or a connection moved to
  another origin, is refused;
- the **model gateway** adds the key to each request; no node, guest, run
  spec or controller sees it, and the gateway re-checks connection, Secret,
  Agent, owner and pin on every call;
- an operator may narrow where keys may go with `allowedOrigins` and
  `deniedOrigins` on the scope's `CellnExecutionPolicy`, and approves a
  private (LAN) endpoint once with `modelGateway.privateOrigins`.

See [Agent-bound model access](../guides/celln-mediated-model-access.md) for
the connection, key and API flow.

## Selecting Celln in YAML

An enduring run in an authorised namespace, on an Agent whose connection
is `cloud`, using the chat-only wrapper runtime (`celln-native`; an Agent
that lends the starter toolbox uses `celln-native.toolbox` and selects
exactly the toolbox's tools, which `GET /api/v1/celln-platform/profiles`
lists as `toolboxTools`):

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  generateName: notes-
  namespace: team-a
spec:
  agentRef: researcher
  backend: celln
  executionLifecycle: enduring
  model:
    connectionRef: cloud
    model: qwen/qwen3-235b-a22b
  cellnSelection:
    runtimeRef: celln-native
    toolRefs: []
  enduring:
    leaseSeconds: 14400
    maxTurns: 64
    maxModelRequests: 384
    maxOutputTokens: 196608
  task: "Summarise the three most common Kubernetes failure modes, then reply done."
```

The controller resolves the namespace's `CellnExecutionPolicy`, the scope's
`CellnRuntimeProfile`, the tools and the Agent's connection into one
immutable, signed decision before issuing the parent, and re-checks it
before every later turn. The API fills in the profile's persona when
`systemPrompt` is omitted.

## What runs inside a cell

Cells borrow only the tools the policy lends: eight brokered Celln tools
(`workspace-read/-write/-list/-append/-search/-delete`, `https-fetch`,
`https-post-json` to public HTTPS hosts or the scope's approved list) and
commands borrowed from digest-pinned images (busybox, jq, …), listed as
`ClusterCellnTool` objects. See
[the toolbox](../guides/celln-fleet-installation.md#the-toolbox).

Celln deliberately does **not** support SkillPacks, Agent MCP servers,
ensembles, delegation, shared memory, NATS/IPC or sub-agent spawns. The API,
the run form and the admission webhook reject those combinations rather than
silently dropping them; use the `job` backend (optionally with
[Agent Sandboxing](agent-sandbox.md)) for that work. `task.mode: harness`
with `backend: celln` is also rejected — see [Harness Mode](../modes/harness.md).
Celln runs only its native harness; Pi and Hermes stay on the Kubernetes
plane and use the same `ModelConnection` and key model.

## Trust model

Task text never grants tool authority. A model-driven run gets only the
tool revisions its policy lends, through host brokers with quotas; every
write-like operation is an approved effect.

Deleting a dispatched run requests authenticated remote cancellation. The
controller keeps its finalizer until Celln confirms teardown; acknowledgement
alone is not proof.

## Disabling

A fresh `sympozium install` deploys no Celln. To remove an installed fleet:

```bash
sympozium install --no-celln
# or, with Helm
helm upgrade sympozium sympozium/sympozium --reuse-values --set celln.enabled=false
```

Disabling does not remove `celln` as a valid `backend` value; such runs are
schema-valid but refused at dispatch. There is never a fallback to a Job.

## See Also

- [Celln Fleet Installation](../guides/celln-fleet-installation.md) — install, verify, upgrade from 0.11, operate.
- [Agent-bound model access](../guides/celln-mediated-model-access.md) — connections, keys, origin pins, the gateway.
- [Skills, tools and execution](skills-tools-and-execution.md) — how skills, tools, backends and lifecycles relate.
- [Model connections](../guides/model-connections.md) — the `ModelConnection` resource on both planes.
- [Celln repository](https://github.com/sympozium-ai/celln) — the runtime itself.
