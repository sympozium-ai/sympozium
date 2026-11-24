# Sympozium vs Google AX and Agent Substrate

In September 2026 Google open-sourced two related projects:
[AX](https://github.com/google/ax) (Agent Executor), a declarative orchestrator
for agent tasks, and [Agent Substrate](https://github.com/agent-substrate/substrate),
the sandboxed execution runtime underneath it. Both run on Kubernetes and both
treat agents as a new kind of workload, which is also Sympozium's starting point.
This page is an honest comparison: where they are ahead, where Sympozium is,
and where the projects are not really competing at all.

The relationship has changed since this page was first written. Sympozium
no longer treats Agent Substrate as a rival runtime: since 2026-11-24
**Substrate is Sympozium's default execution plane**, and Sympozium is the
layer above it. So read "Sympozium vs Substrate" below as "what Substrate
gives you on its own, and what Sympozium adds on top". AX remains a
different product at Sympozium's layer, with a much narrower scope.

!!! info "Scope and date"
    AX and Substrate rows last reviewed **2026-09-23** against `google/ax` v0.3.0 and
    `agent-substrate/substrate` v0.1.0, from their own READMEs, design docs,
    roadmap and threat model, plus Google's
    [GKE announcement](https://cloud.google.com/blog/products/containers-kubernetes/agent-substrate-available-on-gke).
    Sympozium rows updated **2026-10-30** for v0.12.1 (agent-bound model access)
    and **2026-11-24** for the decision to make Substrate the default execution
    plane. The AX and Substrate rows have not been re-reviewed against
    Substrate 1.0; treat their maturity statements as a snapshot of
    September 2026. If something here is out of date, please open an issue
    or PR.

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

## The short version

- **Agent Substrate is an execution runtime, and it goes deeper than anything
  in Sympozium.** It multiplexes many idle, stateful agents onto a small pool
  of sandboxed worker pods, snapshotting memory and filesystem so an agent can
  be suspended and resumed on any worker. Sympozium has nothing equivalent
  of its own, which is why it now runs on Substrate instead of competing
  with it.
- **AX is a thin, fast task API on top of Substrate**: four resource kinds
  (`Task`, `Workspace`, `Gateway`, `Model`) and a `kubectl`-shaped CLI. It
  deliberately does not model what an agent *does* over its lifetime.
- **Sympozium is a coordination layer**: agent identity, policy, tool
  isolation, ensembles, memory, schedules, channels and a UI, expressed as
  Kubernetes CRDs. It decides what agents do. It does not try to be a
  high-density runtime; it uses one.

So the projects sit at different layers of the stack. Substrate is the
execution layer under Sympozium. AX and Sympozium both sit above Substrate,
and differ in how much they take on: AX stops at one isolated task,
Sympozium covers an agent's identity, policy and teamwork.

---

## At a glance

| Dimension | Sympozium | AX + Agent Substrate |
|---|---|---|
| **What it is** | Coordination layer for multi-agent systems, above an execution plane (Substrate by default) | Task orchestrator (AX) on an agent execution runtime (Substrate) |
| **API surface** | Kubernetes CRDs (`Agent`, `AgentRun`, `Ensemble`, `SympoziumPolicy`, …) | AX: its own gRPC API, resources stored in Redis. Substrate: two CRDs (`WorkerPool`, `SandboxConfig`); actors, templates and atespaces go through its gRPC API into PostgreSQL |
| **Works with kubectl / RBAC / GitOps** | Yes: every resource is a Kubernetes object | Only the Substrate CRDs. Tasks, workspaces, gateways, models and actors are not Kubernetes objects |
| **Unit of execution** | An `AgentRun`, executed as a Substrate actor (default), a Celln microVM cell, or a Pod (Job) | A Substrate *actor*, assigned on demand to a pre-warmed worker pod |
| **Idle agents** | Whatever the execution plane does. On Job and Celln they consume their Pod or cell until they finish, time out or hit their lease; suspension comes from Substrate (right) | Suspended to object storage (memory + filesystem) and resumed on any worker; Google quotes sub-500 ms resume |
| **Isolation** | The execution plane's: gVisor actors via Substrate; KVM microVMs via Celln; hardened Pods, with gVisor/Kata via `agent-sandbox` | gVisor or Cloud Hypervisor microVMs, per actor |
| **Egress control** | Substrate's egress gateway, narrowed per run to Sympozium's own endpoints; brokered tools in Celln; Kubernetes NetworkPolicy for Pods | All actor TCP forced through an mTLS egress proxy with per-actor certificates; non-HTTP(S) blocked |
| **Model credentials** | Each Agent owns its key. Substrate and Celln runs: the key is added by Sympozium's model gateway outside the sandbox, after it has mapped the caller to a run and the run to an Agent. Pod runs: per-key env injection from an allowlist | Google describes egress proxies that inject credentials; the Substrate roadmap still lists proxy-based credential injection as upcoming work |
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

These are the reasons Sympozium adopted Substrate as its execution plane
instead of trying to close the gaps itself.

**Density and idle cost.** This is Substrate's reason to exist, and it is a
real problem. Agents burst for a moment, then wait seconds or minutes on a
model, a tool or a person. A Sympozium run keeps its Pod (or its Celln cell)
for that whole time. Substrate checkpoints the idle agent, gives the worker to
someone else, and restores the agent on any worker when a request arrives for
it. Google's figures (10× density over standard container runtimes, over 1,000
dormant agents per host, 500+ suspend/resume operations per second) are
vendor numbers without a published method. The repository does ship a load-test
harness, but we have not reproduced them. The mechanism is sound, though. Our
own measurements are smaller in scope: on one node, a suspended actor held
no worker process memory and about 0.6 MB of storage, and a suspended actor
resumed in 0.23 s
([spike results](evidence/substrate-spike-go-no-go-2026-11-22.md),
2026-11-22). Sympozium's Job and Celln backends have no answer to this,
which is the main reason they are now the alternatives.

**Scale of the control plane.** AX's design doc is explicit that storing
millions of short-lived tasks as CRDs would overload etcd, so it keeps tasks in
Redis and Substrate keeps actors in PostgreSQL. Sympozium stores every
`AgentRun` as a CRD in etcd, whichever backend executes it. That is the right
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

**It is easier to install today.** `sympozium install` deploys the control
plane from a released binary and runs agents as Kubernetes Jobs with nothing
else installed; `--celln-fleet` adds the Celln microVM plane. AX expects you to
build and push its images with `ko` and to install Substrate separately, with
PostgreSQL and object storage. On GKE there is a supported path; elsewhere,
Substrate's quickstart is still labelled "(Development)". Running Sympozium
on Substrate inherits that install: Substrate has to be there first.

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

## How they fit together

Substrate calls itself "a low-opinion system … not an SDK for building
agents, but a system for running them at scale". Sympozium is the opinions.
kagent's 1.0 alphas made the same choice and are rebuilding its runtime on
Substrate.

On Sympozium, an `AgentRun` is still a Kubernetes object, admitted under
your `SympoziumPolicy`. The controller then creates a Substrate actor for
it. What each side contributes:

| | Substrate provides | Sympozium adds |
|---|---|---|
| **Execution** | The sandbox, the snapshot it starts from, suspend and resume, the worker it lands on | Nothing. This is not Sympozium's layer |
| **Identity and keys** | A short-lived token, stamped at the egress gateway, that says which actor is calling | The mapping from actor to run to Agent to key, and a model gateway that adds the Agent's key outside the sandbox on every call |
| **Policy** | An egress policy per actor | `SympoziumPolicy`, tool allow and deny, and admission before an actor exists. The actor's egress is narrowed to Sympozium's own endpoints |
| **Coordination** | Nothing, by design | Ensembles, delegation, shared memory |
| **Reach** | A control API | Channels, schedules, an OpenAI-compatible API per Agent |
| **Operations** | Two CRDs; actors in PostgreSQL behind a gRPC API | Every run as a CRD under your RBAC and GitOps, a console, TUI and CLI, token and cost tracking |

In the spike that led to the decision, a one-shot run reached `Succeeded` in
0.43 s as an actor against 11.3 s as a Job (median of 20 runs each, one
node, stub model, 2026-11-22). How the backend works is on the
[Substrate backend](concepts/substrate-backend.md) page; the decision and
its history are in [Positioning](positioning.md#how-we-got-here).

**Substrate is the default, not a requirement.** Sympozium keeps two other
execution backends. [Celln](concepts/celln-backend.md) is the self-hosted
option: KVM microVMs, with no dependency outside your cluster. A Kubernetes
Job needs nothing installed at all. Both stay because Substrate is young and
needs a database and an object store of its own, which not every cluster can
take on.

---

## When to choose what

**Choose Agent Substrate on its own (with or without AX) when:**

- you want a low-level runtime and plan to build identity, policy,
  coordination and UX yourself;
- your workload is millions of short-lived tasks, where a Kubernetes object
  per run is the wrong trade-off;
- isolated tasks are all you need, and AX's four resource kinds cover them.

**Choose Sympozium when:**

- you want agents managed as Kubernetes resources, under your existing RBAC,
  namespaces and GitOps;
- you need per-tool policy and credential isolation between the agent and
  its tools;
- you are coordinating teams of agents with memory, schedules and chat
  channels, not only running isolated tasks;
- you want to choose the execution plane per cluster or per run: Substrate
  for density, Celln where nothing outside the cluster is allowed, a Job
  where nothing extra can be installed;
- you want on-prem and local models to work the same way as hosted ones.

---

## Sources

- [google/ax README](https://github.com/google/ax), [DESIGN.md](https://github.com/google/ax/blob/main/DESIGN.md), [concepts](https://github.com/google/ax/blob/main/docs/concepts.md), [runners](https://github.com/google/ax/blob/main/docs/runner.md)
- [agent-substrate/substrate README](https://github.com/agent-substrate/substrate), [glossary](https://github.com/agent-substrate/substrate/blob/main/docs/glossary.md), [roadmap](https://github.com/agent-substrate/substrate/blob/main/docs/roadmap.md), [threat model](https://github.com/agent-substrate/substrate/blob/main/docs/threat-model.md), [egress contract](https://github.com/agent-substrate/substrate/blob/main/docs/network-egress.md)
- [Agent Substrate available on GKE](https://cloud.google.com/blog/products/containers-kubernetes/agent-substrate-available-on-gke) (Google Cloud blog, 2026-09-16)
- [Positioning](positioning.md): the layer Sympozium deliberately occupies
