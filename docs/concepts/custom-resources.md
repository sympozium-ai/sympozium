# Custom Resources

Sympozium models every agentic concept as a Kubernetes Custom Resource. All
are in the `sympozium.ai/v1alpha1` API group.

### Core

| CRD | Kubernetes Analogy | Purpose |
|-----|--------------------|---------|
| `Agent` | Namespace / Tenant | Per-user gateway — channels, provider config, memory settings, skill bindings |
| `AgentRun` | Job / [Sandbox CR](agent-sandbox.md) | Single agent execution — task, model, result capture, memory extraction. Optionally uses Agent Sandbox CRDs for kernel-level isolation |
| `SympoziumPolicy` | NetworkPolicy | Feature and tool gating — what an agent can and cannot do |
| `SkillPack` | ConfigMap | Portable skill bundles — kubectl, Helm, or custom tools — mounted into agent pods as files, with optional sidecar containers for cluster ops |
| `SympoziumSchedule` | CronJob | Recurring tasks — heartbeats, sweeps, scheduled runs with cron expressions |
| `Ensemble` | Helm Chart / Operator Bundle | Pre-configured agent bundles — activating a pack stamps out Agents, Schedules, and memory for each persona |
| `Model` | Deployment + Service | [Cluster-local inference](../guides/local-models.md) — declares a model (GGUF or HuggingFace), controller deploys an inference server (llama.cpp, vLLM, or TGI) and exposes an OpenAI-compatible endpoint |
| `MCPServer` | Deployment + Service | Managed [Model Context Protocol](../mcp-servers.md) server — external tool providers with auto-discovery and allow/deny filtering |
| `SympoziumConfig` | Cluster configuration | Platform-wide singleton — gateway, canary, and pricing settings |
| `ModelConnection` | ExternalName Service | Namespaced, reusable model route — provider, protocol, endpoint, models, and an optional Secret or host credential profile |

### Execution and sessions

| CRD | Kubernetes Analogy | Purpose |
|-----|--------------------|---------|
| `AgentRuntime` | RuntimeClass | Administrator-approved, digest-pinned harness that replaces `agent-runner` (one-shot or session-capable); on Celln, a wrapper referencing a `CellnRuntimeProfile`. Agents select one with `spec.runtimeRef` |
| `HarnessSession` | Deployment + Service | A persistent, Agent-owned AgentHarness process (Pi, Hermes) behind **Agents → Chat**, with state on a per-session PVC |
| `AgentRunTurn` (`arturn`) | — | One follow-up message and its result within an enduring Celln run; a durable record, not another running agent |
| `WorkspaceSession` (`ws`) | PersistentVolumeClaim | A persistent `/workspace` for one (Agent, `sessionKey`) pair; owns its PVC and is owned by the Agent |

### Celln catalogue and policy

| CRD | Scope | Purpose |
|-----|-------|---------|
| `CellnRuntimeProfile` | Cluster | Immutable, operator-published runtime revision: executable hashes, lifecycles, ceilings and the native parent/worker material for the fleet |
| `CellnExecutionPolicy` | Cluster | Which namespaces may run which profiles, tools and model routes, with lease/turn/request/token ceilings |
| `ClusterCellnTool` | Cluster | Shared, immutable tool revision lent to cells (brokered tools and commands borrowed from pinned images; `spec.sourceImage`) |
| `CellnTool` | Namespaced | Legacy namespaced tool catalogue metadata, published by `sympozium celln-tool approve` |
| `CellnToolSubmission` | Namespaced | Untrusted request to publish a tool; creating it approves nothing |

The [Celln fleet installer](../guides/celln-fleet-installation.md) creates
the cluster-scoped objects once per scope and per-namespace wrappers
(`AgentRuntime`, `Agent`, `ModelConnection`) on first use.

---

## Agent

The core resource representing an agent identity. Each instance has:

- An LLM provider configuration (model, API key reference, base URL)
- Skill bindings (which SkillPacks are active)
- Channel connections (Telegram, Slack, etc.)
- Memory settings (enabled/disabled, max size)
- A policy reference
- Optional node selector and tolerations for placing agent pods on specific nodes (e.g. GPU nodes running Ollama)
- An optional harness runtime (`spec.runtimeRef`) and execution defaults (`spec.execution`: backend, lifecycle, model connection, Celln tool selection)

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-agent
spec:
  agents:
    default:
      model: gpt-4o
  skills:
    - skillPackRef: k8s-ops
    - skillPackRef: code-review
  policyRef: default-policy
```

### Placing agent pods

`spec.agents.default.nodeSelector` and `spec.agents.default.tolerations` pin
an Agent's pods to a node pool, for example a GPU pool tainted
`nvidia.com/gpu=present:NoSchedule` that runs a local model. The controller
copies the tolerations onto every AgentRun it creates for the Agent
(schedules, channels, delegations, the API), and a run may also set
`spec.tolerations` itself. They apply to Job, Deployment (serving) and
Sandbox pods.

```yaml
spec:
  agents:
    default:
      model: qwen3
      nodeSelector:
        pool: gpu
      tolerations:
        - key: nvidia.com/gpu
          operator: Equal
          value: present
          effect: NoSchedule
```

---

## AgentRun

Represents a single agent execution. The controller reconciles each AgentRun into an ephemeral Kubernetes Job containing the agent container, IPC bridge, and any skill sidecars.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: quick-check
spec:
  agentRef: my-agent
  agentId: default
  sessionKey: "quick-check-001"
  task: "How many nodes are in the cluster?"
  model:
    provider: openai
    model: gpt-4o
    authSecretRef: my-openai-key
  skills:
    - skillPackRef: k8s-ops
  timeout: "5m"
```

Phase transitions: `Pending` → `Running` → `Succeeded` (or `Failed`). When [lifecycle hooks](lifecycle-hooks.md) with `postRun` are defined: `Pending` → `Running` → `PostRunning` → `Succeeded` (or `Failed`).

A [response gate](lifecycle-hooks.md#retrying-a-rejected-response) can retry a rejected run. Each attempt is its own AgentRun, linked by `status.retryOf` and `status.attempt`; a superseded attempt ends `Failed` with `status.gateVerdict: retried`. Use `kubectl get agentruns -o wide` to see the chain.

Setting `spec.backend: celln` routes the run to a hardware-isolated Celln microVM instead of a Job, and `spec.executionLifecycle` chooses `one-shot` or `enduring` (a leased parent cell that takes follow-up `AgentRunTurn`s). Celln runs use the model route of the Agent's Celln backend (`spec.model.connectionRef`), not a key in the run, and do not support SkillPacks, MCP, ensembles, delegation or shared memory. See [Celln Backend](celln-backend.md).

Other fields worth knowing: `spec.task.mode: harness` or an `AgentRuntime` replaces `agent-runner` with an approved harness ([Harness Mode](../modes/harness.md)); `spec.mode: server` runs a long-lived Deployment ([Serving Mode](../guides/serving-mode.md)); `spec.tolerations` places the pod on tainted nodes; `spec.cleanup` is `delete` or `keep`.

---

## SympoziumPolicy

Gates features and tools. The webhook checks feature gates and limits when a run is admitted; the controller applies the tool rules to every run's pod. See [Security](security.md#policies).

| Policy | Who it is for | Key rules |
|--------|---------------|-----------|
| **Permissive** | Dev clusters, demos | All tools allowed |
| **Default** | General use | All tools allowed except `fetch_url` |
| **Restrictive** | Production, security | All tools denied by default, must be explicitly allowed |

---

## SkillPack

Portable skill bundles mounted into agent pods as files. Can optionally declare sidecar containers with runtime tools and RBAC rules. See [Skills & Sidecars](skills.md) for details.

---

## SympoziumSchedule

Cron-based recurring agent runs. See [Scheduled Tasks](scheduled-tasks.md) for details.

---

## Ensemble

Pre-configured agent bundles. See [Ensembles](ensembles.md) for details.

---

## ModelConnection

A namespaced, reusable model route. An Agent selects one with
`spec.execution.modelConnectionRef`; native Celln runs select one with
`spec.model.connectionRef`. The controller pins the connection UID, spec, and
revision before admitting a run or persistent session, so a conversation is
never silently redirected to another provider. See
[Model connections for persistent harnesses](../guides/model-connections.md).

---

## AgentRuntime and HarnessSession

An `AgentRuntime` is the admin-owned description of an external harness:
a digest-pinned image, its adapter contract and capabilities, and a support
owner. An Agent references it with `spec.runtimeRef`; runs inherit it. A
session-capable runtime gives the Agent a persistent **Chat**, served by a
`HarnessSession` (a Deployment and private Service with a per-session PVC).
See [AgentHarness](../guides/agentharness.md) and
[Harness Mode](../modes/harness.md).

---

## AgentRunTurn

Follow-up messages to an enduring Celln run. The API server creates them
(`POST /api/v1/runs/{name}/turns`) with the run's controller
`ownerReference`; a turn without one is refused. Each turn records its
message, phase and result. See [Celln Backend](celln-backend.md#lifecycles).

---

## WorkspaceSession

A persistent `/workspace` for one (Agent, `sessionKey`) pair, owning its PVC.
Enable it on an Agent with `spec.workspace.perSessionPVC: true` (optional
`size`, default `1Gi`; `storageClassName`; `idleTTL`, default `720h`);
otherwise `/workspace` is an ephemeral emptyDir. Runs with the same session
key share the workspace and are serialised, because the PVC is
ReadWriteOnce. Deleting the session or its Agent deletes the PVC, and an idle
session is reclaimed after `idleTTL`. Manage them with `sympozium workspace list|show|delete|exec`
(see the [CLI reference](../reference/cli.md#workspaces)).

---

## Celln catalogue and policy

`CellnRuntimeProfile`, `CellnExecutionPolicy` and `ClusterCellnTool` are the
cluster-scoped, immutable authority the Celln fleet admits runs against: the
controller resolves a run's namespace, runtime, tools and model route into one
decision and re-checks it before every turn. They are created by the fleet
installer; see [Celln Fleet Installation](../guides/celln-fleet-installation.md#authorising-namespaces).
