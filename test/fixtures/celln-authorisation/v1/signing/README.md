# Non-production conformance signing keys

These keys exist only so the v1 conformance fixtures are deterministic and
independently reproducible by Sympozium and Celln. They are **not secrets** and
must never be trusted by a running system.

* `test-private-keys.json` — deterministic Ed25519 seeds used by the fixture
  generator (`go run ./cmd/celln-authorisation-fixture gen`).
* `test-jwks.json` — the corresponding public keys (RFC 8037 `OKP`/`Ed25519`)
  used by the validator.

| `kid` | Purpose |
| --- | --- |
| `test-key-1` | default fixture signer |
| `test-key-2` | rotation-acceptance vector (`rotation-key-2`) |

A production deployment must configure its own issuer private key outside the
repository and distribute only its public keys to verifiers. The test issuer
name `sympozium-control-plane` and these `kid`s must be rejected outside
conformance tests.
