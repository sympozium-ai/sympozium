# Model gateway — work in progress (#502)

The dedicated `cmd/model-gateway` binary exists but is not enabled in any chart
or controller path. Do not use this draft to mark #502 complete.

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

- Readiness authority checks and reviewed deployment boundary.
- Initial/turn registration now retains the original decision and checks immutable
  run/route/parent/ceilings, but partial registration recovery and enduring
  PostgreSQL integration still need tests.
- Atomic registered identity checks at reservation, cancellation/fencing of
  locally active requests, and concurrency/deadline ordering review.
- Provider response validation, usage uncertainty and output ceiling handling;
  never return raw provider failures containing credentials or unsafe headers.
- Extend the real PostgreSQL A/B recording-provider test (same-name connections,
  distinct credentials, same-UID rotation, replacement refusal, wrong audience,
  duplicate suppression) with authority outages, full HTTP authentication and
  crash/concurrency tests.
- Shared model-request canonicalisation review with Celln. The current body
  digest must not be represented as reviewed cross-language protocol evidence.
- KVM/installed proof and paired Celln #500/#501 protocol dependencies.

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
