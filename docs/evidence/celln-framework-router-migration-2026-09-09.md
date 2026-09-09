# Framework router migration and cache regression qualification

Date: 2026-09-09. Scope: the installed framework Kubernetes controller, host TLS
execution router, durable ownership ledger, upgraded dispatcher and real KVM.
This is qualification evidence, not a published-artifact or release claim.

## Installed changes

- Celln source: `df59b2b12ff09719769de7b4acd8f16db43142cb`.
- Execution TLS proxy source: Sympozium `055fe2c`.
- General controller: `055fe2c` plus the controller-runtime v0.20.4 cache fix.
  Qualification image index:
  `sha256:98b44ae0f288edc7f01d628723a8643f6b17e6917a17a797343963b789fd04e0`.
- Dispatcher and router listen only on loopback; the execution TLS endpoint is
  `https://192.168.1.237:19444`. Native parents retain their separate `:19443`
  endpoint and owner state.
- Execution, discovery and backend tokens were separated and rotated. The API
  server receives only discovery authority. Model credentials remain host-side.
- The original dispatcher state, receipts, binary, credentials and service were
  privately backed up while stopped. Both legacy DaemonSets are quiesced. The
  original state root remains in use; no journal or authority was reset.

## Confirmed results

| Check | Observed result |
| --- | --- |
| Direct one-shot, real DeepSeek forge and KVM execution | AgentRun `release-celln-oneshot-qualified-20260909`, UID `58121405-23b4-41a9-ad09-e9b5fc8e898f`, returned `CELLN_RELEASE_ONESHOT_OK` |
| Actual execution identity | Cell `6186300f2bb7`; host execution 11:09:51–11:10:05 UTC, controller succeeded at 11:10:06 |
| Router restart | Exact original receipt and cell identity retained |
| Repeated original-ID POST | No new execution; exact original receipt retained |
| Live-cell cancellation | `release-celln-cancel-retry-20260909` reached an observed running cell, was cancelled, returned its matching cancelled receipt, and completed normal finalizer removal with no remaining one-shot cells |
| OCI Pi Harness one-shot after cache fix | `release-pi-oneshot-20260909-zpl66`, UID `7cc05749-4ac9-4274-86a1-82d020888205`, persisted `OCI_HARNESS_RELEASE_OK` and its pod name; succeeded 11:42:09 UTC |
| Native enduring regression after migration | `celln-agent-pvcm2`, UID `3a89e1ef-42d6-4ba1-9220-e64559e666e3`, remembered `amber` in its initial turn, recalled it in a subsequent turn, then completed normal deletion |
| Ordinary Kubernetes agent after cache fix | `release-ordinary-cache-fixed-20260909` persisted `ORDINARY_AGENT_RELEASE_OK` and its pod identity |
| Local regression | `go test ./...`, targeted vet, and race tests for controller, reconciliation and TLS proxy passed |
| Local installed UI access | HTTP 200 through the framework service tunnel on `127.0.0.1:38083` |

The OCI adapter is pinned to
`sha256:b0d50402dc0a25b2c46f86dbd6b5de963487fa0ffa44058cb324ab2c68934f3b`.
Its exact-image test policy is separate from the existing interactive policy;
the latter correctly rejected the initial one-shot request before persistence.

The first cancellation trial failed at Rust compilation (model-generated code
omitted the trait import for stdout flushing). No running cell was cancelled in
that trial. The successful subsequent trial used a minimal sleep program and
waited for an actual live cell before deleting its AgentRun.

## Regression found and fixed

The v0.20.0 controller-runtime all-namespace cache accepted namespace exclusions
but rejected namespace-scoped List calls. This broke pod discovery and resulted
in successful OCI runs without their recorded output. The v0.20.4 patch restores
the all-namespace fallback. A real informer/cache HTTP regression test verifies
included-namespace lists, excluded-namespace isolation, all-namespace lists and
unfiltered cluster-scoped resources. The successful post-fix trial asserts the
AgentRun result, not just pod logs. The pre-fix run was not rewritten.

## Retention and remaining gates

Detailed run/receipt evidence is retained privately under
`target/framework-native/evidence/`; host backups are private under
`/var/lib/celln-migration-backup-20260909/`. Neither contains a release artifact
to copy into container build contexts. Do not commit private evidence or keys.

This does not establish a published combined native-controller image, a
downloadable qualified host package, automatic certificate renewal or reboot
recovery. Qualification certificates are short-lived. The host services are not
yet enabled as a production boot installation. Old failed native journals without
process identity still require conservative operator reconciliation; no manual
finalizer removal is claimed as cleanup. See the
[migration guide](../guides/celln-external-router-migration.md).
