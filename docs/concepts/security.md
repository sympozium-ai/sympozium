# Security

Sympozium enforces defence-in-depth at every layer — from network isolation to per-run RBAC.

## Security Layers

| Layer | Mechanism | Scope |
|-------|-----------|-------|
| **Network** | `NetworkPolicy` deny-all egress on agent pods | Only the IPC bridge can reach NATS; agents cannot reach the internet or other pods |
| **Pod sandbox** | `SecurityContext` — `runAsNonRoot`, UID 1000, read-only root filesystem | Every agent and sidecar container runs with least privilege |
| **Kernel isolation** | [Agent Sandbox CRD](agent-sandbox.md) (optional) — gVisor/Kata via `kubernetes-sigs/agent-sandbox` | When enabled, agent pods run inside a user-space kernel (gVisor) or lightweight VM (Kata), isolating them from the host kernel |
| **Admission control** | `SympoziumPolicy` admission webhook | Feature gates, image and resource limits checked before the run is admitted |
| **Tool gating** | `SympoziumPolicy` `toolGating`, applied by the controller | The policy's allow/deny rules are merged into every Job and agent-sandbox run's tool policy; the agent runner and skill tool server only expose the tools that survive |
| **Skill RBAC** | Ephemeral `Role`/`ClusterRole` per AgentRun | Each skill declares exactly the API permissions it needs — the controller auto-provisions them at run start and revokes them on completion |
| **RBAC lifecycle** | `ownerReference` (namespace) + label-based cleanup (cluster) | Namespace RBAC is garbage-collected by Kubernetes. Cluster RBAC is cleaned up by the controller on AgentRun completion and deletion |
| **Controller privilege** | Dedicated `sympozium-manager` ClusterRole | The controller has RBAC delegation permissions to provision skill roles. The API server has a **separate, scoped** `sympozium-apiserver` ClusterRole with no RBAC delegation or `pods/exec` access |
| **Auth secret isolation** | Individual `secretKeyRef` per provider key | Auth secrets are mounted as individual env vars (e.g. `OPENAI_API_KEY`) rather than wholesale `envFrom`, preventing leakage of unrelated secret keys |
| **Image allowlist** | `ImagePolicy.allowedRegistries` in `SympoziumPolicy` | Lifecycle hook, sandbox, skill sidecar and harness images can be restricted to approved registries. Matched by **string prefix**, so end each entry at a `/`, a full tag or a digest — `ghcr.io/acme` also admits `ghcr.io/acmecorp-evil/…`. No `policyRef`, no `imagePolicy`, or an empty list all mean no restriction |
| **Lifecycle RBAC bounds** | `LifecyclePolicy.deniedResources` in `SympoziumPolicy` | Prevents lifecycle hooks from requesting RBAC access to sensitive resources (e.g. `secrets`, `clusterroles`) |
| **Env var denylist** | Admission webhook validation | Blocks `spec.env` overrides of dangerous variables (`PATH`, `LD_PRELOAD`, `HOME`, etc.) |
| **Model integrity** | SHA256 checksum verification | Model downloads can specify a `sha256` hash; the download job verifies integrity before loading |
| **Multi-tenancy** | Namespaced CRDs + Kubernetes RBAC | Agents, runs, and policies are namespace-scoped; standard K8s RBAC controls who can create them |

## Ephemeral Skill RBAC

The skill sidecar RBAC model deserves special attention: permissions are **created on-demand** when an AgentRun starts, scoped to exactly the APIs the skill needs, and **deleted when the run finishes**. There is no standing god-role — each run gets its own short-lived credentials.

This is the Kubernetes-native equivalent of temporary IAM session credentials.

```
AgentRun starts
  → Controller reads SkillPack RBAC declarations
  → Creates Role + RoleBinding (namespace-scoped, ownerRef → AgentRun)
  → Creates ClusterRole + ClusterRoleBinding (label-based)
  → Agent pod uses these credentials during execution

AgentRun completes/deleted
  → Namespace RBAC: garbage-collected via ownerReference
  → Cluster RBAC: cleaned up by controller via label selector
```

## Policies

`SympoziumPolicy` resources gate what tools and features an agent can use.
Feature gates and limits are checked by the admission webhook when a run is
created. Tool gating is applied by the controller when it builds the run's pod:
the policy's `deny` rules are added to the run's own `spec.toolPolicy`, and with
`defaultAction: deny` only the tools a rule allows remain. The agent never sees
a filtered-out tool. A run that explicitly allows a tool the policy denies is
rejected at admission.

Tool rules are `allow` or `deny`; there is no per-call human approval. To hold
an agent's output until a person approves or rejects it, use a
[response gate](lifecycle-hooks.md#manual-human-in-the-loop-approval).
Celln runs are not covered by `toolGating`: their tools come from the reviewed
Celln catalogue and its grants.

| Policy | Who it is for | Key rules |
|--------|---------------|-----------|
| **Permissive** | Dev clusters, demos | All tools allowed, generous resource limits |
| **Network-isolated** | Agents that must not reach the network | All tools allowed except `fetch_url`; deny-all network policy |
| **Restrictive** | Production, security | All tools denied by default; only `read_file` and `list_directory` allowed; sandbox required |

### Policy Fields

| Field | Purpose |
|-------|---------|
| `sandboxPolicy` | Sandbox enforcement, resource limits, seccomp profiles |
| `subagentPolicy` | Max nesting depth and concurrency for sub-agents |
| `toolGating` | `defaultAction` and per-tool `allow`/`deny` rules |
| `featureGates` | Toggle code-execution, sub-agents, browser-automation, file-access |
| `networkPolicy` | Deny-all, DNS, event bus egress rules |
| `modelPolicy` | Restrict which model names/namespaces can be used |
| `imagePolicy` | Registry allowlist for lifecycle hooks, sandbox, and skill sidecar images |
| `lifecyclePolicy` | Denied resources list to bound lifecycle hook RBAC requests |
