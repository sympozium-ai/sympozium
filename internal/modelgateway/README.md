# Model gateway — work in progress (#502)

The dedicated `cmd/model-gateway` binary has disabled-by-default component
packaging in dependent PR #522, but no completed mediated controller/Celln path.
Do not use this draft to mark #502 complete.

Implemented so far:

- A separate HTTP handler with distinct issuer transport authentication and
  execution-permit registration; invocation accepts model-audience credentials.
- Live namespaced route/source reads and immutable source UID comparison.
- PostgreSQL secret-free authority metadata (migration 003, after 002).
- Reservation-before-forwarding without automatic retry or redirect following.
- Connect-time DNS answer validation and dialing of a validated literal IP;
  public/private mixed answers refuse unless the exact origin is approved.
- Plain HTTP is confined to explicitly approved loopback destinations. HTTPS
  always verifies certificates; tenant AllowInsecure does not disable TLS checks.
- Bounded input/output, strict duplicate-key parsing, and reason-only HTTP errors.

## Remaining blockers (not acceptance evidence)

- Reviewed deployment boundary and least-privilege installed evidence.
- Broader route-read outage/deadline failure injection and process-level recovery
  (the publication-boundary tests inject store failures, not OS process crashes).
- Complete response protocol compatibility and usage/output ceiling failure
  injection across both supported providers.
- Shared model-request canonicalisation review with Celln. The current body
  digest must not be represented as reviewed cross-language protocol evidence.
- KVM/installed proof and paired Celln #500/#501 protocol dependencies.

## Integrated accounting and recovery checks

Gateway reservation now requires `ReserveBound`: cluster, namespace UID, run UID,
route digest and exact turn decision are compared under the same PostgreSQL row
locks used to charge allowance, before returning even a duplicate request.

Both fake-client/PostgreSQL and live Kubernetes/TLS tests interrupt registration
after run publication and after turn publication, recover through new pools,
race eight identical registrations, and refuse a changed-cap retry. HTTP checks
require both issuer transport and execution audience; model tokens cannot register.
Enduring tests charge the initial task and a follow-up against the same run,
retain reserved/observed totals, refuse maxTurns overflow and budget top-up, and
prove child-scoped cleanup closes only its turn, not the parent budget.

Provider envelopes are strict non-streaming JSON objects. Duplicate accounting
keys, invalid usage, error envelopes, and exact credential echoes in decoded
strings/keys refuse. Unknown usage remains SQL NULL, including lost responses;
no failure is recorded as measured zero or refunded. Response headers are not
relayed. These checks do not claim to detect arbitrary encodings of credentials.

## Credential custody

The intended dedicated gateway identity needs explicit Secret `get` access,
not Secret list/watch. Cluster-wide get is cluster-wide credential privilege,
regardless of application reference checks. This does not remove the existing
controller's unrelated Secret permissions. No provider key belongs in Celln
host configuration or tenant authority metadata.

## Tests executed during this draft

```
go test -race ./internal/modelgateway ./internal/cellncapability
go vet ./internal/modelgateway
```

The tests include actual loopback HTTP connections and redirect refusal, plus
injected DNS answers asserting the dial receives only the validated literal IP.
They are portable transport tests, not syscall tracing, installed TLS-provider,
or KVM acceptance proof. PostgreSQL tests require
`CELLN_MODEL_BUDGET_DATABASE_URL`; without it they explicitly skip.

## Active cancellation and live readiness

`POST /internal/close` requires both issuer transport authentication and a
separately signed `execution.cleanup` permit in `X-Celln-Execution-Permit`.
It checks durable original run/namespace/parent ownership, then fences the run.
It does not read live policy or Secrets, so withdrawn execution authority or a
deleted credential source cannot strand cleanup. This is gateway budget closure,
not confirmation that a native Celln parent has been torn down.

Each admitted provider request polls its durable run/turn fence every 250 ms
with a one-second query timeout. A fence or accounting read failure cancels its
local HTTP context. Replicas observe the same database fence. This is bounded
best-effort cancellation, not immediate remote revocation or a spending refund;
already-sent requests may still be processed by the provider. Original work
and HTTP deadlines remain independent upper bounds. Watchers are joined and
released when a request completes.

Readiness now checks live SelfSubjectAccessReviews for cluster-wide `get` on
namespaces, ModelConnections and Secrets, plus both database schemas. Denial,
API failure or ambiguous evaluation fails closed. This reads no Secret data and
does not add Secret list/watch permissions. It reports the real custody privilege
rather than mislabelling it namespace-isolated RBAC.

Tests exercise two independently connected stores, run and turn closure while
an HTTP provider is active, cancellation on accounting outage, unchanged reserved
usage, and live Kubernetes readiness/cleanup after Secret replacement. Run the
opt-in live Make target documented in
`docs/design/celln-gateway-live-epoch.md` for the Kubernetes/TLS tier.

## Actual binary restart tier

`TestLiveKubernetesGatewayProcessTenantCredentialIsolation` replaces the test HTTP
listener with a separately built `cmd/model-gateway`. It exercises real startup,
owner-only files, public JWKS, verified TLS, PostgreSQL and live Kubernetes reads.
For both tenants, registration/invocation/rotation/cleanup use the child process;
after the first provider request it stops and restarts that process with the same
key material/database, then requires duplicate suppression and original allowance.
No ambient provider credentials are passed to its environment. The publication
fault and enduring helpers remain in-process checks, not simulated OS crashes.

Build the binary, then explicitly set `CELLN_GATEWAY_PROCESS=1`,
`CELLN_GATEWAY_TEST_BINARY=/absolute/path/to/model-gateway`,
`CELLN_GATEWAY_LIVE_KUBERNETES=1`, `KUBECONFIG` and the disposable database URL:

```
go test -race ./internal/modelgateway -run '^TestLiveKubernetesGatewayProcessTenantCredentialIsolation$' -count=1 -v
```

This is a real gateway process/API test, not a deployed Celln/controller/browser
journey or proof of least-privilege Kubernetes authorization.

## Dedicated process

`make build-model-gateway` builds a TLS-only server. Pass `--config` with an
operator-controlled JSON file containing `clusterId`, `issuer`, `listen`,
`tlsCertificateFile`, `tlsKeyFile`, `verificationKeysFile`,
`registrationTokenFile`, `databaseUrlFile`, and optional `privateOrigins`.
No bearer value is accepted on the command line. Registration/database files
must be owner-only regular files. The keyset is public Ed25519 JWKS only;
private keys and remote key references refuse. SIGHUP atomically reloads a valid
public keyset; invalid reloads retain the previous trusted set. Operators must
retain old keys for all outstanding lifetimes plus skew. Emergency removal
refuses future verification, not already admitted Celln execution.

Apply migrations 002 and 003 before startup. There is no automatic migration or
in-memory fallback. This process does not yet constitute reviewed installation
(#506) or Celln/controller integration (#504/#505).
