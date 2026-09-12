# Model gateway — work in progress (#502)

This package is not enabled in any binary, chart, or controller path. Do not
use this draft to enable mediated execution or mark #502 complete.

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

- Dedicated TLS binary, protected operator configuration, key loading/reload,
  readiness authority checks, build/image wiring and reviewed deployment boundary.
- Complete initial/turn registration integration: a later decision must reuse
  the original immutable run registration rather than register its new decision
  digest as the original run digest. Partial registration recovery needs tests.
- Atomic registered identity checks at reservation, cancellation/fencing of
  locally active requests, and concurrency/deadline ordering review.
- Provider response validation, usage uncertainty and output ceiling handling;
  never return raw provider failures containing credentials or unsafe headers.
- Full A/B credential isolation, source rotation/recreation, missing authority,
  registration authentication and real PostgreSQL concurrency/failure tests.
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
They are portable transport tests, not syscall tracing, real PostgreSQL,
installed TLS-provider, or KVM acceptance proof.
