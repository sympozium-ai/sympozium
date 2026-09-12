package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

var requiredVectorNames = []string{"direct-one-shot", "harness-one-shot", "model-after-admission-window", "issued-permit-after-policy-withdrawal", "parent-create", "enduring-turn", "enduring-model-after-admission-window", "cleanup-after-policy-withdrawal", "request-content-changed", "wrong-endpoint-audience", "admission-expired", "model-work-deadline-expired", "model-token-beyond-work-deadline", "budget-id-mismatch", "namespace-uid-mismatch", "parent-turn-mismatch", "model-route-mismatch", "admission-replay-recovers", "invalid-admission-window", "turn-cap-exceeds-run-cap", "streaming-unsupported", "rotation-key-2", "unknown-kid", "tampered-signature", "resolver-policy-valid", "resolver-policy-removed", "resolver-artifact-contracted", "resolver-https-host-contracted", "resolver-tool-reordered"}
var requiredSequenceNames = []string{"model-budget-idempotency-across-turns"}

func generateFixtures(dir string) error {
	_ = os.RemoveAll(filepath.Join(dir, "vectors"))
	if err := os.MkdirAll(filepath.Join(dir, "signing"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "bundle"), 0755); err != nil {
		return err
	}
	pk := testPrivateKeys()
	if err := os.WriteFile(filepath.Join(dir, "signing", "test-private-keys.json"), marshalIndent(pk), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "signing", "test-jwks.json"), marshalIndent(deriveJWKS(pk)), 0644); err != nil {
		return err
	}
	if err := writeSchemas(dir); err != nil {
		return err
	}
	cases := buildCases()
	if err := writeGzipJSON(filepath.Join(dir, "cases.json.gz"), cases); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(dir, "cases.json"))
	manifest := Manifest{APIVersion: "celln.sympozium.ai/conformance-manifest-v2", Contract: "docs/design/celln-namespace-authorisation.md", VectorNames: append([]string{}, requiredVectorNames...), SequenceNames: append([]string{}, requiredSequenceNames...)}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), marshalIndent(manifest), 0644); err != nil {
		return err
	}
	readme := "# Celln namespace-authorisation conformance bundle\n\n`manifest.json` is the authoritative, non-empty inventory. `cases.json.gz` contains compact signed vectors and stateful accounting sequences. `bundle/SHA256SUMS` covers the manifest, cases, schemas, signing vectors and READMEs; `BUNDLE.sha256` is the external pin. Run `go run ./cmd/celln-authorisation-fixture verify -fixtures test/fixtures/celln-authorisation/v1`.\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0644); err != nil {
		return err
	}
	sreadme := "# Test signing keys\n\nThese deterministic Ed25519 keys are **NON-PRODUCTION** conformance material. Production verifiers must use configured cluster trust and must never trust these keys.\n"
	if err := os.WriteFile(filepath.Join(dir, "signing", "README.md"), []byte(sreadme), 0644); err != nil {
		return err
	}
	return writeBundle(dir)
}

func writeGzipJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := zw.Write(marshalIndent(v)); err != nil {
		_ = zw.Close()
		return err
	}
	return zw.Close()
}

type sumEntry struct{ rel, hash string }

func writeBundle(dir string) error {
	sums, err := computeSums(dir)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	for _, s := range sums {
		fmt.Fprintf(&b, "%s  %s\n", s.hash, s.rel)
	}
	if err := os.WriteFile(filepath.Join(dir, "bundle", "SHA256SUMS"), b.Bytes(), 0644); err != nil {
		return err
	}
	h := sha256.Sum256(b.Bytes())
	return os.WriteFile(filepath.Join(dir, "bundle", "BUNDLE.sha256"), []byte("sha256:"+hex.EncodeToString(h[:])+"\n"), 0644)
}
func computeSums(dir string) ([]sumEntry, error) {
	var out []sumEntry
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if rel == "bundle/SHA256SUMS" || rel == "bundle/BUNDLE.sha256" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h := sha256.Sum256(data)
		out = append(out, sumEntry{rel: rel, hash: hex.EncodeToString(h[:])})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, err
}
