# Native Celln starter system acceptance — 2026-09-09

Status: combined real-model E2E **passed**, not yet a merged release or a
long-running environment handoff.

The Rust entrypoint `native_parent_starter_cross_turn_live`, with
`CELLN_INTEROP_STARTER=true`, ran the actual Go controller driver
`TestLiveCellnParentClient`. It used the local `kind-celln-deployed` cluster,
real KVM parent/worker cells, DeepSeek, separate authenticated API/web and
dispatcher processes, TLS proxy and namespace-scoped controller. Browser
actions were not intercepted.

The suite passed in 108.77 seconds (Go driver 107.51 seconds), no skips.

- The UI selected the native runtime, enduring lifecycle and all three
  revision-pinned starter tools via successful live permission previews.
- A disposable child wrote `violet` to `notes.txt`, revision 1; another child
  read the actual stored content. Assertions inspected guest tool events.
- The UI refreshed and retained the conversation and exhausted-turn state.
- A mismatched deletion UID was refused; original-run deletion finalized.
- The unchanged operator template provisioned a distinct second parent with
  fresh file storage, writes, reads and accounting.
- A third parent fetched `https://example.com/` through its allowlisted broker,
  read `notes.txt`, cancelled a later turn, then read `violet` again in a fresh
  child. The cancellation committed `succeeded: false` and the message
  `Turn cancelled after child teardown.`; the active-turn slot cleared.
- The dispatcher confirmed teardown for all three parent incarnations,
  reported zero live cells and restored its full logical memory reservation.
  Replay was refused. After dispatcher restart, those incarnations reported
  historical `ContextLost` rather than live owners or reconstructed context.

Local private evidence root (conversation/tool data, not replay authority):
`/home/axjns/Code/celln-worktrees/merged-epic-validation/target/starter-live-2066148-1788940479379564452/`.
`browser-cancel-proof.json` contains the third run and four committed follow-up
turn records; `parent-audit/worker-proof-*.json` contains actual guest events.

Post-test read-only checks confirmed namespace `celln-parent-manager-h596k`
was absent, and dispatcher/API/proxy/controller PIDs 2066479, 2066542, 2067096,
2067119 were gone. Shared controller
`harness-proof-controller-58865b456d-zx8xs` remained Ready with zero restarts.

Scope limits: workspace data is retained by the live host parent owner, not
crash-durable storage; cancellation does not roll back already completed
effects. This proof does not claim arbitrary harness compatibility, Python,
checkpoints, pause/resume, production multi-tenant API RBAC, or performance SLOs.
