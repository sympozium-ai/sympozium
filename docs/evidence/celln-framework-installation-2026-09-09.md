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

The latest UI/API compatibility and timeout fixes are source-tested; they still
need deployment/requalification. Initial installed images were private local
qualification builds, not published release artifacts.

## Remaining release gates

- Fresh installed browser creation, permission selection and refresh proof.
  Browser discovery returned no connected browser in this session. API tests
  are not a substitute for this gate.
- Rebuild/deploy the final revisions, qualify projected credential rotation and
  deliberate owner-loss handling, and run installed ordinary Kubernetes/OCI and
  Celln one-shot regressions.
- Review/merge paired PRs, publish digest-pinned installation images, and verify
  the documented installation from those published artifacts before tagging.

Private evidence is under the Sympozium worktree's
`target/framework-native/evidence/` (run/turn records and nine guest audits).
Guest audits contain conversation/tool data and are deliberately not committed.
Host audit retention is explicitly enabled via the private `authority/parent-audit`
directory; default production operation need not retain it.

The qualification TLS server certificate lasts 14 days, its CA 30 days from
2026-09-09. The units were started, not enabled across reboots. Rotate certificates
and deliberately provision production credentials before treating this as a
long-lived installation. Keep journals stable; never delete them to renew access.
