# ExecutionBackend seam

Status: first step landed with this note, 2026-10-30. Part of
[epic #692](https://github.com/sympozium-ai/sympozium/issues/692); the
Substrate backend that will be the seam's first new implementation is
[#699](https://github.com/sympozium-ai/sympozium/issues/699). The first change
moves the existing Job and Celln paths behind the seam with no behaviour
change.

## Why

The `AgentRun` reconciler runs a run in one of three ways today. Each has its
own branch in `Reconcile`, `reconcilePending`, `reconcileRunning` and
`reconcileDelete`:

| Path | Selected by | Start | Observe | Cancel / cleanup |
|---|---|---|---|---|
| Kubernetes Job (default) | neither of the other two | `buildJob` + create, in `reconcilePending` | Job and pod status in `reconcileRunning` | delete the Job in `reconcileDelete` |
| Agent Sandbox | `spec.agentSandbox.enabled`, then `status.sandboxName`/`sandboxClaimName` | `reconcilePendingAgentSandbox` | `reconcileRunningAgentSandbox` | owner references |
| Celln (scoped platform) | `sharedCatalogueSelected`, before model normalisation | `reconcilePendingScoped` | `reconcileRunningScoped` / `observeScoped` | `cleanupScoped` |

Adding Agent Substrate as a fourth branch in each of those functions would
make the reconciler harder to reason about for every backend. The seam makes a
backend an implementation of one interface instead.

## Layering

Sympozium stays the coordination layer. These stay **above** the seam and are
never delegated to a backend:

- **Agent keys.** The Agent is the only identity that can use its model key.
  Target for every backend (option (a) in #692): the backend's only route to a
  model is our model gateway, which adds the key. Nothing in the seam carries
  a key or a Secret reference. Today Celln meets this; the Job backend does
  not (see [Known exceptions](#known-exceptions-and-gaps-not-fixed-by-the-first-change)).
- **Policy and tool gating.** `SympoziumPolicy` admission and tool allow/deny
  run before `Start` and in our tool server.
- **Model key authorisation** (`authorizeRunKeys`). Target: above the seam
  for every backend. Today it is above the seam for Job only; for Celln it
  still runs inside `reconcilePendingScoped` (`celln_scoped.go`), that is
  below the seam, and moves up with the `Admit` split.
- **Spend.** See [Backend-neutral spend ceiling](#backend-neutral-spend-ceiling-688).
- **Ensembles, delegation, channels, schedules, memory, gates, postRun hooks.**
  The reconciler drives these from the run's phase and result, whatever
  backend produced them.
- **Attribution.** Traces, token usage, cost and device energy are keyed to
  the run, using the identity the backend reports.

## The interface

The target contract:

```go
type ExecutionBackend interface {
    Name() BackendName
    Capabilities(ctx) (Capabilities, error)               // ErrBackendTemporarilyUnavailable => requeue
    Admit(ctx, run) (Admission, *Refusal, error)          // refuse before anything starts
    Start(ctx, Admission, idempotencyKey) (StartResult, error)
    Phase(ctx, run) (Phase, error)                        // includes Uncertain and ContextLost
    Result(ctx, run) (*Result, error)                     // terminal only
    Logs(ctx, run) (string, error)                        // may be ErrLogsUnsupported
    Cancel(ctx, RunIdentity) error                        // stop the work
    Release(ctx, RunIdentity) (confirmed bool, err error) // idempotent cleanup, separate from Cancel
    Identity(run) RunIdentity
}
```

### Step 1 (this change): wrap, don't split

`internal/controller/execution_backend.go` defines the types below and an
`ExecutionBackend` interface whose `Start`, `Observe` and `Release` are still
reconcile-shaped, because they wrap today's functions unchanged:

| Verb | Job | Celln |
|---|---|---|
| `Owns` | not Celln, not Agent Sandbox | `sharedCatalogueSelected` |
| `Start` | `startJob` (the Job tail of `reconcilePending`, moved verbatim) | `reconcilePendingScoped` |
| `Observe` | `observeJob` (the Job part of `reconcileRunning`, moved verbatim) | `reconcileRunningScoped` |
| `Release` | delete the Job (from `reconcileDelete`) | `cleanupScoped` |
| `Logs` | `readAgentLogs` (terminal) | unsupported |
| `Phase`, `Result`, `Identity`, `Capabilities` | read-only views of status | read-only views of status |

`Reconcile`, `reconcilePending`, `reconcileRunning` and `reconcileDelete`
call the backends in today's order: a persisted or selected Celln scoped
binding first (before model connection normalisation), then Agent Sandbox
(still inline), then Job. For Job, shared admission (runtime resolution,
policy, gate validation, token budget, key authorisation) stays in
`reconcilePending` above the seam. Celln is selected before that code runs,
so its admission, including `authorizeRunKeys`, happens inside
`reconcilePendingScoped`, below the seam, until `Admit` is split out.

What step 1 implements versus what is a target:

| Item | Status |
|---|---|
| `Phase`: `Refused`, `Uncertain`, `ContextLost` for Celln | done. `Refused` = a shared admission refusal (`ModelKeyAuthorized=False`, typed `MODEL_KEY_*` code, Job and Celln alike) or `status.cellnScoped.nativePhase == "Refused"` (also after `StartAttempted`); `ContextLost` = a receiver-reported `AUTH_CONTEXT_LOST` (`CellnContextLost` condition); `Uncertain` = `CellnScopedExecution` Unknown on a live run, including the `NativeOwnerUncertain` grace period, which can still end in a lease-elapsed success |
| `Result` with an opaque `Receipt` | done, read-only from status |
| `Admit`, the idempotency key on `Start`, `Cancel`, and the `Unknown` phase | target; not implemented |
| Closed enums for `RefusalCode` / `PublicReason` | target. Today `RefusalCode` is a string; Celln's code is the `AUTH_*` prefix `scopedFailureSummary` writes, empty when it mapped none |
| A bound on `Result.Output` size | target |
| "Celln success requires a receipt" enforced in `Result` | target; `Result` reports whatever status holds |
| `ErrBackendTemporarilyUnavailable` | declared, not returned yet: backend holds (e.g. `ScopedDispatchDisabled`) still show as `Pending` |
| `Capabilities` | Job reports `ready`; Celln is hard-coded to `unknown` |
| One release path | not yet: `reconcileCompleted` still calls `cleanupScoped` directly, a second Celln release path beside `Release` in `reconcileDelete`, to fold in later |

### Next steps

1. Split `Start` into `Admit` and `Start(admission, idempotencyKey)`, with
   the caller persisting "attempted" first, as `celln_scoped.go` already does.
2. Split `Observe` into `Phase` plus `Result`, so `succeedRun`, `failRun`,
   memory and postRun hooks are driven by the controller from the typed
   result instead of from inside each backend.
3. Separate `Cancel` from `Release`.
4. Lift the gateway reserve → start → release → close sequence out of the
   Celln dispatcher (`internal/cellnscoped`) into the controller, above the
   seam. That is the hook for #688.
5. Wrap Agent Sandbox; add Substrate (#699).

### Types

- **Admission / Refusal.** `Admit` returns an `Admission` that `Start`
  consumes, or a `Refusal{Code, Message}`. Refusal is a distinct result from
  failing after start: the backend did no work, a spend ledger must not
  charge it, and retry policy differs. Target: codes are a closed enum;
  Celln's `AUTH_*` codes (`internal/cellnplatform` resolver, `celln_scoped.go`
  admission) and the shared `MODEL_KEY_*` codes map onto it.
- **Start is idempotent** (target). The caller persists "attempted" with an
  idempotency key before calling `Start`, as `celln_scoped.go` already does
  before dispatch, so a replayed reconcile never creates a second execution.
- **Phase** is `Pending | Running | Succeeded | Failed | Refused | Uncertain |
  ContextLost | Unknown`. `Uncertain` (the backend cannot confirm the
  outcome) and `ContextLost` (the owner reports the context gone) are
  distinct from `Failed`: nothing may be replayed or replaced, and an
  operator has to look. Celln: `scopedUncertain` (including the
  `NativeOwnerUncertain` grace period) is `Uncertain`; a receiver-reported
  `AUTH_CONTEXT_LOST` is `ContextLost`.
- **Result** is `{Outcome, PublicReason, Output, Receipt}`. `PublicReason`
  is to be a closed enum (target) and `Output` bounded in size (target); free-text native diagnostics never reach tenant-visible
  status (as `scopedFailureSummary` does today). `Receipt` is opaque and
  write-once: Sympozium stores it (Celln: `status.cellnScoped.receiptDigest`;
  request ID, resolved hashes, verdict) but never interprets it, and rejects a
  change to it. Target: a backend may require a receipt on success (Celln
  would), and `Result` rejects a success without one; not enforced yet.
- **Release** is idempotent cleanup that returns whether cleanup is
  confirmed, separate from `Cancel` (stop the work). Celln's
  `cleanupScoped` / `CleanupConfirmed` already work this way.
- **Capabilities** are `{State, Lifecycles, Logs: none|terminal|stream,
  Turns}` where `State` is `ready | not_installed | transport_invalid |
  unknown` (Agent Sandbox without its CRDs is `not_installed`; a Celln
  receiver with invalid configuration is `transport_invalid`). A
  temporarily-unavailable error tells the caller to requeue, not to fail.
  Today Celln's `Capabilities` is hard-coded to `unknown`, and
  `ErrBackendTemporarilyUnavailable` is declared but never returned.
- **Logs** may be unsupported or terminal-only, and anything returned must be
  tenant-safe by contract.
- **No secrets in the input** (target). No key material crosses the seam,
  but `executionStartInput` still carries the Agent and the run, which name
  Secrets (`spec.model.authSecretRef`), because the Job backend renders its
  pod from them (the Job exception below). Celln ignores them; its platform
  decision already strips Secret names.

### Known exceptions and gaps (not fixed by the first change)

- **Job backend holds the key.** `buildContainers` injects the Agent's
  provider key from `spec.model.authSecretRef` into the agent container's env
  (one `SecretKeyRef` per allowed key) and the provider headers as
  `MODEL_PROVIDER_HEADERS`. So "no key inside the backend" is true for Celln
  and is a known exception for Job (and Agent Sandbox, which shares the pod
  template). Planned follow-up: route Job model traffic through a gateway
  (`internal/modelgateway`; draft #700 does this for harness runs with a
  per-run token), which is also what the backend-neutral #688 ceiling needs.
- **Gateway sequence lives inside the Celln dispatcher.** Reserve at the
  gateway, start, release, close happens inside `internal/cellnscoped`'s
  dispatcher today. The next step is to lift that sequence above the seam
  into the generic controller, where it becomes the hook for #688.
- **Second Celln release path.** `reconcileCompleted` still calls
  `cleanupScoped` directly, beside `Release` in `reconcileDelete`. Fold it
  into `Release` with the `Cancel`/`Release` split.
- **Backend immutability.** The "backend cannot change after the first
  reconcile" guard (`status.cellnOnly`) covers Celln only; it should become
  a per-backend record set once and checked for every backend.
- **Response gate parity.** The response gate (`agentrun_gate_verdict.go`)
  is skipped on the Celln scoped path.
- **One-shot budget mapping bug.** On the one-shot Celln path a gateway 429
  with `ReasonExhausted` is reported as `GatewayRegistrationUnconfirmed`
  (an uncertain outcome) instead of `AUTH_BUDGET_EXHAUSTED`. Turns map it
  correctly. Effect: a run that hits a ceiling is left `Uncertain` instead of
  refused. Separate fix.

### Deferred

- **Turns and enduring runs.** `AgentRunTurnReconciler` stays wired directly
  to `cellnscoped` and refuses non-Celln backends; Celln's enduring path
  (leased parents) stays where it is. Substrate's suspend/resume would be a
  different shape and is phase 1 of #692 at the earliest.
- **Server mode** (`reconcilePendingServer`, web-endpoint) stays a
  Deployment owned by the reconciler.
- **Agent Sandbox** gets wrapped in the same way in a follow-up; its paths
  (`reconcilePendingAgentSandbox`, `reconcileRunningAgentSandbox`) are
  already separate functions.

Substrate note (#699): its actors resume per turn and suspend between, so
turns map onto Substrate naturally. That is a reason to design turns into the
seam deliberately later, not to add them now; 0b proves the one-shot shape
first.

## Per-run attribution on shared workers

Job, Agent Sandbox and Celln give each run its own pod or cell, so device
energy and cost can be attributed by pod. Substrate multiplexes idle actors
onto warm workers, and an actor can resume on a different worker (0.42 s in
the phase 0a install record): one pod serves many runs over time. `RunIdentity` therefore
carries the run (namespace, name, UID) and the backend's workload handle
separately, and attribution must key on the run, using the backend's
per-actor identity and active intervals, not on the pod. This is a proposed exit
criterion for the Substrate spike (Ergoz needs per-actor active windows from
the backend).

## Backend-neutral spend ceiling (#688)

Plan only; not implemented in this change.

Today budgets live in two places: membrane token budgets checked in
`reconcilePending` (`checkTokenBudget`), and Celln's per-run and per-turn
budgets reserved at the model gateway. #688 asks for a per-namespace ceiling
on `CellnExecutionPolicy`, which would cap only Celln runs.

Proposal:

1. **Where it lives.** A namespace-scoped ceiling on `SympoziumPolicy` (or a
   small `SpendPolicy`), not on `CellnExecutionPolicy`: amount (tokens and,
   where priced, currency), period (rolling day, calendar month), and
   behaviour at the limit (refuse new runs, or also end running ones).
2. **Ledger.** A per-namespace, per-period ledger the controller owns,
   fed from what every backend already reports: `TokenUsage` on the terminal
   run, and the gateway's reservations for runs whose model traffic goes
   through it. Keyed by run UID so a retried reconcile never double-counts.
3. **Enforcement points.** (Prerequisites: Job model traffic routed through
   `internal/modelgateway`, and the gateway reserve → start → release →
   close sequence lifted out of the Celln dispatcher into the controller,
   above the seam.) Before `Start` for every backend (refuse with
   `OutcomeRefused` / `SPEND_CEILING`, no backend work done); and at the
   model gateway's reservation for traffic that passes through it, which
   covers Celln today and every backend once Job and Agent Sandbox route
   through the gateway.
4. **Surfacing.** Ceiling, spent and remaining on the policy status; a
   Kubernetes Event on the refused run (this also closes the second half of
   #688 for all backends, not only Celln).
5. **Celln compatibility.** Celln's own per-run and per-turn budgets stay as
   they are; the namespace ceiling sits above them.

Open question: runs on a backend that cannot report usage (some harness
adapters). They count as unmetered and follow the existing unmetered-run
policy from #349.
