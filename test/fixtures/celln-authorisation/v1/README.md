# Celln namespace-authorisation conformance bundle

`manifest.json` is the authoritative, non-empty inventory. `cases.json.gz` contains compact signed vectors and stateful accounting sequences. `bundle/SHA256SUMS` covers the manifest, cases, schemas, signing vectors and READMEs; `BUNDLE.sha256` is the external pin. Run `go run ./cmd/celln-authorisation-fixture verify -fixtures test/fixtures/celln-authorisation/v1`.
