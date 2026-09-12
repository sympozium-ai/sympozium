# Celln release evidence validation (#510)

`go run ./cmd/celln-tenancy-evidence --manifest /path/to/manifest.json`
validates evidence structure and referenced file hashes. **It is not the installed
release runner, hardware attestation, or a passing #510 gate.** Exit zero means
only that the submitted structure is valid, including explicitly incomplete rows.
The output always labels qualification `unverified`; even `allRowsClaimPass: true`
is a statement about submitted claims, not independent execution verification.

## Manifest contract

Top-level fields (unknown and duplicate keys are rejected):

- `apiVersion`: `sympozium.ai/celln-tenancy-evidence-v1`.
- `sympoziumSource`, `cellnSource`: exact 40-character lower-case source SHAs.
- `fixtureDigest`: `sha256:` plus 64 lower-case hex characters.
- `images`: `sympozium` and `celln` image digests, required for any claimed pass.
- `rows`: exactly one row each for A01 through A12 from #495.

Each row contains `id`, named `test`, `tier` (`installed` or `not-executed`),
`status` (`pass`, `fail`, `incomplete`), nonempty `expected` and `actual`
observations, `command`, nullable `exitCode`, and `artifacts`.
A pass requires installed tier, a command, exit zero and at least one artifact.
Missing prerequisites are incomplete, not a passing skip. Each artifact contains
relative `path` and `sha256` digest. Paths resolve within the manifest directory;
absolute/traversing paths, escaping symlinks, missing/non-regular files and hash
mismatches refuse validation. Limits: 1 MiB manifest, 16 references per row,
4 MiB per artifact. Split large logs into bounded, separately hashed artifacts.

## What is not validated

A digest proves bytes match a submitted digest, not that their contents are true.
This tool does not interpret receipts, correlate owner/parent/child/request IDs,
verify provider counts against the ledger, attest image deployment, check cluster
architecture/KVM/CNI, execute browser journeys or scan every credential surface.
Images are collection-level pins, not a complete component deployment inventory.
Those details and exact surface coverage must be retained in referenced artifacts
and independently reviewed against the actual installed tests.

No release evidence document is published by this change: installed journeys are
blocked by unfinished prerequisites. No credentials are discovered, no provider
calls are made, and no Kubernetes resources are touched. The final release runner
must execute the real journeys and establish provenance, not merely invoke this
validator or trust an author-supplied `pass` string.

## Tests

`go test -race ./internal/cellnevidence ./cmd/celln-tenancy-evidence` checks
synthetic structural fixtures, explicit incompleteness, duplicate/missing rows,
mock/skip refusal, nonzero/missing exit codes, missing commands/artifacts,
traversal/symlink escape, digest mismatch and mutable image pins. Synthetic fixture
logs are explicitly labelled and are never published as installed evidence.
