# Celln Harness starter toolbox — release requirements

Parent epic: https://github.com/sympozium-ai/sympozium/issues/464

## What exists

The run form selects a Harness/AgentRuntime, Celln backend and optional enduring
lifecycle. Its borrowed catalogue tools are explicit namespace-local
`CellnTool` name/revision references in `spec.cellnSelection.toolRefs`.
Operator, runtime and agent grants independently approve the selected artifacts.
The native KVM/DeepSeek proof now executes workspace write, cross-turn read and
bounded HTTPS fetch. Catalogue limits retain `workspace: none` and empty legacy
egress: optional `artifacts` and `https` broker limits describe these distinct
capabilities without authorizing host mounts or guest sockets. Every operator,
runtime, agent and run-selection grant must explicitly include the capability;
quotas and exact HTTPS destinations are intersected. The disposable one-shot
composition path rejects these parent-owned broker tools.

These type/admission changes are not an installed starter catalogue or proof of
UI integration. General curl/Python support is not implied.

## User-facing selection

After choosing Harness + Celln, show a Tools section with an operator-installed
“Starter tools” preset, individual capability toggles, descriptions and effective
permission limits. Preselect only installed, compatible, approved members of the
preset; clearly list them before Run is clicked. Missing approvals must disable
the tool and explain why. Never infer approval from a familiar tool name or label.

The preset expands to explicit revision-pinned tool references in the saved run
and exported YAML. No new preset CRD is required for the first release. Agent
defaults may populate the form, but the resulting run must record its selection.
Changing the preset must not change an already running parent's tools. Adding
authority to an existing parent is outside this release; create a new run.

The enduring-run form now implements an explicit “Use approved starter tools”
action for installed `workspace-read`, `workspace-write` and `https-fetch`
revisions. It performs live grant previews before selecting preset members and
explains missing, incompatible or unapproved entries. The effective-permission
preview displays intersected file quotas and HTTPS destinations/budgets.
Individual catalogue selection remains explicit and execution still revalidates
all authority; the preview is not admission or proof of host readiness.

`config/samples/celln-native-starter-run.yaml` shows equivalent revision-pinned
YAML intent. The Agent/runtime names and limits must match operator installation;
the sample does not install tools or grant permissions.

UI contract validation: `celln-selection.cy.ts`, 14 passing tests on 2026-09-09,
including partial starter approval, unavailable defaults and enduring intent.
These used intercepted APIs, not the integrated real-model release suite.

| Capability | Default policy | Required execution contract |
| --- | --- | --- |
| Read workspace | Starter preset | Run-owned root only; bounded reads; deny traversal, symlink escapes and host paths |
| Write workspace | Starter preset | Same root; bounded quota and writes; cannot alter signed tool code |
| HTTP fetch | Starter preset when destinations are approved | Host-brokered HTTPS; explicit destination allowlist, bounded time/body, no arbitrary credentials, private-address or redirect bypass |
| Python | Deferred beyond this release candidate | Requires a separately qualified interpreter/agent-code boundary; no selectable toggle in this release |

“HTTP fetch” is the first capability, not an unrestricted shell/curl command.
Mutating HTTP methods and user-secret delegation require separate approval.
Python must not turn agent-authored code into trusted tool-lane execution.

## Workspace semantics must be real

Disposable turn cells do not imply a persistent filesystem. Define and implement
a run-owned artifact/workspace store with explicit read/write mediation before
advertising cross-turn files. Do not mount the host project or give a child an
unrestricted shared volume. Keep artifact retention separate from live Harness
context and document cleanup/quotas. A later turn must demonstrably read the file
written by an earlier turn, without obtaining another run's files.

## Delivery order and acceptance

- [ ] Implement bounded workspace read/write and guest tests for isolation and
  sealed-code protection; prove cross-turn file continuity.
- [ ] Implement host-brokered HTTP fetch, including blocked destinations,
  redirects and quota tests; do not add a guest network stack.
- [ ] Package signed/revision-pinned starter tools and matching operator/runtime/
  agent grants in the installation. Remove manual hash/config editing from the
  ordinary user's selection workflow.
- [ ] Add preset/individual selection UX and YAML parity, permission preview,
  unavailable-tool explanations and saved selection display.
- [ ] Run one integrated real-model scenario that writes an artifact, reads it
  on the next turn, uses an allowed HTTP destination, and refuses an unapproved
  capability without changing the parent grant or silently falling back.

No release claim that defaults exist until these artifacts and end-to-end tests
are present. Pause/resume and checkpoint work are deferred to fund this usability
work; core isolation, authority and teardown checks are not deferred.

The release-candidate scope also defers Python, arbitrary harness compatibility
and performance tuning. These are not remaining gates for this release.
