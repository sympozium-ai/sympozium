package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestTenancyRunnerRequiresActualRelayAndEveryNegativeCase(t *testing.T) {
	raw, err := os.ReadFile("test-celln-tenancy-security.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(raw), "run_case required-relay-cases jq -s -e '")
	if !ok {
		t.Fatal("missing exact relay evidence gate")
	}
	filter, _, ok := strings.Cut(rest, "' \"$out/")
	if !ok {
		t.Fatal("missing relay evidence input")
	}
	var events []map[string]string
	for _, root := range []string{"TestPostgresGatewayTenantCredentialIsolation", "TestLiveKubernetesGatewayTenantCredentialIsolation", "TestLiveKubernetesGatewayProcessTenantCredentialIsolation"} {
		for _, tenant := range []string{"a", "b"} {
			for _, suffix := range []string{"", "/untrusted-ca", "/redirect", "/credential-echo", "/cancellation"} {
				events = append(events, map[string]string{"Action": "pass", "Test": root + "/" + tenant + "/RustHostRelay" + suffix})
			}
		}
	}
	for _, count := range []int{len(events), len(events) - 1, 0} {
		var data bytes.Buffer
		for _, event := range events[:count] {
			if err := json.NewEncoder(&data).Encode(event); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("jq", "-s", "-e", filter)
		cmd.Stdin = &data
		err := cmd.Run()
		if (err == nil) != (count == len(events)) {
			t.Fatalf("relay evidence gate accepted missing cases or rejected full set: count=%d err=%v", count, err)
		}
	}
}

func TestTenancyRunnerCannotImplyReleaseOrUseAmbientKubeconfig(t *testing.T) {
	for _, tc := range []struct{ arg, want string }{{"--release", "Use --components or --local-kvm"}, {"--components", "explicit isolated kubeconfig required"}} {
		cmd := exec.Command("bash", "./test-celln-tenancy-security.sh", tc.arg)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "KUBECONFIG=/ambient/must-not-be-used"}
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), tc.want) {
			t.Fatalf("runner failed targeting guard: %v %s", err, output)
		}
	}
}
