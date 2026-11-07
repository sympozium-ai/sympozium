# Sympozium Roadmap

Last updated: 2026-10-30, after v0.12.2 and Substrate phase 0a.

> **Superseded:** the February 2026 OpenClaw gap analysis that used to live
> here is no longer the plan. It is kept as history in the
> [original design](design.md); see [Not planned](#not-planned) for what
> happened to its items.

This page says where the project is, what is being worked on, and what comes
next. It is maintained: when something here ships or is dropped, the page
changes in the same pull request. Detailed plans live in the linked design
notes and GitHub issues; this is the map, not the territory.

## Direction

Sympozium is the **coordination layer** for agents on Kubernetes: it decides
what agents do. Agents and their own model keys, policy and tool gating,
ensembles and delegation, channels, schedules, memory and the console, all as
Kubernetes resources under your RBAC and GitOps. See
[Positioning](positioning.md).

Decision of 2026-10-30: **isolation and density are an execution plane, and
the execution plane is pluggable.** Sandboxed, suspendable agent runtimes now
exist with far more people behind them than this project has: Google's
[Agent Substrate](https://github.com/agent-substrate/substrate) (which
[kagent](https://kagent.dev/) is rebuilding on) and Kubernetes
[Agent Sandbox](https://agent-sandbox.sigs.k8s.io/). Sympozium already runs
an `AgentRun` as a Job, as an Agent Sandbox, or as a
[Celln](https://github.com/sympozium-ai/celln) micro-VM cell. The plan is to
add Agent Substrate as an optional backend, keep Celln as the self-hosted,
zero-dependency option rather than the default, and spend the effort that
frees up on the coordination layer. The reasoning is in
[Sympozium vs Google AX and Agent Substrate](sympozium-vs-google-ax.md); the
work is tracked in [epic #692](https://github.com/sympozium-ai/sympozium/issues/692).

## Layers

```
┌──────────────────────────────────────────────────────────────────────┐
│ Sympozium: coordination layer (what agents do)                       │
│  identity & Agent keys · policy & tool gating · ensembles · channels │
│  schedules · memory · spend · observability & attribution · console  │
├──────────────────────── ExecutionBackend seam ───────────────────────┤
│  start · phase · logs · cancel · identity  (target: no key crosses)  │
├───────────────┬────────────────┬───────────────┬─────────────────────┤
│ Job (default) │ Agent Sandbox  │ Celln (opt-in,│ Agent Substrate     │
│               │                │ scope frozen) │ (exploratory, #692) │
└───────────────┴────────────────┴───────────────┴─────────────────────┘
```

**Sympozium owns**, for every backend:

- **Identity and keys.** The Agent is the only identity that can use its
  model key. The target for every backend is option (a) in #692: the key is
  added at our model gateway, which is the run's only route to a model, and
  no backend, node or sandbox holds it. Today that is true on Celln only; Job
  and Agent Sandbox pods still receive the key as an environment variable in
  the agent container, and model provider headers as `MODEL_PROVIDER_HEADERS`.
  That is a known exception for the Job (and Agent Sandbox) backend; closing
  it by routing those runs through the model gateway is a workstream below.
- **Policy and tool gating.** `SympoziumPolicy` allow/deny is enforced in our
  tool server and admission, not inside the sandbox.
- **Ensembles, channels, schedules and memory.** Delegation, handoffs and
  routing are decided above the backend.
- **Spend.** Budgets and ceilings belong in Sympozium, not in a backend, so
  a namespace ceiling holds whichever backend ran the turn
  ([#688](https://github.com/sympozium-ai/sympozium/issues/688)).
- **Observability and attribution.** Traces, token usage, cost and device
  energy are attributed to the run (namespace, run UID), including when a
  backend multiplexes many runs onto one warm worker.

**Backends own** isolation, density, placement and warm pools. The seam
between the two is deliberately narrow: start, phase, logs, cancel and
identity. Enduring runs and suspend/resume are not part of it yet. See
[#699](https://github.com/sympozium-ai/sympozium/issues/699), where the seam is being built.

| Backend | Status | Needs |
|---|---|---|
| Kubernetes Job | Default, first class | Nothing beyond Kubernetes |
| Agent Sandbox | Supported | The Agent Sandbox CRDs |
| Celln | Opt-in, scope frozen ([#693](https://github.com/sympozium-ai/sympozium/issues/693)) | KVM nodes, `--celln-fleet` |
| Agent Substrate | Spike: installed (phase 0a), AgentRun backend next ([#699](https://github.com/sympozium-ai/sympozium/issues/699)) | Three beta feature gates, gVisor, PostgreSQL, object storage (bundled in the single-node profile) |

Consumer hardware first still holds: a Kind cluster or a single small node
must get the whole coordination layer on the Job backend, with nothing else
to install.

Two threads carry on from 0.12 unchanged:

- **Agent-bound model access.** An Agent brings its own `ModelConnection`
  and key; nothing on an execution node ever holds a model credential, and
  every model request leaves through the model gateway under that Agent's
  identity. This holds on Celln today and is the bar for Substrate; Job and
  Agent Sandbox runs still mount the key into the agent container and move
  behind the gateway next (see In progress). See
  [Agent-bound model access](design/celln-agent-bound-model-access.md).
- **Bring-your-own harness.** External runtimes (Codex, Claude Code, Pi,
  Hermes, Goose and friends) run under an administrator-approved
  `AgentRuntime` with a versioned adapter contract. See the
  [harness epic (#349)](https://github.com/sympozium-ai/sympozium/issues/349).

## Shipped in 2026

| When | What | Where to read |
|---|---|---|
| Oct (0.12) | Agent-bound model access: model-free Celln fleet, per-Agent keys pinned to their origin, any OpenAI- or Anthropic-compatible provider, direct Agent API (messages and `chat/completions`), onboarding wizard | [design](design/celln-agent-bound-model-access.md), [guide](guides/celln-mediated-model-access.md) |
| Oct (0.12.1) | In-place gate retry for agent-runner and harness-mode runs (resume), memory update/forget, thinking and `maxTokens`/`temperature` tuning, faster Celln admission and turns | [CHANGELOG](https://github.com/sympozium-ai/sympozium/blob/main/CHANGELOG.md) |
| Sep–Oct | Celln tenancy: one-shot and enduring runs from any authorised namespace, one-install fleet packaging, trusted transport, operator and tenant runbooks | [epic #495](https://github.com/sympozium-ai/sympozium/issues/495), [fleet installation](guides/celln-fleet-installation.md) |
| Sep | Enduring AgentRuns: persistent parents, disposable turn cells, exact-turn cancellation, lease and budget accounting | [epic #464](https://github.com/sympozium-ai/sympozium/issues/464), [design](design/enduring-agentrun-celln-epic.md) |
| Aug–Sep | Harness mode: `AgentRuntime`, adapter contract `v1alpha1`/`v1alpha2`, reference adapter, conformance suite, run-detail provenance | [epic #349](https://github.com/sympozium-ai/sympozium/issues/349), [harness adapters](modes/harness-adapters.md) |
| Mar–Jul | Web console, `sympozium doctor`, token usage and cost tracking, ensemble delegation (`delegate_to_persona`, canvas, await/resume), Slack, Discord, Telegram and WhatsApp channels, MCP servers and skill packs | [Concepts](concepts/custom-resources.md), [ensemble roadmap](design/ensemble-coordination-roadmap.md) |

## In progress

- **ExecutionBackend seam.** The controller's Job, Agent Sandbox and Celln
  paths move behind one narrow interface with no behaviour change, so a new
  backend is an implementation, not a fork of the reconciler. Part of
  [#692](https://github.com/sympozium-ai/sympozium/issues/692).
- **Agent keys behind the gateway on every backend.** Job and Agent Sandbox
  runs reach the model through a gateway instead of mounting the Agent's
  key, so the per-Agent key guarantee no longer depends on which backend ran
  the turn. Harness runs first: a per-run token swapped for the key outside
  the pod, opt-in by policy
  ([#700](https://github.com/sympozium-ai/sympozium/pull/700), draft). The
  same route is what lets the #688 spend ceiling hold for every backend.
- **Pluggable execution plane, Substrate spike** ([#692](https://github.com/sympozium-ai/sympozium/issues/692)).
  Four weeks with exit criteria: an `AgentRun` runs as a Substrate actor on
  the maintainer's cluster, under a `SympoziumPolicy`, with the Agent's key
  never inside the sandbox, phases and logs on the `AgentRun`, and a written
  go/no-go with latency and idle-cost numbers against the Job and Celln
  backends. No-go is a valid outcome.
  - Phase 0a, install, is done: Substrate v1.0.0-rc.1 on a single-node
    kubeadm cluster (Kubernetes 1.35) took three beta feature gates and about
    five minutes; a gVisor actor resumes from its golden snapshot in 0.65 s
    and suspends in 0.44 s
    ([install record](evidence/substrate-install-framework-2026-10-30.md)).
  - Phase 0b, an `AgentRun` as a Substrate actor, is next
    ([#699](https://github.com/sympozium-ai/sympozium/issues/699)): an
    atespace per namespace, an `ActorTemplate` per runtime image digest, an
    actor per run (resume per turn, suspend between), egress pinned to our
    model gateway so the key path does not change, tools as MCP servers, and
    a vendored copy of Substrate's one public proto.
- **Celln becomes an option, not the default** ([#693](https://github.com/sympozium-ai/sympozium/issues/693)).
  `sympozium install` deploys the fleet only with `--celln-fleet`; the Job
  backend is the default for new Agents; onboarding offers Celln alongside
  Agent Sandbox and, later, Substrate. Existing fleet installs keep their fleet.
- **Celln turn latency.** With the provider out of the picture, a one-shot
  turn costs about 2.5 s of Sympozium and Celln time (admission 1.5 s, the
  rest observation and cleanup). Next: cache the resolver's reads during
  admission, make the per-turn tool budget a tool error instead of ending the
  turn, and let an Agent lend a leaner toolbox than the whole profile.
  Tracked in [#686](https://github.com/sympozium-ai/sympozium/issues/686); measurements in the
  [design note](design/celln-agent-bound-model-access.md#delivery).
- **Harness epic remainder (#349).** Signature or attestation policy for
  approved runtime images; whether remote MCP credentials stay visible to
  adapters or are mediated by a local proxy; SkillPack MCP server conformance
  for Streamable HTTP clients; stable failure categories for one-shot harness
  runs; pinned examples for Codex, Claude Code and Goose with support tiers;
  scheduled smoke tests with two real adapters.
- **Ensemble policy and safety** (phase 4 of the
  [ensemble roadmap](design/ensemble-coordination-roadmap.md)):
  relationship-scoped delegation rules, cycle detection, timeouts.

## Next

Ordered by what unblocks the most users. Each has, or will get, an issue.

1. **Substrate as a released backend** (phase 1 of #692, only on a go):
   `spec.backend: substrate`, validation and parity tests, install wiring or a
   documented external install, backend choice in the console, and enduring
   runs as suspended actors instead of leased parents.
2. **Spend and operations.** A per-namespace spend ceiling owned by
   Sympozium and enforced for every backend, and refusal events on the run
   ([#688](https://github.com/sympozium-ai/sympozium/issues/688)). It needs
   the Job backend's model traffic routed through the model gateway (see
   In progress). A KVM CI lane that boots cells on an enforcing CNI and
   publishes release evidence
   ([#685](https://github.com/sympozium-ai/sympozium/issues/685)) is kept
   through the Celln scope freeze so the opt-in backend does not rot; Celln
   maintenance (secret providers with live withdrawal, fleet-wide revocation)
   is tracked in its
   [roadmap project](https://github.com/orgs/sympozium-ai/projects/1).
3. **Observability and attribution.** Per-run device energy that stays
   correct when a backend runs many runs on one warm worker, and
   an OTel transport with durable execution audit retention for the fleet.
4. **Provider breadth without code.** Presets for OpenAI-compatible gateways
   (Databricks, OrcaRouter and others) on top of the `custom` connection,
   model discovery per provider where the API allows it, a reachability
   condition on `ModelConnection` status ([#15](https://github.com/sympozium-ai/sympozium/issues/15)),
   the Responses API for OpenAI reasoning models with tools
   ([#608](https://github.com/sympozium-ai/sympozium/issues/608)), and a guide
   for subscription auth through a Claude Code harness adapter
   ([#684](https://github.com/sympozium-ai/sympozium/issues/684)).
5. **Channels.** Ensemble-aware routing (a receiver persona and `@mention`
   delegation, [#257](https://github.com/sympozium-ai/sympozium/issues/257)),
   an agent that can ask a question through its channel and wait for the
   answer mid-turn ([#20](https://github.com/sympozium-ai/sympozium/issues/20)),
   and the long-standing requests for Signal, Teams, Matrix and WebChat, in
   the order people ask.

Small, well-scoped items carry the `good first issue` label; the current set
is listed on the
[issues page](https://github.com/sympozium-ai/sympozium/issues?q=is%3Aopen+label%3A%22good+first+issue%22).

## Not planned

- A model credential held by the fleet, a node, or the installer. 0.12 removed
  it on purpose; see the threat model in the design note.
- Agent pods with host networking, host PID, or privileged SkillPacks under an
  external runtime.
- Growing Celln's scope. It stays maintained as the self-hosted isolation
  option (turn latency, fleet operations), but harness-in-a-cell for Pi and
  Hermes ([#687](https://github.com/sympozium-ai/sympozium/issues/687), closed)
  and other new data paths into the cell are not planned; a sandbox runtime
  with a community behind it is where that belongs.
- Competing with Agent Substrate or Agent Sandbox on density or isolation
  depth. Sympozium sits above them.
- Reviving the February 2026 OpenClaw gap list as a plan. Its items are either
  shipped (dashboard, doctor, usage tracking, delegation), folded into the
  lists above, or not wanted; the
  [original design](design.md) is kept as history.

## How work is tracked

- **Issues** are the backlog. Epics carry the `epic` label and a checklist;
  slices link back to their epic. Labels name the area (`celln`, `harness`,
  `console`, `channels`, `provider`) and the kind (`bug`, `enhancement`,
  `documentation`, `help wanted`, `good first issue`).
- **Pull requests** are reviewed against current `main`. One untouched for 45
  days is labelled `stale` and closes two weeks later unless someone speaks
  up; a maintainer may instead take it over, keeping the author's commits and
  credit. Issues get 120 days and three weeks of grace. `pinned` exempts an
  item from both.
- **Releases** are cut by release-please from conventional commits; the
  [CHANGELOG](https://github.com/sympozium-ai/sympozium/blob/main/CHANGELOG.md)
  is the record of what shipped when. Celln is pinned in
  `config/celln/release.json` and moves by its own release.
- **Design notes** under [Design & History](design/index.md) record why; this
  page records what and when.
