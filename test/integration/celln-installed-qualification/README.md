# Partial installed Celln qualification

**Status: executable development runner, not the #510 release gate.** Even exit
zero and `installedAcceptance: false` mean only this runner's limited checks
completed; they do not satisfy the A01–A12 matrix in #495. Unit tests are synthetic
and never installed evidence. This directory began as untracked operator work;
its existing direct/model/network journeys have been retained.

**Executed locally on 2026-09-23:** all 21 direct/model/RBAC/Cilium checks passed
against a new isolated Kind cluster, with actual KVM cells in A/B and scripted
TLS providers. Four native tombstones and two closed PostgreSQL ledgers were
independently read back. This remains partial evidence, not A01–A12 acceptance.
See [LOCAL-RUNBOOK.md](LOCAL-RUNBOOK.md) for the reproducible dedicated bootstrap,
fixture corrections, exact observed pins, commands and remaining limitations.

## Safe execution boundary

This program is **not read-only**. It creates tenant service accounts, Roles,
RoleBindings, short-lived TokenRequest credentials, four AgentRuns and a network
probe Pod, and execs a credential-free curl command in the review gateway.
It must only target an operator-approved, dedicated disposable review deployment.
Do not point it at a production/shared cluster merely because labels match.

Before any writes, all three existing namespaces must be Active, not deleting,
and carry `sympozium.ai/celln-review=<review>`:

- `celln-review-a-<review>` and `celln-review-b-<review>` also require
  `sympozium.ai/celln-review-tenant=enabled`.
- `celln-review-system-<review>` must not carry the tenant label.

This preflight prevents partial identity creation before discovering an invalid
second tenant or system namespace. It does not prove controller isolation,
namespace UID stability throughout the run, CNI enforcement, or safe provider
configuration. The operator must verify those independently. The runner does not
install namespaces, controllers, CRDs, policies, a database, or a CNI.

Namespace-label, foreign-run-delete and foreign-turn-create negatives use real
tenant credentials with **server dry-run**. Unexpected permission therefore
fails without persisting those attempted writes. These are API authorization
checks, not cancellation/controller lifecycle proofs. Secret GET negatives still
make actual API requests; no Secret contents are recorded. The default target is
`review-provider-credential`; override `-provider-secret` only to match an
explicitly reviewed fixture. This runner never searches for user provider keys.

## Prerequisites and permissions

Read `docs/guides/celln-framework-manual-review.md` and its generated manifests
before preparing a separate review deployment. Historical paths in that document
are not instructions to reuse another operator's cluster or native root.

Required existing fixtures:

- Compatible scoped controller/native Celln/KVM, trusted TLS gateway and durable
  PostgreSQL/ownership state; explicit reviewed image/source/package pins.
- Reviewed public `direct.yaml` and `model-one-shot.yaml` templates, with all
  referenced runtime/Agent/tool/connection resources available in **both** tenants.
  Model output must implement this runner's exact uppercase sentinel contract.
- Exactly one A-namespace provider Pod labelled `app=review-provider-<review>`
  with a private IPv4 Pod IP and TLS listener on 8443; the exact generated
  `review-provider-isolation-<review>` ingress policy and no extra selecting
  ingress policy. Exactly one system gateway Pod labelled
  `app=celln-review-gateway-<review>` with container `gateway` and curl/sh.
- An enforcing CNI and an operator-approved digest-pinned probe image with
  `/bin/sh` and curl; pod security must admit the non-root, tokenless probe.
  Positive controls before/after a connect-timeout denial are mandatory.

The explicitly selected operator kubeconfig needs, at minimum:

| Scope | API access used |
|---|---|
| Three exact review namespaces | get Namespace |
| A/B | create/delete ServiceAccount, Role and RoleBinding; create serviceaccounts/token |
| A/B | authority to grant/bind the generated Role (AgentRun/AgentRunTurn get/list/watch/create/delete); Kubernetes privilege-escalation checks still apply |
| A/B | get/patch/delete AgentRun for cleanup; list Jobs for fallback detection |
| A | list/get/create/delete Pods, get pods/log; list NetworkPolicies |
| System | list Pods; create pods/exec in the existing gateway |
| Tenant identities | create authentication.k8s.io SelfSubjectReview, in addition to the generated namespaced Role |

No administrator Secret access is needed. Do not grant tenants Secret or Namespace
write access to make negative tests pass. Verify narrowly scoped operator RBAC
rather than adding cluster-admin as a workaround.

After reviewing isolation and permissions, from the repository root:

```sh
go run -mod=readonly ./test/integration/celln-installed-qualification \
  -kubeconfig /absolute/path/to/reviewed-kubeconfig \
  -context explicit-reviewed-context \
  -review 495 \
  -templates /absolute/path/to/reviewed/runs \
  -probe-image 'REGISTRY/review@sha256:FULL_64_HEX_DIGEST' \
  -output /absolute/path/to/new-private-evidence-directory
```

The output directory must not exist. The CLI requires a complete lowercase
64-hex SHA-256 image digest; independently verify provenance of the pinned image.
`-journey isolation` runs tenant-token RBAC and network checks without creating
native AgentRuns; `-journey direct` or `model` selects that one-shot journey in
both tenants. These modes are partial by definition, never a release gate.
CLI usage errors exit 2; failing checks, recovery requirements or report-write
errors exit nonzero. An eight-minute execution budget is followed by a separate
one-minute cleanup budget. Runs lacking native cleanup confirmation are retained;
never remove native/controller finalizers to make cleanup appear complete.
Only this runner's observer finalizer is removed after recorded confirmation.
Resource deletes use UID preconditions. Delete requests are not independent
verification of eventual resource absence or native descendant inventory.

`report.json` is owner-only inside the new owner-only directory and explicitly
sets `installedAcceptance: false`. It contains full AgentRun snapshots, including
spec/status: **treat it as sensitive, inspect and redact before sharing**. It is
not the evidence validator's A01–A12 manifest. Partial identity setup/recovery
still requires operator inventory: the report does not yet persist every
ServiceAccount/Role/RoleBinding/probe UID or a full provenance inventory.

## Coverage still missing for #510

- A01/A02: independently correlated receipts/tool/schema/closure identities,
  zero/direct and exact/model provider counts, ledger reservations and per-tenant
  provider authentication; current checks are exact results, nonempty receipt/
  cell identity, status cleanup flag and absence of a currently listed owned Job.
- A03/A04: three-turn enduring browser/API journeys, stable parent/incarnation,
  distinct children, guest workspace retention, and denied-namespace execution.
- A05–A09: all cross-tenant boundaries/name recreation/token substitution,
  route/policy withdrawal, expiry, budget races, multiple gateway replicas,
  database failure, dropped acknowledgements, restarts, cancellation, owner loss
  and independently verified descendant teardown.
- A10: credential canary scans across explicitly enumerated surfaces; network
  checks cover A-to-provider ingress only, not guest or all cross-tenant egress.
- A11/A12: one supported installation followed by fresh ordinary namespaces,
  built-browser/YAML parity, reviewed migration/rollback and unsupported-backend
  regressions. This legacy review fixture is not that installation proof.
- Bounded real-provider KVM smoke for each Harness lifecycle, explicitly supplied
  test credentials/allowance; exact source/image/fixture hashes, architecture,
  topology, KVM/CNI attestation and hashed A01–A12 evidence artifacts.

## Local assessment (2026-09-23)

Read-only inspection found context `kind-celln-tenancy`, one Ready Kubernetes
v1.35.0 node, existing Celln/Sympozium workloads, a `kindnet` DaemonSet and existing
application NetworkPolicies. None of the three `celln-review-*-495` target
namespaces exist. Policy objects alone do not prove enforcement; no CNI packet
qualification or KVM attestation was performed. **No live runner, provider calls,
exec, deployment, or cluster mutations were performed in this assessment.**
The chart dispatcher-toleration fix and chart tests/lint were reported by the
parallel owner; they do not remove the missing fixture/isolation/evidence gates.

## Offline verification

```sh
go test -mod=readonly -race -count=1 -v ./test/integration/celln-installed-qualification
go vet -mod=readonly ./test/integration/celln-installed-qualification
```

Tests check read-only namespace preflight and refusal before any writes for
missing/foreign/disabled/terminating namespaces, administrator credential
separation, private report permissions and report persistence failures. They do
not contact Kubernetes or claim installed qualification.
