# Sympozium Roadmap

Last updated: 2026-11-24, after v0.13.0.

This page says where the project is, what is being worked on, and what comes
next. It is maintained: when something here ships or is dropped, the page
changes in the same pull request. Detailed plans live in the linked design
notes and GitHub issues; this is the map, not the territory.

## Direction

Sympozium is the **coordination layer** for agents on Kubernetes: it decides
what agents do. It does not run them. It sits above an execution plane and
adds five things that plane does not have:

- **Identity and keys.** Each Agent owns its provider, its model and its
  key, and the key never enters the sandbox (true on Substrate and Celln;
  the Job backend still injects it into the pod).
- **Policy.** `SympoziumPolicy`, tool allow and deny, and admission,
  enforced outside the sandbox.
- **Coordination.** Ensembles, delegation and shared memory.
- **Reach.** Channels, schedules and the Agent API.
- **Operations.** Kubernetes resources under your RBAC and GitOps, the
  console, TUI and CLI, and token and cost tracking.

Decision of 2026-11-24: **[Agent Substrate](https://github.com/agent-substrate/substrate)
is Sympozium's default execution plane.** An `AgentRun` runs as a Substrate
actor. Two alternatives stay, each for a reason:
[Celln](https://github.com/sympozium-ai/celln) is the self-hosted option,
KVM micro-VMs with no dependency outside the cluster, and a Kubernetes Job
needs nothing installed at all. The stack is four layers with one question
each: coordination (Sympozium, what agents do), execution (Substrate, Celln
or a Job, how agents run), capability
([llmfit-dra](https://github.com/sympozium-ai/llmfit-dra), where compute
happens) and serving (vLLM, SGLang, llama.cpp, how tokens move). See
[Positioning](positioning.md) and the
[Substrate backend](concepts/substrate-backend.md) page.

This replaces part of the decision of 2026-10-30, which said that isolation
and density are a pluggable execution plane, with Substrate to be added as
an optional backend and the Job as the default
([epic #692](https://github.com/sympozium-ai/sympozium/issues/692)). The
first half stands. The second changed after the spike: a run executed as an
actor through the real controller, with the Agent's key outside the
sandbox, in 0.43 s against 11.3 s for a Job (median of 20 runs each, one
node, stub model, 2026-11-22;
[results](evidence/substrate-spike-go-no-go-2026-11-22.md)). Sandboxed,
suspendable runtimes also have far more people behind them than this
project has ([kagent](https://kagent.dev/) is rebuilding on Substrate too),
so the effort goes into the coordination layer instead. The comparison is in
[Sympozium vs Google AX and Agent Substrate](sympozium-vs-google-ax.md).

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

Two threads carry on from 0.12 unchanged:

- **Agent-bound model access.** An Agent brings its own `ModelConnection`
  and key; nothing on an execution node ever holds a model credential, and
  every model request leaves through the model gateway under that Agent's
  identity. This holds for every backend, including Substrate. See
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

- **Substrate as the default execution plane** ([#692](https://github.com/sympozium-ai/sympozium/issues/692)).
  The spike ([#699](https://github.com/sympozium-ai/sympozium/issues/699))
  ended on 2026-11-22 with a go: a run executes as an actor through the real
  controller in about 0.4 s against about 11 s for a Job, with the key added
  outside the sandbox
  ([results](evidence/substrate-spike-go-no-go-2026-11-22.md)). The
  backend is in review as experimental and off in the chart by default
  ([#717](https://github.com/sympozium-ai/sympozium/pull/717)). On
  2026-11-24 it became the default plane rather than an option. Stacked on
  that, not released:
    - a run that names no backend runs as an actor where Substrate is
      enabled and can execute it, and as a Job otherwise, with the reason on
      the run (20 of 20 live runs, p50 0.41 s, on an earlier build);
    - chart values (`substrate.*`) wire the controller and the model
      gateway, rendered and tested but not yet deployed, and refusing every
      run until the operator acknowledges the unencrypted hop;
    - the first tools are served outside the sandbox under the run's tool
      policy: channel messages and schedules.

    Left before a release, in order: MCP servers and skills through the tool
    endpoint, delegation and sub-agents, the unencrypted hop to the model
    gateway, enduring runs as suspended actors, a deployed install on a
    cluster, and a rerun on Substrate 1.0.1. See the
    [design note](design/substrate-backend.md).
- **Celln becomes an option, not the default** ([#693](https://github.com/sympozium-ai/sympozium/issues/693)).
  `sympozium install` deploys the fleet only with `--celln-fleet`; the Job
  backend is what runs where Substrate is not installed; onboarding offers Celln
  alongside Agent Sandbox and Substrate. Existing fleet installs keep their fleet.
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

1. **Substrate as the released default** (phase 1 of #692): every tool a
   Job-backed run has, served outside the sandbox (MCP servers and skills
   first); enduring runs as suspended actors instead of leased parents;
   `sympozium install` wiring; the backend and its actor in the console.
2. **Fleet operations.** A per-namespace spend ceiling and refusal events on the
   run ([#688](https://github.com/sympozium-ai/sympozium/issues/688)); a KVM CI
   lane that boots cells on an enforcing CNI and publishes release evidence
   ([#685](https://github.com/sympozium-ai/sympozium/issues/685)). On the Celln
   side: secret providers with live withdrawal, fleet-wide revocation, and a
   persistent execution model, tracked in its
   [roadmap project](https://github.com/orgs/sympozium-ai/projects/1).
3. **Observability.** OpenTelemetry `gen_ai.*` semantic conventions on
   agent-runner spans (maintainer take-over of an external PR, in review), and
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
