# Framework native installation qualification — 2026-09-09

This is installed-process evidence, not a release announcement or full UI E2E
pass. Source work is tracked in Sympozium #470 and Celln #99.

## Installed path

Framework (`192.168.1.237`, Linux `7.1.8-100.fc43.x86_64`) has five standalone,
operator-signed starter bundles admitted using real guest member checks.
Package identity:
`blake3:7114835971abdfbbdb83d800daa69783a55600f56fbf563d190bdc5650d0ad94`.

The new non-root native owner listens on loopback port 18787; a separate TLS
edge listens on 19443. The previous `celln-dispatcher.service` is untouched.
State is `/var/lib/sympozium-celln/starter`. The parent controller mounts that
dedicated tree, not KVM or model credentials. The general controller excludes
only `celln-agents`, keeping its existing namespace watches.

The installer created the real Agent/runtime/tool identities and all three
grant layers. Kubernetes model credentials were not created: qualification uses
the user-authorized DeepSeek credential in host-only `/etc/celln-native/model-token`.
The private publisher seed is not in Kubernetes or an image build context.
Owner authentication without a policy returned 503; after independent client
policy installation, unauthenticated parent requests returned 401 over verified
TLS. A package or grant alone is not execution readiness.

## Observed results

- YAML-created `celln-starter-9q84h`, UID
  `5da9a091-a616-4aab-8875-581c37fa5dec`, ran real DeepSeek turns.
- Separate, audited children invoked `workspace-write` and `workspace-read`:
  `notes.txt` became `amber-57` at revision 2, then a fresh tool read returned
  those bytes. Another audited child invoked `https-fetch` and returned the
  actual `example.com` HTML. Audits include executable/schema hashes and broker
  counters, not just the model's answer.
- The parent incarnation remained
  `blake3:d3df3c1650e0927429bfc1aea42ea8cf82b4cbae46f519603667a5693f6f4db4`
  across API and parent-controller restarts. Startup was not replayed.
- A later long-conversation turn failed; the run reported parent context lost
  or stopped and did not claim empty-state resumption. Its records remain for
  diagnosis. This observation alone does not prove the cause was context size.
- API-created `celln-agent-2rpjs`, UID
  `c067ffea-5a1c-4b22-8e1b-757346ce0b1e`, completed an initial write. Cancellation
  after dispatch committed `succeeded: false`, `Turn cancelled after child
  teardown.` A subsequent child read `proof.txt`, returning `cobalt`, revision 1.
- Normal API deletion of that second run completed and its finalizer cleared.
  The owner remained active with zero service restarts; replay journals remain.
- An earlier API attempt was refused before execution because its default
  five-minute timeout was shorter than the one-hour registered lease. That
  unexecuted test record was removed after inspection; its request was retained
  in private qualification evidence. No live authority was reset.

## Defects found and corrected

- Native catalogue selection incorrectly inherited OCI harness readiness at
  admission. Native selection is now separate, without fabricating OCI Ready.
- API RBAC lacked turn `watch` and `update`, causing stale history and failed
  cancellation persistence. Chart and incremental-upgrade roles now include
  both, with a render regression test; no turn-status write authority is added.
- Enduring API/UI timeout defaults now follow the requested lease. Explicit
  shorter timeouts are refused by the API.
- Incompatible SkillPacks are flagged before submission; API/admission reject
  SkillPacks and Agent MCP connections rather than silently discarding them.
- The glossary and OCI/native guides now state supported tools, permission
  boundaries and backend-specific persistence limits.

API/UI and webhook revision `8e96f31` was subsequently deployed successfully.
The API returned 400 before persisting an incompatible native request for the
existing `foo` Agent's SkillPacks. An enduring request with no explicit timeout
was persisted with `1h0m0s`, matching its lease.

The ordinary Kubernetes `foo-g2669` smoke run succeeded using its existing local
Qwen model and SkillPacks. Runner output was `SYMPOZIUM_STANDARD_OK`, with zero
tool calls, 6169 input tokens, 73 output tokens and 33831 ms model duration.
Its normal memory integration automatically stored a 170-byte test record;
this was not a test of Kubernetes administration tool effects.

The parent controller pod `celln-parent-controller-c99b4ff4f-z4v27` stayed on
the same UID with zero restarts after creation at 09:55:49 UTC. Its projected
credential directory was refreshed at 10:21:54 UTC; a new parent and status
writes succeeded after that refresh, without restarting the controller.

For deliberate owner loss, `celln-agent-tq7st` first completed a real-model
initial turn. Restarting only the new native owner caused `CellnParentReady`
to become false with reason `ContextLost` and message `Parent context
unavailable; no automatic reconstruction`. The original incarnation and initial
result remained recorded. The old dispatcher stayed active throughout.

These are private local qualification images, not published release artifacts.

## Automated regression results

- Sympozium `go test ./...` passed. Targeted API, webhook, installer and chart
  race tests passed. Web TypeScript checking and production bundling passed.
- Celln `make ci` passed, including warning-free clippy.
- Four explicit local KVM tests passed: signed closure admission, persistent
  parent launcher, declared substrate and dispatch outcomes (including hostile
  guest workspace/exec probes, stop/cancel and invalid-kernel refusal).
- The JSON harness grant-issuance KVM test initially lacked its required package.
  After generating that package with `celln-json-harness-proof --package-only`,
  it passed in 3.23 seconds with no model calls. This is not a billable one-shot
  model E2E or an installed framework router regression.

## Remaining release gates

- Fresh installed browser creation, permission selection and refresh proof.
  Browser discovery returned no connected browser in this session. API tests
  are not a substitute for this gate.
- Rebuild/deploy any final UI edits; installed OCI persistent-session and Celln
  one-shot router regressions remain, beyond the ordinary Kubernetes smoke and
  local KVM coverage above.
- Review/merge paired PRs, publish digest-pinned installation images, and verify
  the documented installation from those published artifacts before tagging.

Private evidence is under the Sympozium worktree's
`target/framework-native/evidence/` (run/turn records and nine guest audits).
Guest audits contain conversation/tool data and are deliberately not committed.
Host audit retention is explicitly enabled via the private `authority/parent-audit`
directory; default production operation need not retain it.

## Follow-up technical qualification

Celln revision `df59b2b` adds pre-launch host process identity and same-boot
process-exit confirmation, preserving the strict stop-response ABI. Installed
run `celln-agent-jnhq7`, UID `6f5e91c4-9903-4d63-aa3b-ad75c100c617`, completed
a DeepSeek turn remembering `opal`. Its parent incarnation was
`blake3:7c71e8690fa2fb9b9ffebc422eba17b6c77793e0e95889aea2c5f52295940552`.
After restarting only the new owner, the run reported `ContextLost`. Normal
API deletion returned 204 and Kubernetes confirmed deletion after the controller
received host teardown acknowledgement. No finalizer was manually removed and
no journal or authority was reset. Older qualification journals intentionally
remain fail-closed because they lack a pre-launch process identity.

The installed OCI Pi session suite passed against published digest
`sha256:8c8f0df071dc5307b3b557e0233a70757d8a4be17cb688c1ca725cb41001bbd5`
in `sympozium-harness-e2e`: real DeepSeek conversation across pod restart,
SSE content and `[DONE]`, explicit stop/resume with the same remembered token
and PVC, request audit, disconnect cancellation, idle timeout, actionable
failure status, and deletion of owned resources. Test Agents and credentials
were removed by the suite. The final proof used an SSH TCP tunnel directly to
the API Service: disconnect cancellation did not propagate correctly in the
earlier `kubectl port-forward` test path. The older runtime digest still bound
in `default` returned JSON instead of SSE; it was not silently overwritten.
The suite now rejects temporary non-JSON responses when waiting for a replacement
session endpoint and verifies the actual remembered token after resume.

The installed one-shot smoke did **not** pass. Its legacy v0.4.13 router has a
plaintext URL and predates the current client-authentication/ownership contract.
The updated controller refused before dispatch with `router requires HTTPS`.
This is an unresolved installation upgrade gate, not a guest execution failure.
The old dispatcher and router were not replaced and insecure HTTP was not enabled.

The qualification TLS server certificate lasts 14 days, its CA 30 days from
2026-09-09. The units were started, not enabled across reboots. Rotate certificates
and deliberately provision production credentials before treating this as a
long-lived installation. Keep journals stable; never delete them to renew access.
