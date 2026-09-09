# Epic: enduring AgentRuns with persistent Harness parents and disposable turn cells

## Relationship to existing work

This epic overlays https://github.com/sympozium-ai/sympozium/issues/426 and
coordinates with https://github.com/sympozium-ai/sympozium/issues/349.
It does not discard the signed catalogue, independent admission, host model
broker, warm-mote execution, cancellation, ownership or one-shot proofs already
delivered there. PR #463 now carries the integrated native persistent-harness
release candidate, paired with sympozium-ai/celln#98.

It supersedes #426's proposed use of a Celln-specific HarnessSession extension
and its assumption that the entire Harness is reconstructed outside a disposable
cell for each turn. It does not mark unfinished #426 operational gates complete.

## Agreed product model

## Standard installation follow-up — 2026-09-09

The next user-approved target is an installed native persistent Harness on
framework, selectable through the ordinary UI/YAML without the test supervisor.
The merged local starter RC is the baseline, not proof of this deployment.

- [x] Add opt-in chart packaging for a single node-pinned parent controller,
  rotating projected Kubernetes credentials, restricted namespace RBAC and
  persistent host issuance/approval storage. Local rendering/refusal tests pass;
  this checkbox does not claim an installed or qualified image.
- [x] Protect AgentRun finalizers from the uninstall stripper; refuse native
  uninstall before mutation while runs remain or the state read fails.
- [ ] Extract host/artifact/catalogue preparation from the test fixture into
  supported installation tooling; qualify and publish combined images.
  Partial: Celln's standalone operator-signed cold packager produced the five
  native bundles on framework. Admission, grants and catalogue binding remain.
- [ ] Establish controller ownership separation on framework without abandoning
  existing multi-namespace workloads; install the host owner, TLS edge and grants.
  Partial: namespace exclusion is implemented and unit/render tested; no shared
  controller rollout or native owner installation is claimed yet.
- [ ] Qualify normal UI/YAML creation and permissions on that installation.
- [ ] Real-model installed E2E: cross-turn files, HTTPS, cancellation/continue,
  refresh, credential rotation/restart, context loss and confirmed cleanup;
  regress ordinary Kubernetes and Celln one-shot execution.

Packaging instructions and outstanding prerequisites:
`docs/guides/celln-native-installation.md`.

## Release scope decision — 2026-09-09

User-approved priority: release a useful native multi-turn Harness MLP, then
iterate. This section supersedes broader M0–M3 release gating below; those
unchecked items remain the roadmap, not claims of completion.

Release gates:

- [x] Native persistent parent, fresh disposable child per turn, real model and
  borrowed-tool work through browser/API/scoped controller/TLS. Latest full
  process proof: `parent-upgraded-environment-live.log` (64.19s), with joined
  cleanup. This is local evidence, not a merged release.
- [x] Useful, explicitly selected starter toolbox with truthful permissions:
  run-owned workspace read/write and host-brokered allowlisted HTTPS fetch.
  Signed catalogue installation, all grant layers, UI starter selection and
  real guest effects passed in the combined system suite. Python is deferred.
- [x] One integrated acceptance sequence: create, model/tool turn, follow-up,
  cancel follow-up, continue, refresh/reconnect, delete and confirm teardown.
  Latest proof: `docs/evidence/celln-starter-system-2026-09-09.md`, 108.77s;
  real DeepSeek/KVM, browser/API/scoped controller/TLS, files and HTTPS,
  committed cancellation, continued conversation, refresh and confirmed
  three-parent teardown. Not a merged release claim.
- [ ] Focused failure/security suite: lease expiry, parent/context loss,
  controller/API restart and lost acknowledgements without duplicate work;
  tenant isolation, grant narrowing/revocation, budgets and cancellation races.
- [ ] Existing direct one-shot, Harness one-shot and server/OCI smoke regressions;
  basic footprint/timing measurements, reproducible installation and evidence.
- [ ] Coherent reviewed/merged PRs and release notes with supported configuration
  and limitations. No unsigned or unavailable tools advertised as usable defaults.

Deferred from this release: Python, pause/resume, checkpoints/transparent crash recovery,
initial-turn-only cancellation (whole-run stop/delete remains available),
parallel/nested turn trees, arbitrary Pi/Hermes compatibility, performance tuning
and exhaustive compatibility matrices. These are deferred, not completed.

Build discipline: focused tests for changed components; reuse prepared cluster
and immutable artifacts; one full release-candidate regression instead of a full
suite after every small edit. Tool work replaces deferred features rather than
expanding the release indefinitely. Tool UX and implementation requirements are
in `docs/design/celln-starter-toolbox.md` and tracked in
https://github.com/sympozium-ai/sympozium/issues/467.

### Product model (unchanged)

AgentRun is the execution envelope for both one-shot and enduring work.
Harness configuration is optional and describes execution behaviour; it is not
a competing execution lifecycle. Agent remains the stable identity/policy owner.
Approved AgentRuntime references and their trust boundary remain reusable.

Workload, lifecycle and backend are independent choices, subject to explicit
compatibility checks:

| Workload | Lifecycle | Celln behaviour |
| --- | --- | --- |
| Direct approved executable/tool invocation | One-shot | Warm-mote fork, sealed execution, bounded result, dissolution; no model or Harness required |
| Harness task | One-shot | Harness/model/tool loop inside one disposable cell |
| Multi-turn Harness | Enduring | Leased persistent parent cell retaining live Harness context; disposable child cell per turn |

Deterministic output is a property of the tool, inputs and permitted effects,
not a blanket guarantee of every direct invocation.

## Non-negotiable requirements

- The enduring AgentRun owns parent/child lifecycle, durable identities, budgets,
  transcript/audit retention and user interaction. Existing AgentRun server mode
  is a foundation, not proof that this conversation lifecycle already exists.
- The Harness parent actually remains alive inside a sealed cell across turns.
  An external coordinator repeatedly launching one-shots is not this milestone.
- Each accepted turn creates a distinct disposable child cell. Define exactly
  which Harness functions run in parent and child, which data crosses the
  boundary, and how successful child output updates parent context.
- The first supported Harness may be explicitly designed as a stateful parent
  plus bounded turn worker. Do not claim arbitrary Pi/Hermes process state can
  be transferred or merged without an adapter/snapshot contract.
- Sub-cells are host-managed cells with their own warden/microVM. No nested KVM,
  guest VM-management privileges, Kubernetes credentials or general host RPC.
- Child spawn is a CoW fork of an admitted warm mote, never a hot-path boot.
  Whether a quiesced parent snapshot can safely seed a child is an explicit
  research/qualification question, not an assumed existing capability.
- Executable identity remains hash-bound and signed; tool code remains sealed.
  Guest-requested child creation confers no authority. The host independently
  intersects session, runtime, operator and selected-tool grants for every child.
- Authority only shrinks. Parent longevity cannot renew revoked authority or
  replenish exhausted aggregate budgets by creating children.
- Model credentials stay host-side. Preserve the bounded PIO/host-broker design;
  do not add a guest network stack to obtain chat or child spawning.
- One active turn initially. Each message has a durable idempotency identity;
  uncertain outcomes are reconciled against the original owner, never replayed.
- Cancellation reaches the actual child owner and waits for confirmed teardown.
  Stopping/deleting/expiring a parent prevents new work and reclaims its children.
- Distinguish live-context persistence from crash durability. Explicitly report
  unrecoverable context loss; never silently restart an empty Harness as resumed.
- Keep working one-shot direct and Harness execution intact. Existing OCI
  HarnessSession users require a deliberate compatibility/migration path.

## M0 — architecture and compatibility contract

Progress notation: checked items have local implementation and scoped execution
evidence, not a merged/released production claim. The native model/tool and
browser two-turn checks are backed by the completed all-process proof at Celln
`target/declared-parent-pair-1837300-1788905002200189967/` and the implementation
notes. The separate hands-on hold remains in progress. Broad security, recovery,
compatibility and release gates below remain open until individually qualified.

- [ ] Specify AgentRun lifecycle/configuration schema, preserving existing task,
  server, direct invocation and Harness behaviours; refuse invalid combinations.
- [ ] Specify the supported stateful parent/turn-worker Harness architecture,
  versioned bounded mailbox/spawn/result protocol and state commit semantics.
- [ ] Specify leases, aggregate/per-turn limits, identity, cancellation, replay
  refusal, live context loss and eventual checkpoint recovery.
- [ ] Inventory actual warm fork, guest mailbox and VM run/pause capabilities;
  identify required Celln changes with evidence, not inferred guarantees.
- [ ] Define HarnessSession compatibility and ownership shared with #349.

## M1 — real persistent parent and per-turn child lifecycle

- [ ] Add a leased persistent parent cell with authenticated bounded host input
  delivery and guest response; demonstrate retained Harness state between turns.
- [ ] Add host-authorized child spawn and parent/turn/child identity binding.
- [ ] Prove two distinct child cells fork from warm motes without guest boot in
  the hot path, execute bounded work and dissolve while the parent survives.
- [ ] Enforce grant narrowing, one active turn, aggregate memory/model/time
  budgets, child-count limits, backpressure and whole-tree stop/lease expiry.
- [ ] Guest tests actually attempt forbidden child spawning, authority expansion,
  undeclared tools and stale-parent work. Unsupported hardware must refuse.

## M2 — multi-turn AgentHarness MLP in Sympozium

- [ ] Enduring AgentRun reconciler owns the real parent and immutable turn records,
  reusing the existing admission/router/receipt paths where valid.
- [x] Approved native stateful Harness plus approved borrowed tools performs real
  model work in the documented per-turn child architecture.
- [ ] UI/YAML select Harness, Celln, enduring lifecycle and explicit borrowed tools.
  Direct and Harness one-shot choices remain usable and distinctly labelled.
- [ ] Feed shows the enduring run as an interactive agent; transcript/results
  survive browser refresh and reconnect without another execution submission.
- [ ] Stop/resume, turn cancellation, lease expiry, failures and context-loss
  states are visible and map to confirmed backend actions.
- [ ] Durable audit correlates AgentRun, parent, turn, child, runtime/tool hashes,
  authority decision, model use and receipts without exposing credentials.

## M3 — acceptance and handoff

- [x] Real-model browser E2E: create enduring run; first turn establishes private
  context; second turn uses it; each turn uses its own child; parent identity and
  guest state persist. Include borrowed-tool use and evidence from guest code.
- [ ] Refresh/reconnect and API/controller restart retain identity and results;
  no duplicate child or model request on lost acknowledgements.
- [ ] Parent crash/host restart either recover a qualified checkpoint or explicitly
  report context loss. Never count transcript-only recreation as full recovery.
- [ ] Cross-session/tenant, changed identities, malformed/flooded protocol,
  revoked grants, budget exhaustion, cancellation races and parent death tests.
- [ ] Regress direct one-shot, Harness one-shot, server mode, OCI persistent
  HarnessSession, and the relevant #426 security/installation gates.
- [ ] Publish measured parent idle footprint and child spawn/turn/cleanup costs,
  compatibility limits, installation and a reproducible local hands-on environment.

## Explicitly outside the initial MLP

Arbitrary unmodified OCI/Pi/Hermes compatibility, unrestricted descendant trees,
parallel turns, transparent whole-process state merging, and unqualified host
crash recovery. These remain follow-up capabilities, not implied guarantees.

## Completion definition

The MLP is complete only when a user can interact over multiple turns with an
enduring AgentRun whose actual Harness parent stays alive inside Celln, whose
turns spawn real disposable child cells with verified authority/lifecycle, and
whose UI, failure behaviour and audit pass the acceptance above. A selector,
host-side transcript wrapper, or a permanently running one-shot test is not
completion. The full predecessor epic retains its remaining requirements.
