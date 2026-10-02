# Enduring/context publication review

Related: #510, #495. Stacked on baseline qualification PR #634. Neither issue is
closed. `installedAcceptance: false` and `A03: false` remain mandatory: this lane
uses a deterministic provider, not a real LLM, and claims no guest workspace
retention, browser execution or full release acceptance.

## Immutable historical evidence versus current tests

`evidence/2026-09-24.json` is byte-for-byte the original independently collected
export (SHA-256 `27f53e9f9d9efd454929dc17a172e6ae1df27b9be3a8506e5d2d70b6247301ec`).
Its original source SHA and source-file hashes describe that historical run, not
this publication branch. Its six native child tombstones, two stable parents,
twelve independently counted provider attempts, exact prior user/assistant
history, closed ledgers and restored fixtures must not be attributed to a new
execution. The dated README preserves the exact old commands and limitations.

Publication review changed `run.py` and `collect.py` without executing them
against a cluster:

- Require `--expected-cluster-uid` instead of embedding one operator's host/cluster
  identity. Validate the absolute bootstrap state, dedicated Kind name, context,
  node, state-local kubeconfig, persisted UID and live UID. Collection also matches
  the report UID. No ambient kubeconfig or implicit current-context fallback.
- Preflight all three namespace ownership/enablement/lifecycle states before
  authority changes, rather than discovering an invalid second tenant midway.
- Validate the full lowercase image digest rather than only its string length.
- Do not persist raw API/admission stderr in error messages.
- Restore the exact original policy selector, including any original expressions,
  with optimistic UID/resourceVersion tests instead of unconditionally deleting
  all expressions. Native/controller finalizers are never stripped.

New synthetic safety tests cover those state/UID targeting boundaries, refusal
before writes, diagnostic redaction and selector restoration. No cluster state,
provider image, private logs or installed evidence was regenerated. The public
export was audited for credential/private-key/JWT patterns and sensitive JSON
keys. The only bearer literal in Go tests is a deliberate synthetic sentinel;
provider counters must omit it. Private kubeconfigs and generated fixture keys
are not committed.

## Current invocation (operator-reviewed dedicated fixture only)

Prepare a new dedicated bootstrap and reviewed fixtures using the parent runbook.
This helper still deliberately selects review `495`, the original reviewed
runtime/tool revision `r-de5d0b56b9d0-71efec768d3c`, and `workspace=none`. Changing
those is a separate fixture review, not a reason to relax checks. This is not a
normal installation tool. Ensure exclusive operator ownership for the run;
names and labels alone do not establish isolation. Failures retain authority and
private reports for recovery rather than blindly restoring a live root's policy.

Independently review the new cluster's `kube-system` UID, its persisted
`cluster.json` and exact kubeconfig/context. Supply that UID explicitly; do not
copy the historical UID or populate it from an ambient cluster query.

```sh
Q=test/integration/celln-installed-qualification
STATE=/absolute/path/to/new-reviewed-dedicated-state
EXPECTED_UID=REPLACE_WITH_INDEPENDENTLY_REVIEWED_KUBE_SYSTEM_UID
IMAGE=REPLACE_WITH_REVIEWED_PROVIDER_IMAGE_AT_SHA256_DIGEST
python3 "$Q/enduring/run.py" --state "$STATE" \
  --expected-cluster-uid "$EXPECTED_UID" --image "$IMAGE" \
  --output "$STATE/new-enduring-evidence"
python3 "$Q/enduring/collect.py" --state "$STATE" \
  --expected-cluster-uid "$EXPECTED_UID" \
  --report "$STATE/new-enduring-evidence/report.json" \
  --output "$STATE/new-enduring-evidence/public.json"
```

These placeholders intentionally fail validation until explicitly replaced. Build
and import the provider as described in the dated README; never reuse its already
existing output directory. Inspect every export before publishing.

## Offline verification on the publication branch

```sh
GOMAXPROCS=2 go test -mod=readonly -p 2 -race -count=1 ./test/integration/celln-installed-qualification/...
GOMAXPROCS=2 go vet -mod=readonly -p 2 ./test/integration/celln-installed-qualification/...
python3 -B -m unittest discover -s test/integration/celln-installed-qualification -p 'test_*.py' -v
python3 -B -m unittest discover -s test/integration/celln-installed-qualification/enduring -p 'test_*.py' -v
```

All passed during publication on 2026-09-24: both Go packages (race enabled), Go
vet, three baseline bootstrap tests and twelve enduring validator/safety tests.
Python discovery must be run separately for the `enduring` directory. These tests
use synthetic values and mocks, never installed evidence.
