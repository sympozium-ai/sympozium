package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTLSRenewalPreservesKeyAndOnlyRestartsEdges(t *testing.T) {
	for _, binary := range []string{"openssl", "flock", "bash"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " required")
		}
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("openssl", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("openssl: %v: %s", err, out)
		}
	}
	run("req", "-x509", "-newkey", "ed25519", "-nodes", "-subj", "/CN=test-ca", "-days", "365", "-keyout", "ca.key", "-out", "ca.crt")
	run("req", "-x509", "-newkey", "ed25519", "-nodes", "-subj", "/CN=192.0.2.1", "-days", "1", "-keyout", "tls.key", "-out", "tls.crt")
	keyBefore, err := os.ReadFile(filepath.Join(dir, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RESTART_LOG\"\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	renew := func() ([]byte, error) {
		cmd := exec.Command("bash", "renew-celln-tls.sh", dir, "192.0.2.1")
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "RESTART_LOG="+filepath.Join(dir, "restarts"))
		return cmd.CombinedOutput()
	}
	if out, err := renew(); err != nil {
		t.Fatalf("renew: %v: %s", err, out)
	}
	run("verify", "-CAfile", "ca.crt", "-verify_ip", "192.0.2.1", "tls.crt")
	run("x509", "-in", "tls.crt", "-checkend", "5184000", "-noout")
	keyAfter, _ := os.ReadFile(filepath.Join(dir, "tls.key"))
	if string(keyBefore) != string(keyAfter) {
		t.Fatal("renewal changed the key")
	}
	certBefore, _ := os.ReadFile(filepath.Join(dir, "tls.crt"))
	if out, err := renew(); err != nil {
		t.Fatalf("no-op renewal: %v: %s", err, out)
	}
	certAfter, _ := os.ReadFile(filepath.Join(dir, "tls.crt"))
	if string(certBefore) != string(certAfter) {
		t.Fatal("unnecessary renewal")
	}
	restarts, err := os.ReadFile(filepath.Join(dir, "restarts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(restarts)) != "try-restart sympozium-celln-native-proxy.service sympozium-celln-router-proxy.service" {
		t.Fatalf("unexpected restarts: %s", restarts)
	}
	// A near-expiry CA must refuse, not silently replace client trust.
	run("req", "-x509", "-key", "ca.key", "-subj", "/CN=test-ca", "-days", "1", "-out", "ca.crt")
	if _, err := renew(); err == nil {
		t.Fatal("near-expiry CA accepted")
	}
}
