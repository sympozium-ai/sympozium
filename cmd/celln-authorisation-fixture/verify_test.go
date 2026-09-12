package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAndVerify(t *testing.T) {
	dir := t.TempDir()
	if err := generateFixtures(dir); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "bundle", "BUNDLE.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyFixtures(dir); err != nil {
		t.Fatal(err)
	}
	if err := generateFixtures(dir); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "bundle", "BUNDLE.sha256"))
	if string(first) != string(second) {
		t.Fatalf("generation not deterministic: %s != %s", first, second)
	}
}
func TestCommittedFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "test", "fixtures", "celln-authorisation", "v1")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("committed fixture tree not present in isolated package test")
	}
	if err := verifyFixtures(dir); err != nil {
		t.Fatal(err)
	}
}
func TestStrictCanonicalInput(t *testing.T) {
	bad := []string{`{"a":-0}`, `{"a":1,"a":2}`, `{"a":"\ud800"}`, `{"a":1}{"b":2}`, `{"a":1.0}`, `{"a":1e2}`}
	for _, in := range bad {
		if _, err := canonicalizeJSON([]byte(in)); err == nil {
			t.Errorf("expected refusal for %s", in)
		}
	}
	good := `{"b":2,"a":"é"}`
	got, err := canonicalizeJSON([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":"é","b":2}` {
		t.Fatalf("got %s", got)
	}
}
func TestParseCLI(t *testing.T) {
	cmd, dir, err := parseCLI([]string{"verify", "-fixtures", "/tmp/x"})
	if err != nil || cmd != "verify" || dir != "/tmp/x" {
		t.Fatalf("got %q %q %v", cmd, dir, err)
	}
}
func TestLifecycleRegressionCases(t *testing.T) {
	c := buildCases()
	activeJWKS = deriveJWKS(testPrivateKeys())
	want := map[string]Expect{"model-after-admission-window": {Outcome: "accept"}, "cleanup-after-policy-withdrawal": {Outcome: "accept"}, "request-content-changed": {Outcome: "reject", Reason: ReasonRequestBinding}, "model-work-deadline-expired": {Outcome: "reject", Reason: ReasonDeadlineExpired}, "admission-replay-recovers": {Outcome: "recover", Reason: ReasonAdmissionReplay}}
	for _, v := range c.Vectors {
		e, ok := want[v.Name]
		if !ok {
			continue
		}
		raw, _ := jsonMarshal(v.Decision)
		o, r, err := Verify(v.Credential, raw, *v.Verify)
		if err != nil || o != e.Outcome || r != e.Reason {
			t.Fatalf("%s got %s/%s %v", v.Name, o, r, err)
		}
		delete(want, v.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing cases %v", want)
	}
}
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
