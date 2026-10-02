# Disposable local KVM qualification: reproduction and actual results

This is the bounded **development fixture** lane for #510/#495, not the normal
operator installation and not release acceptance. It uses the real scoped
controller, KVM dispatcher/guests, TLS gateway, PostgreSQL and Cilium, plus the
explicitly scripted `celln-review-provider`. No paid provider keys are needed,
searched for, or used. Do not replace this fixture with ambient credentials.

## Actual execution (2026-09-23)

- Source: Sympozium `43f00691e720c1588d3705ed08b54a53bf1477c8` plus this uncommitted
  runner work; Celln `de5d0b56b9d0d9a8e6e610ddd2a10b6ed4fc2b56` (0.5.26).
- New cluster: `hermes-celln-qual2-0923`, one Kind control-plane node, capped at
  8 GiB / 4 CPUs. Existing `celln-tenancy` and `substrate-local` were not changed.
  All Kubernetes calls used the private kubeconfig and exact context; no global
  kubeconfig/context or host installation was changed.
- Kubernetes node:
  `kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f`.
  Cilium chart 1.18.2. Exact running host kernel/modules mounted read-only, with
  `/dev/kvm` and a new dedicated native state directory. No host device chmod.
- Rebuilt native/controller/gateway/provider image (linux/amd64 manifest):
  `localhost:5009/celln-qualification@sha256:6ee177d868afdd2e8aad2140669508bafb2cebc89dec4e7fbb15e2d524bdf008`.
  PostgreSQL:
  `docker.io/library/postgres@sha256:d13db94ae661d517c5ed57c509a578d5ea64aae639871ba25294f4f42d83de28`.
- Full runner exited **0**, epoch `9c75d945f4`: **21/21 checks passed**. A/B each
  executed real direct and Harness one-shot cells with exact namespace sentinel
  output, nonempty receipts, cleanup confirmation, and no listed Job fallback.
- Actual tenant TokenRequest credentials passed identity verification and were
  denied own/foreign fixture Secret GET, namespace label mutation, foreign run
  read/list/delete and foreign turn create. Mutating negatives used server dry-run.
- Enforcing network observation: gateway positive control before and after;
  tenant probe had curl exit 28, HTTP `000`, **no connected remote IP**. Only A's
  provider ingress path was probed; this is not a universal egress/isolation proof.
- Independent collector matched all four native status/tombstone receipt digests,
  owners and cell IDs against the runner. Native `ps --json` returned no cells;
  `doctor` reported KVM with read-only memslots and `can_seal_cells:true`.
- Both model run ledgers were closed with **2 requests / 1024 reserved output
  tokens / 16 observed output tokens each**. Neither direct UID had a model
  budget row. These are durable ledger observations, not an independent counter
  of every provider attempt. Fixture responses required the namespace's generated
  canary credential. The fixture plan calls **one** uppercase tool, not A02's two.
- The four successful AgentRuns, their temporary identities and network probe
  were checked absent after cleanup. The earlier admission-refused model run
  `qualification-model-661c34ce70` and its temporary identities are retained as
  failure evidence, with only the runner's observer finalizer. No native start
  was recorded for that run; its report is **not** a successful run.

Private artifacts (inspect/redact before sharing):

```
/home/axjns/.hermes/cache/scratch/hermes-celln-qual2-0923/
  kubeconfig
  cluster.json
  fixtures/
  native-root-495/
  deployment-private-495/       # generated fixture keys; never publish
  evidence-all/report.json      # initial failure retained
  evidence-fixed/report.json    # successful 21 checks
  evidence-fixed/ledger.csv
  evidence-fixed/verified.json  # independent native/ledger read-back
  evidence-isolation/report.json
```

`installedAcceptance` remains **false** everywhere. No browser, three-turn
enduring/workspace proof, real-provider smoke, migration, restart/concurrency
suite, multi-host claim, or complete A01–A12 evidence was produced.

## Fixes discovered by actual execution

1. Kind needs a **version tag as well as a digest** to select kubeadm's API.
   The first digest-only newer node failed kubeadm configuration before install;
   Kind deleted that failed node. Bootstrap now rejects digest-only node refs.
2. Initial Cilium pulls exceeded the original 180-second node wait. That attempt
   was resumed manually after Cilium became ready. Bootstrap now explicitly
   waits for Cilium rollout and uses a 600-second node deadline. A single-node
   Kind cluster may already lack the control-plane taint: removal is conditional.
3. Host caller membership in the KVM group is not required for privileged Kind.
   Check the character device without changing permissions, then establish real
   usability inside the native pod (actual execution and `doctor`).
4. Docker's multi-platform cache can lack non-amd64 layers; Kind's import with
   `--all-platforms` failed. `import-image.py` imports the explicit amd64 platform,
   obtains the real manifest digest from containerd, and verifies the digest alias.
5. The historical manual fixtures predate the Agent credential-lending check.
   Model execution correctly refused `AUTH_ROUTE_MISMATCH`. The dedicated fixture
   adapter now explicitly grants **only its generated fixture Secret** using
   Agent `authRefs`; it does not weaken the resolver or add Secret API privileges.
6. Historical tenant B used an enduring profile, unsuitable for the identical
   one-shot runner. The dedicated adapter selects the package's one-shot profile
   in both A/B. Do **not** use the historical enduring templates from this adapted
   output; enduring qualification needs its own deliberately reviewed fixtures.
7. The runner's Secret-negative target now matches `review-provider-credential`,
   accepts a configurable exact fixture name, validates the full probe digest,
   supports bounded journey selection, and reports terminal execution failures
   promptly while retaining unconfirmed resources.

## Reproduce on a new disposable cluster

Prerequisites: Docker, Kind, kubectl, Helm, Python 3, Go, Rust with the musl target,
OpenSSL, jq, readable current kernel/modules and `/dev/kvm`. Check available RAM;
coordinate other agents. Builds below are limited to two workers. Do not reuse
an existing cluster/state directory. No sudo or host installation is required.

From the Sympozium root, choose **new absolute paths/names**, a reviewed Celln
checkout and an existing reviewed runtime base image. The actual run used the
cached historical review base below solely for native runtime dependencies;
all four executable binaries were replaced by fresh builds.

```sh
set -euo pipefail
umask 077
Q=test/integration/celln-installed-qualification
STATE=/absolute/new/hermes-celln-state
NAME=hermes-celln-your-unique-name
CELLN=/absolute/reviewed/celln-checkout
PACKAGE=/absolute/new/celln-package
BUILD=/absolute/new/celln-image-context
BASE='localhost:30501/celln-review@sha256:0017975d80a0304a66ab146476627470fc5313e92b21604912ed4e1d5d5423b6'

python3 "$Q/bootstrap-kind.py" --name "$NAME" --state "$STATE" \
  --node-image 'kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f'

CARGO_BUILD_JOBS=2 "$CELLN/scripts/package-framework-native.sh" \
  "/boot/vmlinuz-$(uname -r)" "$PACKAGE"
CARGO_BUILD_JOBS=2 cargo build --manifest-path "$CELLN/Cargo.toml" --locked \
  --release --target x86_64-unknown-linux-musl -p celln-cli --bin celln
mkdir -m 700 "$BUILD"
install -m 755 "$CELLN/target/x86_64-unknown-linux-musl/release/celln" "$BUILD/celln"
CGO_ENABLED=0 GOMAXPROCS=2 go build -mod=readonly -p 2 -o "$BUILD/controller" ./cmd/celln-scoped-controller
CGO_ENABLED=0 GOMAXPROCS=2 go build -mod=readonly -p 2 -o "$BUILD/model-gateway" ./cmd/model-gateway
CGO_ENABLED=0 GOMAXPROCS=2 go build -mod=readonly -p 2 -o "$BUILD/review-provider" ./test/integration/celln-review-provider

docker build --build-arg "BASE=$BASE" -f "$Q/Dockerfile" -t hermes-celln-review:local "$BUILD"
IMAGE=$(python3 "$Q/import-image.py" --state "$STATE" hermes-celln-review:local)
# Select an explicitly reviewed locally available PostgreSQL image, not latest.
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

The scripts refuse existing generated fixtures rather than overwriting evidence.
On partial bootstrap failure, inspect exact resources before resuming the failed
step; never redirect these commands to an existing operator cluster. The adapter
is explicitly tied to the reviewed manual generator. PostgreSQL uses Kind's
`standard` local-path PVC: durable across pod restarts, **not cluster deletion**.
The collector currently targets review `495` and the four-run default journey.

After preserving evidence and independently confirming no live native cells,
teardown **only the exact owned Kind cluster** with `kind delete cluster --name
"$NAME"`. Do not delete private state/PVC authority during unresolved native
cleanup. The actual executed cluster was left available for coordinator inspection;
its temporary loopback-only registry `hermes-celln-registry-0923` was removed
and its absence verified after all images were imported. A registry is unnecessary
for the reproduction above because the image-import helper does not need one.
