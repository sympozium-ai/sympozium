# Sympozium Roadmap

Last updated: 2026-10-30, after v0.12.1.

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

- **Pluggable execution plane, Substrate spike** ([#692](https://github.com/sympozium-ai/sympozium/issues/692)).
  Four weeks with exit criteria: an `AgentRun` runs as a Substrate actor on
  the maintainer's cluster, under a `SympoziumPolicy`, with the Agent's key
  never inside the sandbox, phases and logs on the `AgentRun`, and a written
  go/no-go with latency and idle-cost numbers against the Job and Celln
  backends. No-go is a valid outcome.
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
