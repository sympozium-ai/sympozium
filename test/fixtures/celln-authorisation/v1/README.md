# Celln namespace-authorisation v1 conformance fixtures

Normative fixtures for
[`docs/design/celln-namespace-authorisation.md`](../../../../docs/design/celln-namespace-authorisation.md),
delivered under epic #495 / issue #496.

These fixtures freeze the decision/credential wire contract, canonical encoding
and expected refusal reasons so Sympozium and Celln implement one protocol
instead of inventing divergent ones. **This directory contains no runtime
enforcement code.**

> ⚠️ **Fixture keys are NON-PRODUCTION.** `signing/test-private-keys.json`
> contains deterministic Ed25519 seeds and `signing/test-jwks.json` their public
> keys. A real deployment must never trust `test-key-1` / `test-key-2`. See
> [`signing/README.md`](signing/README.md).

## Layout

```
v1/
├── manifest.json                  # authoritative expected outcomes + pinned bundle hash
├── schema/
│   ├── decision.schema.json       # JSON Schema 2020-12 for the decision
│   └── credential.schema.json     # JSON Schema 2020-12 for the JWS claims
├── signing/
│   ├── test-jwks.json             # non-production Ed25519 public keys
│   ├── test-private-keys.json     # non-production seeds (generator only)
│   └── README.md
├── bundle/
│   ├── SHA256SUMS                 # sha256 of every fixture file
│   └── BUNDLE.sha256              # sha256 of the sorted SHA256SUMS manifest (pinned hash)
└── vectors/<name>/
    ├── decision.json              # the decision document
    ├── decision.canonical         # exact RFC 8785 canonical bytes
    ├── decision.digest            # sha256 of decision.canonical
    ├── credential.jws             # compact Ed25519 JWS
    ├── observed.json              # live context the verifier compares against
    └── expect.json                # {accept} or {reject, reason}
```

The **pinned shared hash** for the Celln consumer is the value in
`bundle/BUNDLE.sha256`, also recorded as `bundleHash` in `manifest.json`:

```
sha256:87703a8b4e6727e395918fef84d8e9509376b3316dadd5d3310e3e40c3441548
```

Celln's consumer must vendor this bundle **by hash**. Do not copy the vectors
and let them drift; a contract change updates the schemas, vectors,
`SHA256SUMS` and `BUNDLE.sha256` together in review.

## Vectors

41 vectors (9 accept, 32 reject). Highlights:

| Category | Vectors |
| --- | --- |
| Accept | `one-shot-direct-no-model`, `harness-one-shot`, `parent-create`, `enduring-initial-turn`, `enduring-subsequent-turn`, `owner-read`, `owner-cleanup`, `model-invoke`, `rotation-key-2` |
| Identity | `namespace-uid-mismatch`, `run-uid-mismatch`, `parent-incarnation-mismatch`, `turn-id-mismatch`, `subject-mismatch`, `route-mismatch`, `credential-source-mismatch` |
| Authority | `operation-mismatch`, `audience-mismatch`, `issuer-mismatch`, `policy-contracted`, `policy-removed`, `limit-zero`, `limit-missing`, `limit-out-of-range`, `tool-reordered`, `tool-revision-unknown` |
| Crypto/token | `unknown-algorithm`, `unknown-kid`, `tampered-signature`, `stale-decision-digest`, `duplicate-json-key`, `unknown-payload-field`, `oversized-credential`, `unknown-version`, `expired-token`, `future-token` |
| Budget/lifecycle | `budget-id-mismatch`, `request-binding-mismatch`, `streaming-unsupported`, `protocol-unsupported`, `duplicate-decision` |

Every reject vector changes exactly one authoritative binding (or one
credential/encoding property) and must be refused for exactly the named reason
in `manifest.json`.

## Run the validator

```
go run ./cmd/celln-authorisation-fixture verify -fixtures test/fixtures/celln-authorisation/v1
```

or:

```
make test-celln-authorisation-contract
```

### Last recorded result (command, exit code, environment)

```
$ go run ./cmd/celln-authorisation-fixture verify -fixtures test/fixtures/celln-authorisation/v1
...
41/41 vectors passed; bundle sha256:87703a8b4e6727e395918fef84d8e9509376b3316dadd5d3310e3e40c3441548
$ echo $?
0
```

The validator checks, in order and failing closed on the first violation:

1. credential/decision size bounds;
2. strict compact-JWS parsing (three base64url segments, duplicate-key and
   unknown-field rejection, protected-header bound);
3. `alg`/`typ`;
4. decision `apiVersion`;
5. configured `kid`;
6. Ed25519 signature over the signing input;
7. JCS canonical decision bytes, decision digest and `decisionDigest` claim;
8. fixed issuer;
9. `iat`/`nbf`/`exp`, 5 s skew and 60 s admission window;
10. audience for the operation;
11. claim bindings (operation, budget ID, subject run/turn/parent);
12. non-self-referential `requestBinding`;
13. namespace name/UID;
14. run UID/spec;
15. parent incarnation/turn;
16. model route;
17. credential source;
18. live policy presence/digest;
19. policy contraction;
20. limit ranges;
21. tool order and approved revisions;
22. protocol and streaming refusal;
23. replay/duplicate decision.

It also verifies `bundle/SHA256SUMS` against every committed file and
`BUNDLE.sha256` against the sorted manifest before trusting any expectation.

## Regenerate

```
go run ./cmd/celln-authorisation-fixture gen -fixtures test/fixtures/celln-authorisation/v1
```

Generation is deterministic: running it twice produces the same
`BUNDLE.sha256`. Regenerate only through review. It writes only into the
fixture tree; it does not touch production keys or cluster state.

## Unresolved incompatibilities

The contract records ten explicit incompatibilities with the current Sympozium
and Celln implementations, including static bearer credentials (no JWS/audience)
in Celln 0.5.10, non-canonical hashing in both repos, the `sha256`/`blake3`
family mismatch, the absent durable budget ledger, and the absent dedicated
model gateway. See
[§12 of the contract](../../../../docs/design/celln-namespace-authorisation.md#12-recorded-incompatibilities-with-current-implementations).

Missing KVM is irrelevant to this contract-only task. No runtime or
installed-security acceptance is claimed here.
