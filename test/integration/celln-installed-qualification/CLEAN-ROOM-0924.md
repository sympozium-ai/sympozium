# Clean-room helper replay — 2026-09-24

**Result: 21/21 partial development checks passed; installedAcceptance=false.**
The final existing helper path worked on its first attempt, without manual
cluster patches, bootstrap resumption, or helper fixes. This verifies the fixes
already described in LOCAL-RUNBOOK.md; it is not release installation acceptance.

## Retained cluster and evidence

- Cluster: `hermes-celln-clean-0924`
- Context: `kind-hermes-celln-clean-0924`
- State: `/home/axjns/.hermes/cache/scratch/hermes-celln-clean-0924`
- Kubeconfig: state directory's `kubeconfig`; never use ambient context.
- kube-system UID: `e767874f-7e81-4086-aa07-24a4d6b86c80`
- Runner epoch: `2b28e8253d`
- Private evidence: `evidence/report.json`, `evidence/verified.json`,
  `evidence/clean-room.json`; logs: `prepare.log`, `runner.log`.
- Bootstrap log: `/home/axjns/.hermes/cache/scratch/hermes-celln-clean-0924-bootstrap.log`.
- Public metadata/hash summary: `evidence/clean-room-0924.json` in this directory.

The new cluster is deliberately retained for separately reviewed enduring tests.
The existing `hermes-celln-qual2-0923`, `celln-tenancy`, and `substrate-local`
clusters remain present. No commands wrote to them, the global kubeconfig, or
host installation. No paid provider credentials were used. Do not reuse adapted
historical enduring templates: A and B currently select identical one-shot
runtime declarations. Enduring qualification requires new reviewed declarations.

## Integrity and execution

Before creation, available host RAM was 49 GiB. The existing package at
`/home/axjns/.hermes/cache/scratch/hermes-celln-package-0923` passed all 50 entries
of `MANIFEST.blake3`. The host lacked `b3sum` (probe exit 127); the already installed
Python `blake3` module verified every entry instead, with no installation.

The local image `localhost:5009/celln-qualification:0923` was exported with
`docker save --platform linux/amd64`. SHA-256 verification covered its actual
manifest and all five referenced config/layer blobs before reuse. Its imported
manifest was `sha256:6ee177d868afdd2e8aad2140669508bafb2cebc89dec4e7fbb15e2d524bdf008`.
The Docker inspect ID was not treated as the manifest digest. PostgreSQL was
imported from local `postgres:17`, resolving to
`docker.io/library/postgres@sha256:d13db94ae661d517c5ed57c509a578d5ea64aae639871ba25294f4f42d83de28`.

Bootstrap, both imports, fixture preparation, the full runner, the independent
collector, and three offline bootstrap tests each exited **0**. The runner used
`GOMAXPROCS=2`, `-mod=readonly`, and `-p 2`; fixture generation also bounded Go
builds to two workers. No image/package rebuild was needed.

Docker readback confirmed 8 GiB memory, 8 GiB memory+swap, and four CPUs. Node
readback confirmed Ready and the persisted kube-system UID. Live A/B runtime
specs matched exactly; both Agents explicitly lend only the fixture credential.
The independent collector matched four native tombstones, two closed model
ledgers (2 requests, 1024 reserved output tokens, 16 observed output tokens each),
no direct-run ledger, empty native inventory, and absence of temporary runner
resources. Gateway positive network controls bracketed a tenant curl timeout
(exit 28, HTTP 000, no connected peer).

**Collector caveat:** `verified.json` includes an old fixed limitation mentioning
an earlier admission-refused retained run. That applies only to the old qual2
cluster, not this clean run. The collector was not edited under this ownership
boundary; `clean-room.json` records the correction explicitly.

## Replay command sequence

Use a **new** name/state for another replay; the values below are the actual
already-existing successful execution, not permission to overwrite them.
Start in the Sympozium repository root after independently verifying the reused
package manifest and image blobs as above.

```sh
set -euo pipefail
umask 077
Q=test/integration/celln-installed-qualification
STATE=/home/axjns/.hermes/cache/scratch/hermes-celln-clean-0924
NAME=hermes-celln-clean-0924
PACKAGE=/home/axjns/.hermes/cache/scratch/hermes-celln-package-0923
python3 "$Q/bootstrap-kind.py" --name "$NAME" --state "$STATE" \
  --node-image 'kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f'
IMAGE=$(python3 "$Q/import-image.py" --state "$STATE" localhost:5009/celln-qualification:0923)
POSTGRES=$(python3 "$Q/import-image.py" --state "$STATE" postgres:17)
python3 "$Q/prepare-fixtures.py" --state "$STATE" --package "$PACKAGE" \
  --image "$IMAGE" --postgres-image "$POSTGRES"
GOMAXPROCS=2 go run -mod=readonly -p 2 ./"$Q" \
  -kubeconfig "$STATE/kubeconfig" -context "kind-$NAME" -review 495 \
  -templates "$STATE/fixtures/runs" -probe-image "$IMAGE" \
  -output "$STATE/evidence"
python3 "$Q/collect-evidence.py" --state "$STATE" \
  --report "$STATE/evidence/report.json" --output "$STATE/evidence/verified.json"
```

No browser, enduring/workspace retention, real-provider, migration, concurrency,
recovery, or full A01–A12 claim follows from this replay. Ledger reservations are
not an independent provider-attempt counter. PostgreSQL storage survives pod
restarts, not cluster deletion. Keep private fixture keys and full reports out
of public evidence.
