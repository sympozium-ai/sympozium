# Celln fleet on multi-node Kind — 2026-09-14

Local evidence for #530 (fleet) on a fresh three-node Kind cluster
(`fleet-control-plane`, `fleet-worker`, `fleet-worker2`; kernel
`7.1.13-200.fc44.x86_64`, real `/dev/kvm`). This is a development trial, not a
release claim: images were built from the working tree and the model
credential was the operator's own DeepSeek key.

## What ran

- Celln trial build: `main` (0.5.10) plus celln#109 (`POST /v1/parents/provision`,
  affinity at provisioning) and celln#110 (live `--backends-srv` refresh),
  static musl binaries, image `celln:fleet-trial`.
- Sympozium: this branch's controller/apiserver/webhook/celln-installer images
  tagged `fleet-trial`; CLI built from the branch.
- Starter package: `celln starter-package` with the host kernel and a
  throwaway operator seed; `celln starter-inspect` reported
  `blake3:c75a6a4b976f5d7b26748b32b19fe2033577dd7ac88e948aa8dc87c12d4fc1bb`,
  publisher `a39e74b9…10363`; pushed as a `FROM scratch` image to a registry on
  the Kind network and referenced by digest
  `sha256:69c7858565a13a19a64c379a5b2472d0e0984e1eddcd6be3e2d2c667082ce6d6`.
- One command: `sympozium install -n celln-agents --celln-fleet …` (see the
  guide). Workers were labeled `celln.dev/kvm=true`; no other per-node action.

## Observed

| Step | Result |
| --- | --- |
| Node preparation | Both `celln-node` init steps pulled the package by digest, verified the hash, admitted all five bundles with real guest member checks (`"admitted":true`), configured the model profile, and `fleet-worker` published `celln-fleet-configuration`; `fleet-worker2` verified identical files. |
| Plane | Two dispatchers Running; router started with zero owners, then discovered both through `celln-node.celln-system.svc.cluster.local` (capability report listed two nodes). Controller: no `kubernetes.io/hostname` selector, no hostPath, `CELLN_PARENT_CONFIG=/var/lib/sympozium/celln-parent/approvals` on the `celln-parent-journal` claim, `remoteProvisioner` targeting the router. |
| Run 1 `celln-starter-bb8tg` | Issued through the gateway (binding target = router), incarnation `blake3:e67a91f6…8644`, launch profile `blake3:d781c5b6…18e9` present only on `fleet-worker`; parent Ready; initial turn (real DeepSeek + `workspace-write` in child `blake3:ba8e0655…f1a5`) answered *Done — wrote "violet" to notes.txt (now at revision 1)*. |
| Run 2 `celln-starter-krhfh` | Incarnation `blake3:0842e2c9…9718`, owner `fleet-worker2`; initial turn succeeded. Owners are spread across the fleet. |
| Follow-up turn | `AgentRunTurn` with the run's controller ownerReference: distinct child `blake3:c70666f5…f634` on the same incarnation answered *Content: violet / Revision: 1*; `acceptedTurns=1`, slot released. |
| Node leave | `kubectl label node fleet-worker2 celln.dev/kvm-` drained the owner (preStop `/v1/drain`); run 2 failed with *Celln parent context lost or stopped … owner=Stopped reachedReady=true admittedAge=3m29s … no automatic reconstruction* and `status.cellnParent.ownerOutcome`; run 1 on `fleet-worker` stayed Running/Ready. |

## Defects found and fixed on the way

- `main` held every `enduring` catalogue selection for the tenancy scoped
  receiver even when only the prepared native parent path was configured, so
  no native parent could start without `CELLN_SCOPED_CONFIG`. Fixed: a scoped
  receiver still owns all enduring selections and shared intent never reaches
  legacy issuance; a legacy selection reaches the prepared parent path.
- The unified controller did not register the `AgentRunTurn` reconciler on the
  native parent path (only the retired parent-only binary did), so follow-up
  turns were never reconciled. Fixed.
- `sympozium install --celln-fleet` published trust before the chart created
  `celln-system`, and bound the catalogue before the controller rollout
  finished. Both reordered.
- Once a drained owner's address left the fleet, the gateway answered
  `503 original parent backend removed` and the controller parked the run as
  "uncertain" indefinitely (and could never confirm its cleanup). That refusal
  is now a distinct client error mapped to `ContextLost` on startup/status and
  to an established teardown on stop, so the run fails honestly and deletes.

## Placement is by hash, and a refused create is terminal

A second trial on a fresh `fleet-ci` cluster created two runs back to back
with the default 2 GiB node budget. Both incarnations hashed to the same owner
(`fleet-ci-worker2` held both launch profiles); the first parent came up Ready
and the owner refused the second create for capacity — each parent reserves
one egress slot and the fleet defaulted to `egressSlots: 1`, and two 1.25 GiB
parents also exceed a 2 GiB budget — so that run reported *Parent outcome
unavailable; preserving original incarnation without replay* and the gateway's
status read reached the owner (`parent owner not found`). The gateway does not
re-place a refused incarnation, by design. Raising the budget did not help:
`current_node` in the dispatcher zeroes advertised egress whenever any parent
is live ("parent registry does not yet carry exact broker-slot charges"), so
Celln v0.5.11 holds exactly one parent per node. The integration script now
accepts a same-owner refusal as the honest outcome, and per-parent broker
accounting is filed against Celln. Load-aware placement is a possible later
gateway improvement.

## Any authorised namespace (P0, PR #536)

A third trial on a fresh `fleet-ci` cluster ran the same install with the
controller in **platform admission mode**: no grant ConfigMaps, no copied
tools, one `CellnRuntimeProfile` (with native material), three
`ClusterCellnTool`s and one `CellnExecutionPolicy` for the scope, and the
install namespace labeled `celln.sympozium.ai/scope=trial`.

| Step | Result |
| --- | --- |
| Install namespace | `celln-starter-jp65f` resolved through policy, provisioned through the gateway on `fleet-ci-worker2`, answered the initial DeepSeek turn; `celln-starter-2bq6v` hashed to the other owner `fleet-ci-worker` and ran too (an earlier attempt of this trial saw the second run hash to the occupied node and be refused for capacity, not re-placed). |
| Follow-up turn | Distinct child on the same incarnation read back *Content: violet / Revision: 1*. |
| Release | Both live parents were deleted through the gateway; the fleet was free again. |
| Second namespace `celln-agents-b` | Operator labeled the namespace; tenant applied only the three wrapper objects copied from the install namespace. `celln-starter-5djrt` was admitted on `fleet-ci-worker` and answered its initial turn. No grant ConfigMaps, no namespaced tools. |
| Unlabeled namespace `celln-agents-denied` | Same wrapper objects, no label: the run was held with `CellnParentReady=AdmissionPending: Platform policy refused admission (AUTH_POLICY_WITHDRAWN)` and no parent was issued. |
| Node leave | Unlabeling `fleet-ci-worker` drained its owner; `celln-starter-5djrt` reported *context lost or stopped* and deleted cleanly. |

### Defects found and fixed on the way

- The platform resolver treated the controller's own persisted route
  (`spec.model.provider/protocol/baseURL/credentialProfile`) as an inline
  override and refused every run with `AUTH_ROUTE_MISMATCH`; then it compared
  the run's pinned connection revision against a digest computed differently
  from the one the controller pins. Mirrored values are accepted and one
  `modelconnection.Revision` serves both sides.
- The provision plan clamped the per-turn output allowance to the run total
  divided by turns (1024), below the owner's model profile (1536), so the
  owner refused every plan with an opaque `409 parent provisioning refused`.
  The resolver's turn cap now follows a native profile's per-turn allowance
  bounded by the run total; a run whose budget affords less than one turn is
  refused with `AUTH_LIMIT_RANGE` before anything is pinned.
- A failed issuance could never be retried: each retry resolved under a new
  clock, pinned a different choice and failed with "already assigned
  differently". The choice record now carries the frozen resolution; retries
  revalidate it and re-send byte-identical plans, so the gateway's owner
  affinity and the owner's ledger see one plan per incarnation.
- Owner refusals were reduced to "remote parent issuer failed" before reaching
  the log; the status and bounded error text are now kept.
- The guide's tenant wrapper YAML omitted `spec.image` and `spec.agents`,
  which the CRDs require even for profile wrappers.

Deleting a capacity-refused run looped on "parent or turn not found"; fixed
in PR #538 (#534).

## Zero-ceremony namespaces and node-sized capacity (P1a/P1b, PR #538)

A fourth trial on a fresh `fleet-ci` cluster used a Celln build of
sympozium-ai/celln#113 (per-parent broker charging, `d52d55a`) in the
installer image, the default-open policy and `celln.fleet.capacity: auto`.

| Step | Result |
| --- | --- |
| Capacity | Each dispatcher logged `celln capacity: node=66863595520 memory=50147696625 cells=74 egressSlots=74` (the Kind workers see the 64 GiB host). |
| Two parents, one node | `celln-starter-cq7kj` went Ready on `fleet-ci-worker` and answered its DeepSeek turn; the next run, `celln-starter-xtl7p`, hashed to the same owner, went Ready beside it and answered its own turn while the first stayed Ready. With Celln v0.5.11 the second was refused. |
| Follow-up turn | Distinct child read back *Content: violet / Revision: 1*. |
| Delete | Both co-located parents deleted through the gateway. |
| Unlabeled tenant namespace `celln-agents-b` | No label and no YAML: `GET /api/v1/celln-platform/profiles` offered `celln-native-trial`, `POST /api/v1/celln-platform/wrappers` created the three wrappers, and `celln-starter-q8mmp` ran on `fleet-ci-worker`. |
| Excluded namespace `celln-agents-denied` | Labeled `celln.sympozium.ai/excluded=true`: offered no profiles, wrapper creation answered 403, and a run (with hand-applied wrappers) was refused `AUTH_POLICY_WITHDRAWN` with no parent. |
| Node leave | Unlabeling `fleet-ci-worker` reported ContextLost on `celln-starter-q8mmp`, which deleted cleanly. |

A run 14 on Celln v0.5.11 passed the same namespace steps. Terminal
`CreateRefused` outcomes are covered by unit tests; with node-sized capacity
the journey no longer hits a capacity refusal.

## llama-server backend on the framework machine

A fifth trial ran the same journey with the scope's model backend set to a
llama-server on another machine reached over Tailscale
(`--celln-fleet-model-provider llama-server --celln-fleet-model
Qwen3.8-27B-UD-Q4_K_XL.gguf --celln-fleet-model-endpoint
http://100.81.163.75:8080/v1/chat/completions
--celln-fleet-model-allow-insecure`, no credential file). The Celln build
included sympozium-ai/celln#116 (model requests bounded by the turn deadline
instead of 45 seconds); llama-server served about 11 tokens/s.

| Step | Result |
| --- | --- |
| Install | Nodes configured the llama-server model profile; the policy route was stamped `auth: host-profile`, origin `http://100.81.163.75:8080`, `allowInsecure: true`; a placeholder credential Secret was published. |
| First parent | `celln-starter-ntjgc` (connection provider `llama-server`) answered *Done. Wrote "violet" to notes.txt; the file is now at revision 1.* |
| Two parents, one node | `celln-starter-27vh7` joined it on `fleet-ci-worker2` and answered its own turn. |
| Follow-up turn | Distinct child read back *violet (revision 1)*. |
| Tenant and excluded namespaces | `celln-starter-l484n` ran in unlabeled `celln-agents-b` with wrappers created on first use (a llama-server connection); `celln-agents-denied` was refused `AUTH_POLICY_WITHDRAWN`. |
| Node leave | ContextLost on `celln-starter-l484n`, clean delete. |

The first attempt failed at install: the `CellnExecutionPolicy` CRD rule
allowed plain HTTP only for no-auth loopback routes. Policy routes now carry
an explicit `allowInsecure` for host-profile credentials. OpenAI and Anthropic
presets are covered by unit tests; no live keys were available for this trial.

## Anthropic protocol and two backends side by side

A sixth trial ran the full journey with the llama-server backend over the
Anthropic Messages protocol (`--celln-fleet-model-protocol
anthropic-messages`, endpoint `http://100.81.163.75:8080/v1/messages`), with
sympozium-ai/celln#118 in the dispatcher. llama-server returns `thinking`
content blocks for reasoning models, which the broker used to refuse; they
are now dropped. Every step passed: first turn *Done. "violet" written to
notes.txt (now revision 1)*, two parents on one node, follow-up *violet
revision: 1*, deletes, label-free tenant namespace, excluded namespace,
node loss.

A second cluster, `fleet-ds`, ran the DeepSeek backend at the same time. Each
fleet then got a brand-new namespace, prepared only through
`/api/v1/celln-platform/wrappers`, and one enduring run asked *"Where is
Botswana? Answer in two sentences without using any tools."*:

| Namespace | Backend | Answer |
| --- | --- | --- |
| `botswana-llama` (`fleet-ci`) | llama-server, Qwen3.8-27B, `anthropic-messages` | *Botswana is a landlocked country located in southern Africa. It is bordered by South Africa, Namibia, Zimbabwe, and Zambia.* |
| `botswana-deepseek` (`fleet-ds`) | DeepSeek, `deepseek-chat`, `openai-chat` | *Botswana is a landlocked country in Southern Africa, bordered by South Africa to the south, Namibia to the west and north, Zimbabwe to the northeast, and Zambia to the north. Its capital is Gaborone, located in the country's southeastern corner near the South African border.* |

Two clusters were used because a fleet scope has one model backend; several
backends in one cluster is #535.

Running two installs on one host exposed installer defects, all fixed: Helm
ignored `$KUBECONFIG` and installed into whichever cluster `~/.kube/config`
selected; CRDs were not awaited as established; cert-manager readiness was
skipped when it was already present; GitHub 504s on release manifests aborted
the install (now retried, and the journey can use a cached cert-manager
manifest); and the journey leaked its API port-forward.

## Day-long ceilings and capacity-aware placement (Celln v0.5.16)

Two more journeys ran on the **released** Celln v0.5.16 bundle (host limits,
celln#120; capacity-aware placement, celln#122) with the installer defaults:
DeepSeek on `fleet-ds` and llama-server on `fleet-ci`.

| Check | Result |
| --- | --- |
| Ceilings | Policy `maxParentLeaseSeconds 86400, maxTurns 256, maxModelRequests 768, maxOutputTokens 393216`; the node's reviewed parent request carries `timeoutMs 86400000`; the sample conversation asks for `14400 s / 64 turns / 192 / 98304`. |
| Placement | Both fleets: the first parent landed on one owner, the second on the emptier owner, the third tied and co-located with the first (`fleet-ds`: `gc5sk` → worker, `8pdfs` → worker2, `9kt44` → worker; `fleet-ci`: `69696` → worker, `dkt5s` → worker2, `v9f7w` → worker). |
| Everything else | Real turns, follow-up context, deletes, two API conversations of one Agent in a label-free namespace, excluded namespace refused, node leave → ContextLost, clean delete — all passed on both fleets. |

The first DeepSeek attempt failed before any run: its generated NATS
password began with a digit, `nats.conf` references it as a bare variable,
NATS crash-looped and the controller never became ready (about one install
in six). Fixed in the chart in PR #542.

## Several model backends per scope (#535)

One scope, two backends, every node configures both, one namespace runs
parents on either. On `fleet-ci` (two KVM workers) the scope `ci` was
installed with `--celln-fleet-backend` twice against the framework machine's
llama-server: `native` over `openai-chat` (`/v1/chat/completions`) and
`messages` over `anthropic-messages` (`/v1/messages`), same model, same
origin, different protocol.

| Check | Result |
| --- | --- |
| Node preparation | Each node ran `starter-configure` twice from the one admitted package and published `native.*` and `messages.*` keys in `celln-fleet-configuration`; the second node verified. |
| Catalogue | Profiles `celln-native-ci` (backend `native`, protocol `openai-chat`, credential `ci`) and `celln-native-ci-messages` (backend `messages`, protocol `anthropic-messages`, credential `ci-messages`); one policy `celln-fleet-ci` with both profiles and both routes. |
| Credentials | Secrets `celln-fleet-model-credential` and `celln-fleet-model-credential-messages`, each mounted read-only into every dispatcher at its own directory (`/etc/celln-native`, `/etc/celln-native/messages`); the prepare step mounts neither. |
| Tenant | Label-free `celln-agents-b` was offered both profiles by the API and got wrappers `celln-native`/`celln-agent` and `celln-messages`/`celln-agent-messages`, each connection bound to the route of its own protocol. |
| Runs | Three conversations at once in that namespace: two of `celln-agent` on `native`, one of `celln-agent-messages` on `messages`. The `messages` parent answered "Botswana is a landlocked country in southern Africa, bordered by South Africa, Namibia, Zimbabwe, and Zambia." |
| Everything else | Real turns, follow-up context, gateway deletes, excluded namespace refused, node leave → ContextLost, clean delete — all passed. |

A first attempt paired DeepSeek (`native`) with llama-server (`local`). The
install was correct (two profiles, two Secrets, prefixed ConfigMap keys,
both wrappers) but DeepSeek was returning `503 Service is too busy` and then
hanging for 60 s at the time; its parents lost context after the 60 s turn
deadline while the llama-server parent answered in seconds. The pair with
two llama-server protocols was run instead. Worth noting: a provider outage
surfaces as `ContextLost` rather than a failed turn, which is a Celln
owner behaviour to look at separately.

## One-shot runs on the fleet (#490)

A one-shot on the shared catalogue is a single-turn native parent: the same
admission, backend choice and owner protocol as an enduring run, finished
with its answer and its parent stopped. On `fleet-ci` (scope `ci`, backends
`native` and `messages` on the framework machine's llama-server), the
tenant namespace `celln-agents-b` ran two one-shots through the API while
three enduring conversations were live:

| Run | Backend | Result |
| --- | --- | --- |
| `celln-agent-bq8jx` | `native` (openai-chat) | Succeeded: "Botswana is a landlocked country in southern Africa, bordered by South Africa, Namibia, Zambia, and Zimbabwe." |
| `celln-agent-messages-pmfmw` | `messages` (anthropic-messages) | Succeeded: "The capital of Botswana is Gaborone." |

Both carried a parent binding (`status.cellnParent.binding.incarnation`),
finished with `status.result`, and after they completed the owners' live
cells returned to the 6 held by the three enduring parents. No namespace
grants, no scoped receiver, no separate one-shot stack: the parent path
serves both lifecycles.

## A backend added to a running scope (no owner restart)

Node preparation now lives in a `celln-node-configure` DaemonSet; the
`celln-node` owner only waits for the node's package admission, and every
backend's key is one entry in the `celln-fleet-model-credentials` Secret
mounted whole. On `fleet-ci` (scope `ci`, backends `native` and `messages`)
the same install command was rerun with a third backend, `spare` (custom
provider `llama-spare`, openai-chat, the framework machine's llama-server),
while three enduring conversations were live in `celln-agents-b`.

| Check | Result |
| --- | --- |
| Owners | Both `celln-node` pod UIDs unchanged across the rerun; the conversation `celln-agent-wgmnz` still Running afterwards. |
| Nodes | Both configure pods rolled, configured `spare` from the already-admitted package, waited for its key file, and merge-patched `spare.*` into `celln-fleet-configuration`; existing keys untouched. |
| Catalogue | Profile `celln-native-ci-spare` (credential profile `ci-spare`) added; policy `celln-fleet-ci` grew to three runtime profiles and three routes; `installed.json` rewritten, `run.json` kept. |
| Tenant | `celln-agents-b` offered the new profile through the API and got wrappers `celln-spare`/`celln-agent-spare` on first use. |
| Run | One-shot `celln-agent-spare-69sfb` on `spare` succeeded: "The Nossob River flows through Botswana." |

Two earlier attempts found real gaps. A custom provider needs a key file
(the journey now supplies one). And a run issued 34 s after the key was
published reached its owner 5 s before the kubelet had refreshed that
owner's Secret mount, so the parent was lost on "provider credential
unavailable". Nodes now publish a backend only once its key file is present
in their own mount of the Secret, and after a rerun that added backends the
installer waits 90 s for the running owners' mounts to follow.

## A failed child is a failed turn, not lost context (Celln v0.5.17)

Celln v0.5.17 (celln#125, closing celln#124) commits a child failure as a
failed turn with a readable reason and keeps the parent. On `fleet-ci`:

| Case | Before | Now |
| --- | --- | --- |
| Local model made a second tool call against the package's one-call budget (a real intermittent case in the journey) | parent lost after the child died; run failed as `ContextLost` | enduring run stays Running, parent Ready, `initialTurn.result.succeeded=false` with "Turn failed; no result committed: child refused: guest exited with code 1: … CELLN_HARNESS_ERROR tool call budget exhausted" |
| Backend key replaced with an unusable value (probe on `messages`) | same `ContextLost` | one-shot `broken-one-shot-cs9gj` Failed with "Celln one-shot turn failed: Turn failed; no result committed: child refused: guest exited with code 1: CELLN_HARNESS_ERROR lent executable failed"; enduring `broken-enduring-5r56p` Running with parent Ready and no owner outcome |

The journey now reports a committed failed turn immediately with its reason
instead of waiting for a success, and the installer's sample task asks for
exactly one tool call so the local model's optional second call does not
make the smoke run flaky.

The installer also probes every backend before touching the cluster (PR
#548): on this run both backends answered a one-token request on their own
protocol before the install began.

## Picking a backend and answering once from the UI

The Agent page's Harness tab now carries a **Model backend** picker (every
backend the namespace's fleet offers, with provider, model and protocol),
lifecycle cards that explain one-shot versus enduring, wrapper-aware runtime
labels, and an **Answer once** box on the conversation panel. Proven with
`web/cypress/e2e/agent-backend-picker.cy.ts` against `fleet-ci` through the
Vite dev server and the live API:

| Step | Result |
| --- | --- |
| Agent created on `native`; picker shows "Runs on …"; `messages` chosen | wrappers ensured, Agent rebound: runtime `celln-messages`, connection `celln-messages`, model and the policy's three shared tools |
| Lifecycle cards | "single-turn parent" and "follow-up turns" explanations present |
| "Ask once" with "Where is Botswana?" | one-shot run on `celln-messages`, no `enduring` block, Succeeded in about 24 s with an answer naming Botswana |

Found on the way: a run created from the UI carried no `systemPrompt`, and
the provision plan refuses a persona that differs from the profile's, so the
run sat in admission pending. The API now fills a fleet profile's persona in
when a client omits it, and the plan's persona/route/tool-call mismatches
are platform refusals (`AUTH_POLICY_CONTRACTED`) rather than generic
pending states.

## Borrowed commands from pinned images (Celln toolbox)

The fleet's toolbox now comes from container images pinned by digest.
`celln starter-package --tool-image busybox --tool-image jq` extracted each
command's static executable from the pinned image, lent it inside the
signed worker closure under its own alias, and recorded the image as the
tool's source; `starter-configure` turned them into `celln.argv/v1` tools.
On `fleet-ci` (scope `ci`, two llama-server backends):

| Check | Result |
| --- | --- |
| Catalogue | 16 cluster tools: 3 brokered (`workspace-read`, `workspace-write`, `https-fetch`) and 13 `celln.argv/v1` commands, 12 naming `docker.io/library/busybox@sha256:fc6ddd…` (grep, sed, awk, sort, uniq, wc, cut, head, tail, base64, sha256sum, date) and `jq` naming `ghcr.io/jqlang/jq@sha256:4f34c6…`. |
| Worker | Parents with the 16-tool template started, completed real turns, held live context across a follow-up turn and were deleted through the gateway, as before. |
| jq | One-shot `celln-agent-hnpsd`: "call the jq tool with filter .capital, raw output, on {"capital":"Gaborone",…}" → answered `Gaborone`. |
| grep | One-shot `celln-agent-8j4lk`: "call the grep tool with pattern ^vio on red, violet, blue" → answered `violet`. |
| Cells | The one-shots released their cells: live cells back to the 6 held by the three enduring parents. |
| Add backend | With the file-based bodies, a third backend (`spare`) joined the running scope: configure pods published it with zero restarts, owners untouched, the enduring run kept going, and a one-shot on `spare` answered. Full journey: 26 checks passed, exit 0. |

Three things the runs found and that are fixed in the same change set: the
tool schema validator accepts a strict subset (no per-field descriptions,
strings at most 4096 characters), so command schemas are generated within
it and parameter notes are folded into the tool description; every borrowed
tool needs its own executable path, so each command is lent under its own
alias (hard-linked to the same bytes) and a multi-call binary picks its
applet from argv[0]; and the node script passed the whole configuration
ConfigMap as one command-line argument, which three backends with 16-tool
templates pushed past the kernel's limit, so bodies now travel through
files.

## Eight brokered tools: run files listed, appended, searched, deleted; JSON posted (Celln v0.5.19)

The brokered toolbox grew from three tools to eight without a new channel:
the warden's workspace broker gained list, append, search and delete on
`celln.workspace/v1`, and the egress broker gained a credential-free JSON
POST grant beside the model grant. Each is a six-line guest binary that
sends its request through `/pilot-fetch`. On `fleet-ci` (scope `ci`, two
llama-server backends, package built with busybox and jq: 22 tools):

| Check | Result |
| --- | --- |
| Catalogue | 22 cluster tools: 8 brokered (`celln.json-stdio/v1`, artifact operation `read|write|list|append|search|delete` or an `https` block naming `example.com` and the receiver) and 14 argv commands from busybox and jq. |
| Conversation | On the enduring run that wrote `notes.txt` = `violet`: append `' orange'` at revision 1 → revision 2; search `orange` → `notes.txt, line 1`; list → `notes.txt: 12 bytes`; delete at revision 2 → revision 3; list → 0 files. Five turns, one tool call each, the revision carried by the model between turns. |
| JSON POST | One-shot: `https-post-json` to `http://hook-echo.celln-agents-b.svc.cluster.local:8080/hook` (a Python receiver the journey deploys; plain HTTP on a private Service, permitted by the native backend's `allow-insecure`) with body `{"event":"done"}`. The receiver logged `HOOK /hook {"event":"done"}`; the model answered `200`. |
| Everything else | The 27 earlier checks (two backends, enduring and one-shot runs, jq and grep, adding a backend, exclusion, drain) passed unchanged. |

Two limits the larger toolbox found, fixed in Celln v0.5.19: a model
request now carries every selected tool's schema plus the conversation, and
22 tools did not fit the 8 KiB broker wire (`starter-configure` refused the
template outright), so the guest client, the host's request buffer and the
broker accept 32 KiB for the model request while the workspace and plain
POST paths keep their tighter bounds; and a first attempt raised only the
guest and broker bounds, leaving the VMM's 8 KiB buffer to truncate the
request, which surfaced as `lent executable failed` on the first turn.
The cluster tool names carry the scope prefix (`celln-ci-workspace-list`),
which the journey's gates now use.

## The one-liner: a bare `sympozium install` enables Celln parents

Reported on v0.10.70: after a plain install, the Agent wizard greyed out
**Celln parent** and pointed at a retired path. Now a release build pins a
starter package the project built and signed (`hack/build-celln-starter.sh`:
the pinned Celln, eight brokered tools, busybox and jq; an ephemeral CI
signing key; `celln-starter.json` as the release asset), the fleet is the
default Celln plane, the backend comes from the environment or a prompt, and
the node probe labels the nodes. `test/integration/test-celln-oneliner.sh`
on Kind (`oneliner`, two workers, no hand label, llama-server backend in
`SYMPOZIUM_CELLN_BACKEND`):

| Check | Result |
| --- | --- |
| Package | Built and published by the shared script, signed with a key generated for the run; identity `{image@sha256, packageHash, publisher}` pinned into a CLI build with the same linker flags the release uses. |
| Install | `sympozium install` with only image tags and the private-registry setting: picked the pinned package, took the backend from the environment, probed it, installed scope `starter`, approved the starter tools (grants printed), waited for the nodes, installed catalogue and policy, wired the controller. |
| Nodes | Both workers labelled `celln.dev/kvm=true` by the node probe (`/dev/kvm` and a `/boot` kernel present); the control plane, without a kernel, stayed unlabelled; two owners running. |
| Namespace | A fresh namespace was offered `celln-native-starter` through the call the wizard makes to enable the Celln parent tile; wrappers created on first use. |
| Run | A one-shot on the default fleet answered "Botswana is a landlocked country in southern Africa." |

The first run's only failure was the journey's own port-forward opening into
the API server pod as the final wiring upgrade replaced it; the journey now
waits for that rollout.

## Environment caveats

- Kind nodes have no kernel in `/boot`; the dispatcher's readiness gate
  (`guest_kernel`) needs one, so the host kernel was copied into each worker.
  Real nodes are unaffected. The integration script does this automatically.
- Hand-written `AgentRunTurn` objects must carry the controller ownerReference
  to their AgentRun (`BindTurn` refuses otherwise); the API server sets it.
- The model credential reached dispatchers as a Secret mount (interim; the
  model gateway remains the target boundary).

Reproduce with `test/integration/test-celln-fleet.sh`.
