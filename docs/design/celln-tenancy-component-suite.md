# Celln tenancy component security suite (#509)

This runnable tier tests the shared Go/Rust credentials, durable PostgreSQL
accounting, and gateway namespace/credential binding through a live Kubernetes
API and a TLS recorder provider. It also builds and launches the real gateway
binary, testing the TLS/file/JWKS startup path and durable duplicate suppression
after process restart for both tenants. It does **not** qualify installed Celln,
tenant RBAC, enforcing CNI, KVM isolation, browser flows, or a real LLM provider.
`--release` is deliberately refused. #509 remains incomplete.

## Run

Use clean Sympozium and Celln checkouts with matching fixture bundle pins.
Provision a **disposable PostgreSQL database**: tests create/mutate ledger tables.
The script never provisions or deletes that database. Install the ModelConnection
CRD separately; the test must be allowed to create and clean up uniquely named
temporary namespaces. Existing application workloads are not changed.

```sh
export CELLN_TENANCY_KUBECONFIG=/absolute/path/to/isolated-kubeconfig
export CELLN_TENANCY_CONTEXT=explicit-reviewed-context
export CELLN_TENANCY_CELLN_SOURCE=/absolute/path/to/celln
export CELLN_TENANCY_CELLN_SHA=FULL_COMMIT_SHA
export CELLN_MODEL_BUDGET_DATABASE_URL='postgres://.../disposable_test_database'
make test-celln-tenancy-integration
```

Context matching is an operator targeting check, not proof that the cluster is
isolated. Review the endpoint before running. The runner does not change the
current context, install CRDs, deploy controllers, or enable mediated admission.
Celln's SHA pins the tested source, not approval or production readiness.

Evidence is written into a new owner-only temporary directory, whose path is
printed. `summary.json` identifies source commits, context, case exit codes and
logs, and explicitly sets `installedAcceptance: false`. Go case groups must
contain passing tests and no skips; the live Kubernetes test must pass by exact
name. A failing command fails the suite. Logs stay local; inspect before sharing.
Early prerequisite refusal produces no acceptance summary.

## Combined local KVM prerequisite tier

With the same explicit environment variables and a clean pinned Celln checkout:

```sh
make test-celln-tenancy-local
```

This runs the component/API/binary checks and Celln's strict `make conformance-kvm`
runner. The latter builds its native parent and JSON Harness package, makes no
external model calls, executes five real-KVM proofs in separate serial processes,
and refuses printed skips/missing cases. `/dev/kvm`, the kernel, musl target,
compiler and image tools are required. KVM logs remain in the pinned Celln
checkout's private `target/conformance-kvm.*` directory.

The combined command does **not** claim a controller-created mediated run travels
through Celln to the gateway. Receiver/broker wiring, multi-namespace mediated
workloads, enforcing-CNI, browser and real-provider tests remain missing; the
summary retains `installedAcceptance: false` even when local KVM succeeds.

## Executed epoch (historical component-only run)

- Sympozium: `48db137`; Celln: `96b7755b89c81f68b82e15231c301de51d92951d`.
- `make test-celln-tenancy-integration`: exit 0; all five evidence checks passed.
- PostgreSQL 17 in a disposable localhost-bound container.
- Context: `kubernetes-admin@kubernetes`; admin-level application binding proof,
  **not** tenant RBAC proof.
- Live test cleanup confirmed both newly created namespaces were deleted.
- Provider was a TLS recorder, not an external LLM.
- Evidence directory: `/tmp/celln-tenancy-components.mML9RuTU` (local, not a
  durable CI artifact).

Receiver admission, native/enduring brokering, enforcing-CNI denial attempts,
KVM guest credential attempts, browser journeys, and real-provider smoke remain
required for installed acceptance. Passing this tier cannot satisfy #510.
