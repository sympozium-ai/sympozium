# Custom Resources

Sympozium models every agentic concept as a Kubernetes Custom Resource:

| CRD | Kubernetes Analogy | Purpose |
|-----|--------------------|---------|
| `SympoziumInstance` | Namespace / Tenant | Per-user gateway — channels, provider config, memory settings, skill bindings |
| `AgentRun` | Job / [Sandbox CR](agent-sandbox.md) | Single agent execution — task, model, result capture, memory extraction. Optionally uses Agent Sandbox CRDs for kernel-level isolation |
| `SympoziumPolicy` | NetworkPolicy | Feature and tool gating — what an agent can and cannot do |
| `SkillPack` | ConfigMap | Portable skill bundles — kubectl, Helm, or custom tools — mounted into agent pods as files, with optional sidecar containers for cluster ops |
| `SympoziumSchedule` | CronJob | Recurring tasks — heartbeats, sweeps, scheduled runs with cron expressions |
| `Ensemble` | Helm Chart / Operator Bundle | Pre-configured agent bundles — activating a pack stamps out instances, schedules, and memory for each persona |
| `Model` | Deployment + Service | [Cluster-local inference](../guides/local-models.md) — declares a GGUF model, controller deploys llama-server and exposes an OpenAI-compatible endpoint |

---

## SympoziumInstance

The core resource representing an agent identity. Each instance has:

- An LLM provider configuration (model, API key reference, base URL)
- Skill bindings (which SkillPacks are active)
- Channel connections (Telegram, Slack, etc.)
- Memory settings (enabled/disabled, max size)
- A policy reference
- Optional node selector for pinning agent pods to specific nodes (e.g. GPU nodes running Ollama)

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
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

---

## AgentRun

Represents a single agent execution. The controller reconciles each AgentRun into an ephemeral Kubernetes Job containing the agent container, IPC bridge, and any skill sidecars.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: quick-check
spec:
  instanceRef: my-agent
  task: "How many nodes are in the cluster?"
  model:
    name: gpt-4o
    provider: openai
  skills:
    - k8s-ops
  timeout: "5m"
```

Phase transitions: `Pending` → `Running` → `Succeeded` (or `Failed`). When [lifecycle hooks](lifecycle-hooks.md) with `postRun` are defined: `Pending` → `Running` → `PostRunning` → `Succeeded` (or `Failed`).

---

## SympoziumPolicy

Gates features and tools at admission time. The webhook evaluates policies before a pod is created.

| Policy | Who it is for | Key rules |
|--------|---------------|-----------|
| **Permissive** | Dev clusters, demos | All tools allowed, no approval needed |
| **Default** | General use | `execute_command` requires approval, everything else allowed |
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
