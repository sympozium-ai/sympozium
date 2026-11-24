# Sympozium vs kagent

Both Sympozium and [kagent](https://kagent.dev/) are Kubernetes-native platforms for running AI agents. They share a CRD-driven, declarative model and support multiple LLM providers, and both have chosen Google's [Agent Substrate](https://github.com/agent-substrate/substrate) as the runtime underneath. The differences lie in **what each adds above that runtime: how keys and tools are isolated, and how far the platform extends beyond a single agent run**.

!!! info "Last reviewed 2026-09-23"
    kagent is mid-transition: its v1.0.0 alphas (September 2026) are rebuilding
    its runtime on Google's [Agent Substrate](https://github.com/agent-substrate/substrate),
    announced in [The future of kagent](https://kagent.dev/blog/the-future-of-kagent).
    Rows marked *(1.0 alpha)* describe that new runtime. See also
    [Sympozium vs Google AX and Agent Substrate](sympozium-vs-google-ax.md).
    Sympozium rows updated 2026-11-24 for the decision to make Substrate its
    default execution plane.

<!-- STATUS:substrate -->
!!! warning "State of the Substrate backend, 2026-11-24"
    Agent Substrate is the default execution plane by decision of
    2026-11-24. The implementation is on a branch and not released. Where
    Substrate is installed and enabled (chart value `substrate.enabled`), a
    run that names no backend runs as an actor when it can: a one-shot
    string task with a model key, with channel messages and schedules served
    outside the sandbox under the run's tool policy. A run that needs
    skills, MCP servers, memory, delegation or an enduring conversation runs
    as a Kubernetes Job instead, and its `DefaultExecutionPlane` condition
    says why. A plain `sympozium install` does not install Substrate, so
    there every run is a Job. One in-cluster hop, from Substrate's egress
    gateway to the model gateway, is unencrypted. See [Substrate
    backend](concepts/substrate-backend.md).

---

## At a Glance

| Dimension | Sympozium | kagent |
|-----------|-----------|--------|
| **Agent runtime** | A pluggable execution plane: an Agent Substrate actor per run by default, or a Celln microVM cell, or an ephemeral Pod (Kubernetes Job) | Long-running engine process (Python or Go ADK); *(1.0 alpha)* Substrate actors that suspend and resume |
| **Tool isolation** | Dedicated sidecar container per skill, ephemeral RBAC (Job backend) | In-process (MCP client inside the engine) |
| **Kernel-level sandboxing** | gVisor via Agent Substrate; KVM microVMs via Celln; gVisor / Kata via `kubernetes-sigs/agent-sandbox`, warm pools | *(1.0 alpha)* gVisor via Agent Substrate |
| **Multi-tenancy** | Namespace-per-tenant, per-instance RBAC, admission webhooks | Namespace-scoped CRDs |
| **Agent packaging** | Ensembles (bundle personas, skills, schedules, memory seeds) | Individual Agent CRDs |
| **Persistent memory** | SQLite + FTS5 on PVC, survives across runs | Vector-backed memory (in-engine) |
| **Channels** | Telegram, Slack, Discord, WhatsApp as dedicated Deployments via NATS JetStream | Slack, Discord (in-engine integration) |
| **Scheduled runs** | SympoziumSchedule CRD with CronJob-style concurrency policies | Not available |
| **MCP support** | MCPServer CRD with auto-discovery, tool filtering, managed or external servers | MCP tools as CRDs, remote MCP server references |
| **Observability** | Built-in TUI + Web UI, resource views, live logs | Dashboard UI, OpenTelemetry tracing |
| **Human-in-the-loop** | Response gates: a run's output is held for approve/reject in the UI; tool calls are allow/deny by policy, not approved one by one | Tool-level approve/reject in UI |

---

## Design Philosophy

### Agent Execution Model

This used to be the most fundamental difference between the two projects. It is now the place they are converging.

**Sympozium** treats execution as a separate layer. An `AgentRun` is a Kubernetes object that the controller admits and then hands to an execution backend. By default that backend is **Agent Substrate**: the run is an actor, a sandbox restored from a memory snapshot. Two alternatives remain: a **Celln** microVM, which is self-hosted and depends on nothing outside the cluster, and an **ephemeral Kubernetes Job**, which needs nothing installed. Whichever is used:

- Each run gets its own sandbox. There is no long-lived process shared between runs.
- What the run may do (which key, which tools, which policy) is decided outside the sandbox, before it starts.
- Blast radius of a misbehaving agent is contained to one run.

**kagent** has run agents inside a **persistent engine process** (Python ADK or Go ADK) that handles the conversation loop. The controller provisions CRDs, but the agent runtime is a long-running service, not an ephemeral workload. This means:

- Faster turn-around for conversational interactions (no pod startup per message).
- Agent state lives in the engine process memory.
- Multiple agent runs may share the same engine process.

kagent's 1.0 alphas move agent sessions onto Agent Substrate actors: sandboxed with gVisor, suspended to storage while idle, and resumed in well under a second.

!!! note "Trade-off"
    With both projects on the same runtime, start latency and idle-agent density stop being the difference. What remains is the layer above. kagent's engine holds the conversation and its tools in one process. Sympozium keeps the Agent's key, its policy and its coordination outside the sandbox, and expresses them as Kubernetes resources.

### Tool Isolation

On the Job backend, **Sympozium** injects every skill as a **dedicated sidecar container** in the agent pod. Each AgentRun gets a unique ServiceAccount; least-privilege RBAC and a short-lived projected token are exposed only to sidecars that declare Kubernetes access. Skills communicate with the agent container over shared workspace/IPC surfaces. A kubectl skill can have cluster access; the agent or external harness container does not receive its Kubernetes credential.

**kagent** loads tools **in-process** within the engine via MCP clients or built-in function calls. Tool execution shares the same process, memory space, and Kubernetes credentials as the agent itself.

!!! warning "Why this matters"
    If an agent can convince its LLM to call a tool with malicious arguments, in-process tools execute with the full privileges of the engine's ServiceAccount. Sidecar isolation means the agent container never holds the credentials — only the skill sidecar does, scoped to exactly the permissions that skill needs.

### Sandboxing

**Sympozium** takes its isolation boundary from the execution plane: gVisor on Agent Substrate, a KVM microVM on Celln. On the Job backend it integrates with the [`kubernetes-sigs/agent-sandbox`](https://deploy.sympozium.ai/docs/concepts/agent-sandbox/) project to offer kernel-level isolation via **gVisor** (user-space kernel) or **Kata Containers** (lightweight VM). `SandboxWarmPool` CRDs pre-provision ready environments so agents start instantly despite the stronger isolation boundary.

**kagent** had no kernel-level sandboxing beyond standard Kubernetes pod security until its 1.0 alphas, which run agents as gVisor-sandboxed Agent Substrate actors. That integration is still alpha.

### Multi-Tenancy

**Sympozium** is designed around an `Agent` CRD that represents a single tenant. Each instance gets its own channel connections, agent configurations, policy bindings, and memory store. Admission webhooks enforce policy boundaries. Multiple teams share a cluster without seeing each other's agents or data.

**kagent** uses Kubernetes namespace isolation and supports cross-namespace tool references, but does not have a first-class tenant abstraction or admission-webhook-based policy enforcement.

### Agent Lifecycle

**Sympozium** provides **Ensembles** — declarative bundles that stamp out an entire team of agents, their skills, schedules, and memory seeds in one apply. `SympoziumSchedule` CRDs drive cron-based recurring runs (health checks, alert triage, resource right-sizing) with CronJob-style concurrency policies (Forbid, Allow, Replace). Cleanup is automatic via ownerReferences.

**kagent** defines agents individually as CRDs. Scheduling and bundling are handled outside the platform.

### Channels & Messaging

**Sympozium** deploys each channel (Telegram, Slack, Discord, WhatsApp) as a **dedicated Kubernetes Deployment**. Messages flow through **NATS JetStream** with durable pub/sub — the channel pod and the agent pod are fully decoupled. This survives restarts, scales independently, and supports group chat.

**kagent** integrates Slack and Discord through in-engine handlers. Channel state lives in the engine process.

---

## When to Choose What

**Choose Sympozium when you need:**

- Agents that perform cluster-admin operations (kubectl, Helm, scaling) and you want strong isolation guarantees.
- Multi-tenant environments where teams share a cluster.
- Scheduled, unattended agent runs (overnight health sweeps, alert triage).
- Messaging integrations beyond Slack/Discord (Telegram, WhatsApp).
- Hardware (KVM microVM) isolation for untrusted or third-party agent code, available today through Celln.
- A choice of execution plane: Substrate, a self-hosted alternative with no external dependency, or plain Jobs.
- Each Agent's model key kept out of the sandbox it runs in.

**Choose kagent when you need:**

- Low-latency conversational agents with a persistent engine.
- Google ADK, CrewAI, or LangGraph integration out of the box.
- A lighter-weight setup for single-tenant experimentation.
- A2A (Agent-to-Agent) protocol support.
- Human approval of individual tool calls (approve/reject in the UI).
- Long-lived conversational sessions suspended and resumed on Agent Substrate, if you can run its 1.0 alpha.

---

## Summary

kagent and Sympozium solve similar problems from different angles. kagent is a solid choice for teams that want a conversational agent framework with Google ADK integration. Both projects are moving onto Agent Substrate for execution, so the choice is about the layer above it. Sympozium focuses on multi-tenant clusters that need **per-Agent identity and keys, policy enforced outside the sandbox, scheduled automation, and coordinated teams of agents**. Both projects are pre-1.0.
