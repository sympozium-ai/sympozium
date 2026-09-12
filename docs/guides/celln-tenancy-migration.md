# Celln tenancy migration: inventory and stop boundaries (#508)

**Status:** dry-run inventory is available; mediated migration and new-namespace
release qualification are not. Do not run the legacy native installer again for
a tenant or treat this guide as permission to enable mediated execution.
#506 packaging, runtime admission/brokering, and #510 installed qualification
must be completed first. This does not close #494 or #490.

## 1. Inspect without mutation

Run once for each explicitly selected legacy tenant and control-plane namespace:

```sh
umask 077
export CELLN_MIGRATION_KUBECONFIG=/absolute/path/to/reviewed-kubeconfig
export CELLN_MIGRATION_CONTEXT=reviewed-context
export CELLN_MIGRATION_NAMESPACE=legacy-namespace
bash scripts/celln-migration-inventory.sh > legacy-inventory.json
```

Require exit 0 and inspect the report. The command only GETs namespaced runtimes,
tools, runs, Pods, ConfigMaps and PVCs. It does not GET Secrets, invoke Celln,
write Kubernetes objects or read host credential files. It projects allowlisted
identities, artifact/schema hashes, Pod images/mount references, parent/turn IDs,
and storage references. It omits annotations, ConfigMap contents, environment
values, tasks, transcripts and results. Review even these metadata fields before
sharing: resource references and host paths can be operationally sensitive.

`migrationAuthorized` is always false; `proposedDeletions` is empty. ConfigMap
identities are candidates, **not** inferred grant sources. A terminal Kubernetes
phase is not proof that descendants are gone. Lists are not a cross-resource
transaction: repeat the inventory and investigate differences before proceeding.

The operator must separately record, without copying contents:

- exact configured operator/runtime/Agent grant source names, UIDs and digests;
- owner target and principal, original parent/incarnation/child/turn identities;
- prepared registration identity and its owner-local durable journal location;
- credential file **references**, key IDs, retained public verification keys;
- source SHAs, image digests, protocol versions, node architecture/KVM and CNI;
- ledger schema/version and durable backup/restore procedure, not spendable rows.

Do not discover arbitrary host files or export entire registration documents,
Secret objects or Pod environment maps to find these values. Missing data is a
reported operator decision, not an invitation to guess an authority source.

## 2. Prepare a reviewed proposal — currently manual and blocked

For each exact selected runtime/tool revision, verify executable, closure,
publisher, ABI, schemas and admitted node artifacts. Compare the **intersection**
of the original operator/runtime/Agent grants, preserving ordered tool identity,
limits, namespace/Agent binding and approved provider route. Never union grants,
turn object labels into authority, infer unrestricted origins, or copy all legacy
tools into an allowlist. Unresolved route/publisher/limit mismatches stop migration.
The inventory does not generate a safe policy proposal by itself.

Keep AgentRuntime and CellnTool namespaced. Shared CellnRuntimeProfile and
ClusterCellnTool are new resources with explicit references, not converted CRDs.
Do not reinterpret old names or edit immutable historical receipts.

## 3. Install, test, then drain — not yet a shipped migration command

After compatible components and trust/accounting are qualified, publish only the
reviewed immutable catalogue and policy proposal. Test newly admitted work before
retiring old authority. Finish or explicitly drain each old parent on its original
owner. Never attach a legacy parent to a new run, mint it a fresh budget, replay
accepted work, or transfer it to another node as a migration shortcut.

Persist an operator checklist keyed by namespace/resource UID and original owner
identity. On interruption, inspect those same identities and unresolved operations;
do not reinstall keys, reset journals, re-register allowance or regenerate work.
No deletion is performed by the inventory command. Retirement needs a separately
reviewed exact resource/UID list, explicit confirmation, and owner evidence that
no active reference remains. A same-name resource with a different UID is not the
resource approved for retirement. Preserve unrelated Job/OCI resources.

## 4. Rollback boundary

Never deploy a binary that ignores mediated permits while live credentials exist.
Do not erase ledger rows, ownership journals or tombstones, and do not restore an
old budget snapshot as spendable allowance. If compatibility cannot preserve the
original authority, fence/drain, retain audit state and report the interruption.
Rollback is not transparent live-parent migration.

## Verification

`go test -race ./test/integration -run TestMigrationInventory` exercises the
allowlist against credential/content canaries and a recording kubectl stand-in.
It checks exactly one GET, explicit namespace/context, no Secret query, preserved
parent identity and no proposed deletions. These are offline command-contract
tests, not an executed upgrade or evidence that effective grants are equivalent.
