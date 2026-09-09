# Exact-turn cancellation

Implementation note for enduring AgentRun epic #464 (overlay of #426).

An enduring harness parent retains its context while a disposable sub-cell
performs each turn. Cancelling a turn must not mean stopping that parent.

## Current local contract

- `AgentRunTurn.spec.cancelRequested: true` is a one-way request. Turn input,
  run name and run UID remain immutable. A recorded dispatch attempt is required.
- `POST /api/v1/runs/{name}/turns/{turn}/cancel` accepts only `runUID` and
  `turnUID` in its JSON body (with the existing namespace query). It verifies
  both identities and the active slot before persisting intent. A repeated
  request or already completed turn returns the existing record without dispatch.
- The conversation UI offers `Cancel turn` for an attempted turn whose UID
  matches the active slot. Confirmation explains that the parent is not stopped.
  The browser saves the exact turn UID before sending, disables network retries,
  and preserves the pending marker across reloads. Polling continues; an HTTP
  acknowledgement alone never enables another turn.
- The controller validates the original parent binding and active turn UID, then
  persists `status.cancelAttempted` before contacting the original owner.
- Controller submission uses `Prefer: respond-async` so the dispatcher returns
  pending immediately after admission. Waiting synchronously for the child
  would otherwise occupy reconciliation while cancellation arrives. Legacy
  synchronous clients retain their existing response behavior; pending still
  requires journal reconciliation and never authorizes replay.
- The transport sends an empty POST to
  `/v1/parents/{incarnation}/turns/{turnID}/cancel`. Authentication, fixed-origin
  TLS routing and exact child identity checks remain mandatory.
- HTTP 202 confirms a cancellation request only. It does not prove delivery to
  guest code, child teardown, result commitment or parent readiness.
- An uncertain send is not automatically repeated. The controller reads the
  original turn journal; only a durable committed result permits slot release.
  A completion racing cancellation can legitimately retain its successful result.
- Neither cancellation nor uncertainty refunds accepted-turn or host budgets.
  An old request must never select the next child.

## Verification and outstanding work

Local Celln HTTP routing tests cover two successive children, stale identity
refusal and retained parent capacity. Sympozium race tests cover a lost
cancellation acknowledgement, single-send recovery and release only after
result persistence. Kubernetes accepted the generated CRD in server dry-run.
The intercepted conversation browser suite passes 23 tests, including accepted
and uncertain cancellation responses surviving reload without resending.

On 2026-09-09 the explicit native test
`declared_parent_worker_pair_cancelled_model` passed without skipping (10.59s).
It completed a real DeepSeek/borrowed-uppercase turn, observed an owned curl
model transport on the next child, cancelled that exact child via authenticated
HTTP, checked subprocess exit and a parent-committed failed result, then used
the same parent for a fresh real model/tool turn retaining the original `violet`
context. The intervening cancelled message's `orange` value did not replace it.
Both successful children performed two model requests and one uppercase call;
the cancelled child recorded one request, one denial and zero response bytes.
Transport observation does not prove remote receipt or billing.

Evidence under the Celln validation worktree:
`target/declared-parent-pair-1952661-1788909212571835718/`, with the public
`cancelled-model-proof.json` and private worker audit files. Test log:
`target/parent-model-cancel-live.log`; full subsequent `make ci` passed in
`target/parent-model-cancel-ci.log`.

Initial-turn cancellation and full browser/API/controller/TLS live cancellation
qualification remain outstanding. Schema and controller changes have
not been deployed to the existing hands-on environment. This note is not a
release or epic-completion claim.

## Mixed-version controller finding

The 2026-09-09 full-process async regression attempts stopped before parent
launch: the browser-created run lost `executionLifecycle`, `cellnSelection`
and `enduring`, while retaining its persona. The active shared
`harness-proof-controller` image is `localhost/sympozium-celln-controller:b331a29`.
That revision lacks these fields and uses a whole-object update to add its
finalizer. This is consistent with the observed field loss; the exact writer
has not yet been proven from an API audit record. Agent setup also hit a real
resource-version conflict; its fixture now rereads/retries conflicts with a UID
guard instead of overwriting concurrent state.

A local generated-CRD transition rule now rejects stripping an existing enduring
run's execution boundary. A test uses Kubernetes' CEL validator against the
generated schema, covering unchanged intent and five invalid transitions.
This is a fail-closed safeguard, not mixed-version compatibility. The shared
controller has not been upgraded or stopped; choosing an upgrade or separate
test cluster remains pending. The new AgentRun boundary rule is not installed.

Update after explicit user approval to update the environment: the shared
deployment was upgraded to
`localhost/sympozium-celln-controller:enduring-20260909` (local image ID
`10dba30d98cda7275cb4ad9490ab94703d6b874947f6ffe7d9a50a3c7e987aa3`),
with `--watch-namespace=sympozium-system`. Existing shared runs were confined
to that namespace; the hands-on parent has a separate scoped controller.
The new AgentRun boundary CRD was applied. Rollout completed with one ready
pod and zero restarts. Pre-change deployment YAML is saved locally at
`/tmp/celln-controller-upgrade.4P8QZi/deployment-before.yaml` for rollback.
Namespace watch scoping is not an RBAC boundary.

The upgraded environment then passed the full-process regression: Go driver
63.09s, outer Rust proof 64.19s, no skip. It covered actual browser creation and
follow-up, authenticated API, scoped parent controller, TLS proxy, real
DeepSeek/borrowed-tool work, browser deletion, a second parent from the unchanged
template, joined teardown and owner restart checks. Evidence:
`target/declared-parent-pair-1982572-1788933934328004087/` and
`target/parent-upgraded-environment-live.log` in the Celln validation worktree.
The isolated namespace and all four explicitly owned API/controller/proxy/
dispatcher processes were absent after completion. The shared controller stayed
ready with zero restarts. This run did not yet exercise the live Cancel turn UI.

Follow-up: the shared controller later restarted on its two-minute cache-sync
deadline. Logs identified missing `WorkspaceSession` list/watch permission for
the existing service account. Initial pod Ready was therefore insufficient
upgrade evidence. Applied the namespace-only supplement
`config/samples/celln-shared-controller-workspace-rbac.yaml` and restarted the
deployment. This supplies the current WorkspaceSession controller's declared
resource/status/finalizer permissions within `sympozium-system`; it does not
grant any workspace capability to native guest tools.

## Full live UI cancellation acceptance

Passed on 2026-09-09: the real browser submitted an `orange` turn, requested its
cancellation using the UI, observed `Turn cancelled after child teardown.`, and
sent another turn that returned `VIOLET` from the original context. The driver
independently checked exactly two follow-up records, a persisted cancellation
attempt, committed failed and successful results, and an empty active slot.
Browser refresh retained the results. The outer native proof joined all parents
and checked restart/capacity behavior. No responses were intercepted.

Go driver: 90.17s; outer Rust proof: 91.32s; no skips. Evidence:
`target/declared-parent-pair-2008500-1788935997572494712/browser-cancel-proof.json`
and `target/parent-full-browser-cancel-live.log` in the Celln validation worktree.
The test namespace `celln-parent-manager-j2qv7` was absent after completion.
The repaired shared controller remained ready with zero restarts after 6m36s.

Reproduce using the existing all-process borrowed-tool proof settings plus
`CELLN_INTEROP_BROWSER_CANCEL=true`, `CELLN_INTEROP_HOLD_SECONDS=60` and
`CELLN_INTEROP_BROWSER_DELETE=true`. In cancellation mode the third hands-on
parent is used for acceptance and immediately cleaned up, not held for users.
