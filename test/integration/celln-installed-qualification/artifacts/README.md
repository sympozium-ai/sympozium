# Scoped artifact qualification — disposable installed development tier

This extends the stacked baseline/enduring fixtures with real guest-backed
write/read/read across two independently authorized tenant roots. It is **not**
normal-release installation acceptance, real-provider coverage or A03.
`installedAcceptance:false` and `A03:false` are intentional even after success.

## Recorded execution: 2026-09-25

`evidence/2026-09-25.json` is an allowlisted export with independent live readback.

- Sympozium executable source: `75f7d3c93ce050c1aed9771d1436dc2560582bab`;
  artifact fixture/provider sources are added in this follow-up commit.
- Celln executable source: `98994ffcdad973b6757d4c4449e3bb728127d642`
  (the build preceded the commit; production source is identical).
- Single-host Kind cluster: `hermes-celln-artifacts-0924`, kube-system UID
  `900e72f1-abd3-4eba-9666-0669f3f2b9e8`; epoch `852b4e01f0`.
- Imported amd64 manifest:
  `docker.io/library/hermes-celln-artifacts@sha256:933f88e8e88ea89b3373b167545e5c7c5cf64a7cc7b56176a74c42babd286a26`.
- Two stable, distinct native parents; three distinct guest cells per parent;
  six distinct cells in total. Native runtime and tool workspace remain `none`.
- Deterministic provider verified actual write then two read responses for each
  namespace sentinel, plus expected prior conversation history. Six independent
  handler attempts per tenant, twelve total. No provider pod/container replacement
  or restart through final counter collection.
- Six foreign-tenant operations denied by Kubernetes RBAC: read, dry-run delete
  and dry-run turn creation in each direction. These are **API authorization**
  negatives, not a direct adversarial native artifact-access or network test.
- Both durable ledgers closed: each reserved six requests / 3072 output tokens,
  observed 48 output tokens, max three turns. Reservations are not the independent
  provider-attempt count; both are collected separately.
- Independent collector re-read both native root tombstones and PostgreSQL rows,
  matched recorded evidence, checked no public AgentRuns/live cell inventory,
  and retained the runner's fixture-restoration evidence.
- Core authority/scoped/capability/provider race tests, API/controller/gateway
  tests and three Python fixture tests passed. Native scoped HTTP suite: 16 passed.

## Failures and corrections (not concealed as successful attempts)

1. Initial fixture catalogue GET RBAC omitted artifact names. Add only exact
   profile/read/write names to the dedicated controller allowlist.
2. Capability discovery required the dispatcher-wide credential. Celln now allows
   the scoped operator credential only for bodyless GET `/v1/capabilities`.
   Other dispatcher routes remain denied. The artifact TLS frontend permits that
   GET; deploy this provider binary as the frontend as well as the model fixture.
3. An older profile pointed to the minimal runtime source rather than the composed
   descriptor. Native refused `prepared closure source count mismatch` and
   confirmed cleanup. Catalogue immutability rejected an attempted edit. The
   correction was published as `scoped-artifact-runtime-v2`, revision
   `scoped-artifacts-v2`; no existing admitted root authority was modified.
4. Only observer finalizers were removed after controller/native cleanup (or
   confirmed cancellation before admission). Failed-run evidence stays private;
   credentials/deployment snapshots are not published.

## Reproduction

Read `../README.md`, `../LOCAL-RUNBOOK.md` and `../enduring/README.md` first.
Use a **new** disposable cluster/state/package, explicit private kubeconfig and
independently checked kube-system UID; never point these helpers at an operator
cluster. This is a composition of reviewed fixture steps, not a clean-room
one-command installer; the recorded execution included the repairs above.

1. Bootstrap and prepare baseline fixtures as in the local runbook, with the
   pinned Celln checkout above. Build all four image binaries afresh (musl Celln,
   `CGO_ENABLED=0` Go, readonly/locked dependencies, two build workers).
   **Replace the baseline `review-provider` build target** with
   `./test/integration/celln-installed-qualification/artifacts/provider` before
   image build/import/`prepare-fixtures.py`. Record the imported manifest digest.
2. Cold-package starter tools into `$STATE/starter-package` using the pinned
   CLI's `starter-package`: supply reviewed runtime/guest directories and kernel,
   and an owner-only **32 raw-byte** signing seed at `$STATE/artifact-signing-key`.
   Cold packaging does not grant authority. Preserve the original package report
   and hash-verify its binaries. Never publish the seed, credentials or kubeconfig.
3. Admit only the exact runtime/read/write composition and publish the catalogue:

   ```sh
   Q=test/integration/celln-installed-qualification
   python3 "$Q/artifacts/package.py" --state "$STATE" \
     --expected-cluster-uid "$EXPECTED_CLUSTER_UID" \
     --celln "$CELLN/target/x86_64-unknown-linux-musl/release/celln" \
     --guests "$CELLN/target/x86_64-unknown-linux-musl/release"
   ```

   Requires Python `blake3`. Fails if roots exist or artifact-build output already
   exists; do not overwrite a failed package in place. The profile uses the
   composed descriptor's ordered runtime/read/write sources and the admitted
   mote. Tool revisions remain `scoped-artifacts-v1`; profile revision is v2.
4. Execute the runner, then independently collect against the same owned cluster:

   ```sh
   python3 "$Q/artifacts/run.py" --state "$STATE" \
     --expected-cluster-uid "$EXPECTED_CLUSTER_UID" \
     --output "$STATE/artifact-evidence" --image "$IMAGE"
   python3 "$Q/artifacts/collect.py" --state "$STATE" \
     --expected-cluster-uid "$EXPECTED_CLUSTER_UID" \
     --evidence "$STATE/artifact-evidence" --output "$STATE/artifacts-public.json"
   ```

On failure retain snapshots and authority. Do not retry model POSTs, broaden
capabilities, replace admitted owners or remove controller/native finalizers.
Successful runner cleanup restores original policy/provider specs, deletes
qualification resources and removes short-lived tenant kubeconfigs. Inspect the
allowlisted collector output before publishing; never copy the whole state tree.

## Remaining release gates

Real LLM/provider behavior, browser journey, normal operator installation,
restart durability (explicitly unsupported here), migration, concurrent hostile
native clients, network isolation and the full A01–A12 matrix are not proven by
this fixture. Parents execute sequentially, not concurrently. Both tenants use
the same native operator-owner value; separation is established by tenant/root/
parent/budget identities, not different operator-owner values. Receipt fields
are matched, not independently cryptographically verified. Provider statistics
are fetched with TLS certificate verification disabled in this disposable fixture;
they are not a hardened telemetry channel. Packaging guards currently use Python
`assert` (do not run with `-O`), and provider accepted-event recording precedes
its tool-presence check. Neither changes the recorded successful result, but these
are fixture-hardening limitations.

`fixturesRestored` covers runner-owned changes, not package teardown: catalogue
entries, native trust-store additions and narrow controller allowlist additions
remain in the dedicated disposable cluster. The cluster is intentionally retained
for inspection. Supporting Celln and stacked Sympozium PRs require review and CI;
this evidence closes no issue.
