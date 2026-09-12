package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationInventoryOnlyProjectsIdentities(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required for inventory filter tests")
	}
	input := `{"kind":"List","items":[
 {"kind":"ConfigMap","metadata":{"name":"grant","namespace":"tenant","uid":"grant-uid","annotations":{"secret":"CANARY"}},"data":{"token":"CANARY"}},
 {"kind":"AgentRun","metadata":{"name":"run","namespace":"tenant","uid":"run-uid"},"spec":{"task":"CANARY"},"status":{"result":"CANARY","cellnParent":{"binding":{"principal":"owner","incarnation":"original-incarnation"},"initialTurn":{"id":"turn-1","message":"CANARY","result":{"answer":"CANARY"}}}}},
 {"kind":"Pod","metadata":{"name":"dispatcher","namespace":"tenant","uid":"pod-uid"},"spec":{"containers":[{"name":"dispatcher","image":"example@sha256:pinned","env":[{"name":"TOKEN","value":"CANARY"}]}],"volumes":[{"name":"credentials","secret":{"secretName":"owner-secret"}}]}}
 ]}`
	cmd := exec.Command("jq", "-s", "-f", "../../scripts/lib/celln-migration-inventory.jq")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("inventory failed: %v %s", err, out)
	}
	if strings.Contains(string(out), "CANARY") {
		t.Fatal("inventory leaked data, env, annotations or user content")
	}
	for _, want := range []string{"grant-uid", "run-uid", "original-incarnation", "turn-1", "owner-secret", "example@sha256:pinned"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("lost identity %s", want)
		}
	}
	var report struct {
		MigrationAuthorized bool
		ProposedDeletions   []any
		Resources           []any
	}
	if json.Unmarshal(out, &report) != nil || report.MigrationAuthorized || len(report.ProposedDeletions) != 0 || len(report.Resources) != 3 {
		t.Fatalf("inventory authorized mutation or lost resources: %s", out)
	}
}

func TestMigrationInventoryNeverWritesClusterOrFetchesSecrets(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	config := filepath.Join(dir, "config")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	mock := `#!/bin/sh
printf '%s\n' "$*" >> "$CALL_LOG"
printf '%s\n' '{"kind":"List","items":[]}'
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "../../scripts/celln-migration-inventory.sh")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "CALL_LOG="+log, "CELLN_MIGRATION_KUBECONFIG="+config, "CELLN_MIGRATION_CONTEXT=reviewed", "CELLN_MIGRATION_NAMESPACE=tenant")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inventory command: %v %s", err, out)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	if strings.Count(calls, "\n") != 1 || !strings.Contains(calls, " get ") || !strings.Contains(calls, "--namespace tenant") || !strings.Contains(calls, "--context reviewed") || strings.Contains(calls, "secret") {
		t.Fatalf("unexpected cluster operations: %s", calls)
	}
}
