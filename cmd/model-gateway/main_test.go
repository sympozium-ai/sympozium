package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretConfigurationFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("operator-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := readSecret(path); err != nil || got != "operator-canary" {
		t.Fatal("protected file failed")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecret(path); err == nil || strings.Contains(err.Error(), "operator-canary") {
		t.Fatal("unsafe file accepted or leaked")
	}
	if err := run(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing configuration accepted")
	}
}
func TestPublicKeyLoaderRejectsPrivateAndRemoteMaterial(t *testing.T) {
	raw, err := os.ReadFile("../../test/fixtures/celln-authorisation/v1/signing/test-jwks.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keys")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if keys, err := loadKeys(path); err != nil || len(keys) != 2 {
		t.Fatalf("test public keys: %v", err)
	}
	for _, field := range []string{`"d":"private-canary",`, `"jku":"https://attacker.invalid/keys",`} {
		bad := strings.Replace(string(raw), `"kty":`, field+`"kty":`, 1)
		if err = os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = loadKeys(path); err == nil {
			t.Fatal("private or remote authority accepted")
		}
	}
}
