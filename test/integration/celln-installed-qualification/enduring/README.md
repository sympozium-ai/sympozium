# Three-turn enduring/context qualification — 2026-09-24

> **Historical execution evidence:** the results and exact commands below describe
> the original local run, not a replay on this PR head. Publication adds portable
> explicit state-identity checks; see [PUBLICATION.md](PUBLICATION.md) for current
> invocation and offline verification. The historical JSON snapshot is unchanged.

**Executed successfully, not A03 or installed acceptance.** This is an explicitly
deterministic one-uppercase-tool provider fixture, not a real LLM or browser test.
No paid credentials were discovered or used. Only the existing generated fake
provider Secret was lent through explicit new Agent `authRefs`.

## Actual results

Final epoch: `10ad812c38`. Cluster `hermes-celln-clean-0924`, explicit context
`kind-hermes-celln-clean-0924`, kube-system UID
`e767874f-7e81-4086-aa07-24a4d6b86c80`.

| Tenant | Root UID | Actual worker cell IDs, initial then continuations |
|---|---|---|
| A | `731558a1-49b5-445b-8cca-2d303f497487` | `d0e23c1c87bf`, `a4c96d9c80ec`, `a382667e25b6` |
| B | `686f9f85-739f-4d73-999a-b9b19ea97ca3` | `fce47040eb02`, `29a825ee09f2`, `6e4d94c50f2a` |

- Real scoped-controller/KVM path: initial run plus two sequential UID-bound
  AgentRunTurns in A, complete teardown, then the same in B. Actual tenant
  TokenRequest identities created the roots/continuations. Their private
  kubeconfig files were deleted after cleanup; tokens never appear in evidence.
- Each tenant retained one exact parent ID/incarnation across three turns, with
  three distinct actual child IDs **and** cell IDs. Cross-tenant parents were
  different; all six child/cell identities were different.
- Provider validated exact earlier user/assistant exchanges for the unique
  tenant/epoch sentinel on **both** model calls each turn. History counts were
  `0,0,1,1,2,2`, with six accepted requests per tenant and no rejected attempts.
  Counters are measured by the provider handler, not inferred from the ledger.
  Provider pod UID/container/image stayed unchanged, with zero restarts, from
  the zero-attempt snapshot through teardown/six-attempt snapshot.
- Each root ledger was independently re-read as **6 requests, 3072 reserved
  output tokens, 48 observed fixture output tokens, closed=true**. Each of the
  three turn ledgers recorded 2/1024/16; each of six terminal reservations was
  512 reserved/8 observed and provider_attempted=true.
- All six native status records were independently re-read after deletion and
  matched receipt digests, IDs and execution/substrate provenance. `/worker`
  executable/closure matched the installed enduring profile.
- Initial root snapshot: Running, cleanup=false. Continuations: Succeeded,
  cleanup=true. Actual root deletion produced native Cancelled tombstones with
  `retained parent and descendants stopped`, cleanup=true. Only our observer
  finalizer was removed **after** native confirmation; controller/native
  finalizers were never stripped. Public roots/turns are absent.
- Native doctor confirmed KVM/read-only memslots and can_seal_cells=true. Missing
  build-tool/guest-source capabilities in doctor are retained in evidence; the
  installed packaged runtime executed successfully without host installations.
- `celln ps --json` was empty after each turn **even while the parent was ready**.
  It is supplementary child inventory, not proof the retained parent is absent.
  Root cleanup relies on the native root tombstone plus closed ledger/public
  absence, not that inventory alone.

## Bounds and policy ownership

New namespaced AgentRuntime/Agent/ModelConnection wrappers point to the enduring
`/worker` profile at revision `r-de5d0b56b9d0-71efec768d3c`. Connection request
output bound is explicitly 512. New policy/root lifetime bounds are 3 turns,
6 requests, 3072 output tokens, 600-second lease; no frozen live root budget was
edited. Execution was sequential; builds used two workers.

The existing **owned** review policy temporarily excluded only the new
`enduring-qualification` label. A new policy selected that exact epoch plus the
existing review/tenant labels, allowed only enduring/profile/tool/fixture routes,
and used the reviewed new ceilings. A/B received that temporary selector label.
There were no preexisting roots/native cells. Both original provider Deployments,
the original policy specification, and namespace labels were restored and
independently checked. New wrappers, policy, identities, roots and turns are gone.

## Implementation boundary / missing acceptance

**Workspace persistence is unsupported in this scoped contract.** No capability
checks were changed. Exact source blockers at the reviewed commits:

- Sympozium `internal/cellnauthority/platform_resolver.go:855` runtime validation,
  `:870` tool validation, and `:883` runtime attenuation reject workspace other
  than `none`.
- Celln `crates/celln-cli/src/dispatch_scoped.rs:2209-2211` rejects runtime workspace
  other than `none` as `AUTH_PROTOCOL_UNSUPPORTED`; `:2225-2231` also rejects tool
  workspace, artifacts, HTTPS, egress and inputs outside that contract.

This proves actual bounded conversational history retention, **not guest
workspace/file retention**. Fourth-turn exhaustion was not attempted. There is
no browser, real-provider, migration, concurrency/recovery or full release-matrix
claim. `A03: false` and `installedAcceptance: false` remain explicit.

## Files and evidence

Everything added to the repository is below this `enduring/` directory:

- `provider/main.go`, `provider/main_test.go`: TLS deterministic fixture, exact
  history validation, bounded request handling and sanitized independent stats.
- `Dockerfile`: overlays only the newly built fixture binary on the existing
  reviewed runtime image.
- `run.py`, `test_run.py`: scoped fixture setup, real tenant/KVM execution,
  snapshots, lifetime verification and conservative teardown.
- `collect.py`: separate read-only native/database/inventory collector with
  allowlisted export and source/binary hashes.
- `evidence/2026-09-24.json`: sanitized final execution/independent read-back.

Private/raw snapshots:

```
/home/axjns/.hermes/cache/scratch/hermes-celln-clean-0924/
  evidence-enduring/           # first complete successful run, epoch f7fe72bd22
  evidence-enduring-verified/  # pre-run endpoint readiness failure; restored
  evidence-enduring-final/     # final complete run, epoch 10ad812c38
  enduring-build/review-provider
```

The middle attempt created **no root/model work**: immediately after Deployment
rollout a Service endpoint was briefly unreachable (curl exit 7). Its snapshots
were retained; fixture resources were restored/verified before retry. Bounded
GET-only connection retries now cover endpoint propagation. No model POST is
retried. Both complete runs had actual successful root teardown.

Remaining resources: the parent-owned clean Kind cluster, original review stack,
native tombstones/ledger evidence and imported image remain; **no temporary
resources from these qualifications remain**. No commits, module/chart/core-code
changes, global kubeconfig changes or host installs were made by this task.

## Exact successful build/test/execute commands

From `/home/axjns/Code/sympozium` (existing dedicated cluster only; script refuses
other cluster/UID and existing output). This is not a general installation tool.

```sh
Q=test/integration/celln-installed-qualification
S=/home/axjns/.hermes/cache/scratch/hermes-celln-clean-0924
GOMAXPROCS=2 go test -mod=readonly -p 2 -race -count=1 ./"$Q/enduring/provider"
GOMAXPROCS=2 go vet -mod=readonly ./"$Q/enduring/provider"
python3 -m unittest discover -s "$Q/enduring" -p 'test_*.py' -v
CGO_ENABLED=0 GOMAXPROCS=2 go build -mod=readonly -p 2 \
  -o "$S/enduring-build/review-provider" ./"$Q/enduring/provider"
docker build \
  --build-arg BASE=localhost:5009/celln-qualification@sha256:24de6e8f7d159d6c8056704a194f1fee9e0233c33af9e0f76e45ab8b3c8f80e8 \
  -f "$Q/enduring/Dockerfile" -t localhost:5009/celln-enduring-qualification:0924 \
  "$S/enduring-build"
python3 "$Q/import-image.py" --state "$S" localhost:5009/celln-enduring-qualification:0924
python3 "$Q/enduring/run.py" --state "$S" \
  --output "$S/evidence-enduring-final" \
  --image localhost:5009/celln-enduring-qualification@sha256:c08d9a50380e06ff8236a86a295a8188f0963d08664a9077391c727ed1112c01
python3 "$Q/enduring/collect.py" --state "$S" \
  --report "$S/evidence-enduring-final/report.json" \
  --output "$Q/enduring/evidence/2026-09-24.json"
```

All shown test/build/final execution/collector commands returned zero. Go provider
suite: 3 tests; Python validator suite: 6 tests. Docker emitted only a generic
ARG-without-default warning; the actual base was explicitly digest-pinned. Import
returned the real amd64 manifest digest shown above. Runner output:

```
PASS: A/B each three turns, exact provider history, six independent attempts, closed ledgers and root teardown. installedAcceptance=false A03=false
```

Independent collector output:

```
VERIFIED: 6 native tombstones, 2 stable parents, 6 distinct children/cells, 12 independent provider attempts, 2 closed root/6 turn ledgers, 12 terminal reservations; temporary resources absent; acceptance=false
```

On failure the runner preserves evidence and uncertain authority rather than
stripping finalizers or inventing teardown. Inspect the saved exact UIDs before
operator recovery; do not rerun setup over retained live roots.
