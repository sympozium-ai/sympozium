# Sympozium vs Google AX and Agent Substrate

In September 2026 Google open-sourced two related projects:
[AX](https://github.com/google/ax) (Agent Executor), a declarative orchestrator
for agent tasks, and [Agent Substrate](https://github.com/agent-substrate/substrate),
the sandboxed execution runtime underneath it. Both run on Kubernetes and both
treat agents as a new kind of workload, which is also Sympozium's starting point.
This page is an honest comparison: where they are ahead, where Sympozium is,
and where the projects are not really competing at all.

!!! info "Scope and date"
    Last reviewed **2026-09-23** against `google/ax` v0.3.0 and
    `agent-substrate/substrate` v0.1.0, from their own READMEs, design docs,
    roadmap and threat model, plus Google's
    [GKE announcement](https://cloud.google.com/blog/products/containers-kubernetes/agent-substrate-available-on-gke).
    All three projects are pre-1.0 and changing fast. If something here is out
    of date, please open an issue or PR.

---

## The short version

- **Agent Substrate is an execution runtime, and it goes deeper than anything
  in Sympozium.** It multiplexes many idle, stateful agents onto a small pool
  of sandboxed worker pods, snapshotting memory and filesystem so an agent can
  be suspended and resumed on any worker. Sympozium has nothing equivalent.
- **AX is a thin, fast task API on top of Substrate**: four resource kinds
  (`Task`, `Workspace`, `Gateway`, `Model`) and a `kubectl`-shaped CLI. It
  deliberately does not model what an agent *does* over its lifetime.
- **Sympozium is a coordination layer**: agent identity, policy, tool
  isolation, ensembles, memory, schedules, channels and a UI, expressed as
  Kubernetes CRDs. It decides what agents do. It does not try to be a
  high-density runtime.

So the projects sit at different layers of the stack. They overlap in "run an
agent in a sandbox on Kubernetes", and diverge sharply everywhere else.

---

## At a glance

| Dimension | Sympozium | AX + Agent Substrate |
|---|---|---|
| **What it is** | Coordination layer for multi-agent systems | Task orchestrator (AX) on an agent execution runtime (Substrate) |
| **API surface** | Kubernetes CRDs (`Agent`, `AgentRun`, `Ensemble`, `SympoziumPolicy`, …) | AX: its own gRPC API, resources stored in Redis. Substrate: two CRDs (`WorkerPool`, `SandboxConfig`); actors, templates and atespaces go through its gRPC API into PostgreSQL |
| **Works with kubectl / RBAC / GitOps** | Yes: every resource is a Kubernetes object | Only the Substrate CRDs. Tasks, workspaces, gateways, models and actors are not Kubernetes objects |
| **Unit of execution** | One Pod per `AgentRun` (Job), or a Celln microVM cell | A Substrate *actor*, assigned on demand to a pre-warmed worker pod |
| **Idle agents** | Consume their Pod or cell until they finish, time out or hit their lease | Suspended to object storage (memory + filesystem) and resumed on any worker; Google quotes sub-500 ms resume |
| **Isolation** | Hardened Pods; gVisor/Kata via `agent-sandbox`; KVM microVMs via Celln | gVisor or Cloud Hypervisor microVMs, per actor |
| **Egress control** | Kubernetes NetworkPolicy; brokered tools in Celln | All actor TCP forced through an mTLS egress proxy with per-actor certificates; non-HTTP(S) blocked |
| **Model credentials** | Per-key env injection from an allowlist; optional model gateway keeps keys off Celln nodes | Google describes egress proxies that inject credentials; the Substrate roadmap still lists proxy-based credential injection as upcoming work |
| **Tool isolation** | Each skill is its own sidecar with per-run least-privilege RBAC | Not modelled: tools run inside the task sandbox |
| **Tool policy** | `SympoziumPolicy` allow/deny rules merged into every pod-based run's tool list | Egress allowlists only |
| **Human approval** | Response gates hold a run's output for approve/reject in the UI; no per-tool-call approval | Not provided |
| **Multi-agent coordination** | Ensembles, delegation, shared memory, schedules | Out of scope by design: an agent composes tasks itself |
| **Memory, schedules, channels** | Built in (SQLite memory, `SympoziumSchedule`, Slack/Telegram/Discord/WhatsApp) | Not provided |
| **UI** | Web UI and TUI | CLI (`ax`, `kubectl-ate`) |
| **Model providers** | OpenAI-compatible, Anthropic, local (llama-server, Ollama, LM Studio), … | `Model` resource is provider-agnostic; workspace `goal` bootstrap uses Antigravity and needs a Gemini key |
| **Install** | `sympozium install` from a released binary | AX: `go install`, `ko` and your own registry, on top of a separately installed Substrate (PostgreSQL + object storage) |
| **Stated maturity** | `v1alpha1`, not production | AX: "major breaking changes" expected. Substrate: "not ready for production use", "little to no security hardening at this time". On GKE: non-production for everyone, production GA by allowlist |
| **Backing** | Small independent project | Google, with a GKE-optimised path; kagent (CNCF Sandbox) is moving onto Substrate in its 1.0 alphas |

---

## Where AX and Substrate are ahead

**Density and idle cost.** This is Substrate's reason to exist, and it is a
real problem. Agents burst for a moment, then wait seconds or minutes on a
model, a tool or a person. A Sympozium run keeps its Pod (or its Celln cell)
for that whole time. Substrate checkpoints the idle agent, gives the worker to
someone else, and restores the agent on any worker when a request arrives for
it. Google's figures (10× density over standard container runtimes, over 1,000
dormant agents per host, 500+ suspend/resume operations per second) are
vendor numbers without a published method. The repository does ship a load-test
harness, but we have not reproduced them. The mechanism is sound, though, and
Sympozium has no answer to it today.

**Scale of the control plane.** AX's design doc is explicit that storing
millions of short-lived tasks as CRDs would overload etcd, so it keeps tasks in
Redis and Substrate keeps actors in PostgreSQL. Sympozium stores every
`AgentRun` as a CRD in etcd and gives each one a Pod. That is the right
trade-off for the thousands of runs a team generates, and the wrong one for
millions of concurrent agents. If your workload looks like the latter,
Sympozium is not designed for it.

**Suspend, resume and state.** A suspended Substrate actor comes back with
its process memory and filesystem intact. Sympozium's persistent paths are
coarser. A `HarnessSession` that idles out keeps its PVC but loses its
processes, and a Celln parent lives for a bounded lease (at most 24 hours),
with no checkpointing.

**Egress design.** Substrate's
[egress contract](https://github.com/agent-substrate/substrate/blob/main/docs/network-egress.md)
treats everything from the actor as untrusted, routes all of its TCP through
an mTLS policy point that authenticates the specific actor, and refuses to trust
actor-supplied hostnames or SNI. Kubernetes NetworkPolicy, which Sympozium
relies on for its Pod-based runs, cannot express per-hostname rules at all.

**Debuggability.** `ax ssh` into a live sandbox (opt-in with `spec.debug`)
is a genuinely useful affordance that Sympozium lacks.

**Momentum.** Google's backing, a supported path on GKE and kagent's
move onto it mean Substrate is likely to become common infrastructure. That
counts for something when you choose what to build on.

---

## Where Sympozium is ahead

**It is Kubernetes-native end to end.** Every Sympozium resource is a CRD, so
`kubectl`, namespaces, RBAC, admission control, Argo CD / Flux and audit
logging all apply unchanged. AX's resources live in Redis behind a gRPC API,
and Substrate's actors live in PostgreSQL, scoped by *atespaces*, which are not
Kubernetes namespaces. That is a reasonable price for their scale, but it means
your existing access control and GitOps tooling don't cover them.

**It decides what agents do.** AX intentionally stops at "one isolated task";
its concepts page says it "does not try to model" an agent's lifetime of
planning, delegating and retrying. Sympozium's whole product is that layer:

- persistent agent identity with its own model, skills and memory;
- **tool gating**: per-tool allow/deny rules from `SympoziumPolicy`, applied
  by the controller so a denied tool never reaches the agent;
- **human approval** of a run's output through response gates;
- **tool isolation**: each skill runs as its own sidecar with a per-run
  ServiceAccount and least-privilege RBAC, so the agent container never holds
  the Kubernetes credential;
- ensembles, delegation and shared memory for teams of agents;
- cron schedules, chat channels, and a web UI and TUI for operators.

None of these exist in AX or Substrate. You would build them yourself on top.

**It is easier to install today.** `sympozium install` deploys the whole stack
from a released binary, including the Celln microVM plane. AX expects you to
build and push its images with `ko` and to install Substrate separately, with
PostgreSQL and object storage. On GKE there is a supported path; elsewhere,
Substrate's quickstart is still labelled "(Development)".

**It is model- and cloud-neutral.** Sympozium runs against any
OpenAI-compatible endpoint, Anthropic, or local servers, and places local
models through [llmfit-dra](https://github.com/sympozium-ai/llmfit-dra).
Substrate runs on any Kubernetes and stores snapshots in GCS or S3, but its
provisioning tooling targets GKE, and AX's goal-driven workspace setup depends
on Antigravity and a Gemini key.

**Its security posture is further along for what it does.** Sympozium is also
alpha, but its pod hardening, secret allowlisting and admission policy are
implemented rather than planned. Substrate's own
[threat model](https://github.com/agent-substrate/substrate/blob/main/docs/threat-model.md)
states it has "little to no security hardening at this time", and user
authorisation, audit logging and component mTLS are on its roadmap.

---

## Where it is closer than it looks

- **Hardware isolation.** Both have it: Substrate runs gVisor or Cloud
  Hypervisor microVMs, and Sympozium offers gVisor/Kata through `agent-sandbox`
  and KVM microVMs through Celln. The difference is checkpointing, not the
  strength of the boundary.
- **Workspaces and MCP.** AX's `Workspace` (Git repos, MCP servers, skills)
  covers similar ground to Sympozium's skills and `MCPServer` resources.
  Sympozium manages MCP servers as cluster resources; AX wires them into each
  task's filesystem.
- **Maturity.** Neither side is production-grade. Treat every row above as a
  snapshot.

---

## Can they work together?

Probably, and that may be the most useful way to think about it. Substrate
calls itself "a low-opinion system … not an SDK for building agents, but a
system for running them at scale". kagent's 1.0 alphas are rebuilding its
runtime on it. Sympozium already chooses between several execution backends per run
(Kubernetes Jobs, `agent-sandbox`, Celln), and Substrate could in principle be
another one. It would bring suspend/resume density underneath Sympozium's
policy and coordination.

**No such integration exists today.** It is not on the
[roadmap](roadmap.md), and it would first need Substrate's APIs and security
model to stabilise.

---

## When to choose what

**Choose AX + Agent Substrate when:**

- you are running very large numbers of mostly idle, stateful agents and
  compute cost is the dominant concern;
- you want a low-level runtime and plan to build coordination, policy and UX
  yourself;
- you are on GKE and can use Google's supported path;
- you need suspend/resume with memory state intact.

**Choose Sympozium when:**

- you want agents managed as Kubernetes resources, under your existing RBAC,
  namespaces and GitOps;
- you need per-tool policy and credential isolation between the agent and
  its tools;
- you are coordinating teams of agents with memory, schedules and chat
  channels, not only running isolated tasks;
- you want one install on any cluster, including on-prem and local models.

---

## Sources

- [google/ax README](https://github.com/google/ax), [DESIGN.md](https://github.com/google/ax/blob/main/DESIGN.md), [concepts](https://github.com/google/ax/blob/main/docs/concepts.md), [runners](https://github.com/google/ax/blob/main/docs/runner.md)
- [agent-substrate/substrate README](https://github.com/agent-substrate/substrate), [glossary](https://github.com/agent-substrate/substrate/blob/main/docs/glossary.md), [roadmap](https://github.com/agent-substrate/substrate/blob/main/docs/roadmap.md), [threat model](https://github.com/agent-substrate/substrate/blob/main/docs/threat-model.md), [egress contract](https://github.com/agent-substrate/substrate/blob/main/docs/network-egress.md)
- [Agent Substrate available on GKE](https://cloud.google.com/blog/products/containers-kubernetes/agent-substrate-available-on-gke) (Google Cloud blog, 2026-09-16)
- [Positioning](positioning.md): the layer Sympozium deliberately occupies
