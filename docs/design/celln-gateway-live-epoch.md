# Model gateway live-API checkpoint (#502)

This is component integration evidence, **not epic #495 completion or installed
Celln execution proof**. The gateway ran in the Go test process, using the live
Kubernetes API, an isolated PostgreSQL 17 container, and verified-TLS gateway and
provider listeners. No controller deployment or production CRD was changed.

## Reproduce

Required: installed ModelConnection CRD, a disposable PostgreSQL database with
schema creation permission, and an explicitly selected kubeconfig allowed to
create/delete temporary namespaces, ModelConnections and Secrets.

```
CELLN_GATEWAY_LIVE_KUBERNETES=1 \
CELLN_MODEL_BUDGET_DATABASE_URL='postgres://...' \
make test-celln-model-gateway-live
```

The target fails when either opt-in is absent. Ordinary unit tests never access
the cluster implicitly. The test uses unique `celln-gateway-test-*` namespaces,
registers cleanup immediately after creation, and waits for actual deletion.
Use a disposable database: migrations and test accounting rows are retained.

## Observed checkpoint

Caller context: `kubernetes-admin@kubernetes`. This tests application-level
namespace/source binding, **not tenant RBAC or submitter attribution**.

Successful race-enabled live run (exit 0):

| Tenant | Namespace UID | ModelConnection UID | Original Secret UID |
| --- | --- | --- | --- |
| A | `42ebe77a-b7b1-4fc5-b104-c674d7bbfa1e` | `ddd29321-47d3-40fd-8e1e-05fa30a0b849` | `0d85767c-6110-41e7-9869-73481846a39f` |
| B | `e67137b7-9d90-4419-86d3-3afaf1b9b256` | `8620075f-a140-4aa5-b3fc-3ce1d69389ef` | `653e5b6b-58e9-4857-ae0d-7a85eefdd0bb` |

Each tenant used connection `model` and Secret `key`. Assertions passed:

- Execution-audience credential cannot invoke a model.
- Initial provider request receives only the selected tenant credential.
- Duplicate request cannot dispatch again.
- Secret data rotation under the original UID is used by the second call.
- Delete/recreate under the same Secret name refuses before provider contact.
- Caller forwarding/permit headers never reach the provider.
- Exactly two recorded provider calls per tenant; no unexpected calls remain.
- Both temporary namespaces were confirmed deleted.

TLS trust was explicit for generated test certificates; neither gateway nor
provider test disabled certificate verification. PostgreSQL was isolated from
cluster workloads. Provider/gateway credentials are not included in this record.

Remaining proof includes enduring recovery, concurrent cancellation/closure,
authority outages, dedicated-process installation, paired Celln admission and
broker work, KVM journeys, real-provider smoke and release qualification.
