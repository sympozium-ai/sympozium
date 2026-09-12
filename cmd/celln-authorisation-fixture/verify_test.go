package main

import (
	"path/filepath"
	"testing"
)

// TestConformanceFixtures verifies every committed v1 vector, recomputes the
// canonical bytes and digests, checks every Ed25519 signature and asserts each
// negative case fails for its named reason. It needs no cluster, KVM or
// network.
func TestConformanceFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "test", "fixtures", "celln-authorisation", "v1")
	if err := verifyFixtures(dir); err != nil {
		t.Fatalf("conformance fixtures failed: %v", err)
	}
}
