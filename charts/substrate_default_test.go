package charts

import (
	"os/exec"
	"strings"
	"testing"
)

// Where Substrate is enabled it is the default plane unless the operator
// says otherwise, and a release without it says nothing about a default.
func TestSubstrateDefaultPlaneSwitch(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for chart rendering")
	}
	on := []string{
		"--set", "substrate.enabled=true", "--set", "harnessGateway.enabled=true",
		"--set", "substrate.runnerImage=registry.example/agent-runner@sha256:abc",
		"--set", "substrate.controlAPI.endpoint=api.ate-system.svc:443",
		"--set", "substrate.jwt.issuer=https://issuer.example",
		"--set", "substrate.ca.configMap=substrate-ca",
		"--set", "substrate.controlAPI.tokenAudience=api.ate-system.svc",
	}
	render := func(args ...string) string {
		t.Helper()
		raw, err := exec.Command("helm", append([]string{"template", "test", "sympozium"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("render: %v: %s", err, raw)
		}
		return string(raw)
	}
	value := func(out string) string {
		_, after, found := strings.Cut(out, "- name: SUBSTRATE_DEFAULT\n")
		if !found {
			return ""
		}
		line, _, _ := strings.Cut(after, "\n")
		return strings.Trim(strings.TrimPrefix(strings.TrimSpace(line), "value:"), ` "`)
	}
	if got := value(render()); got != "" {
		t.Errorf("a release without Substrate sets SUBSTRATE_DEFAULT=%q", got)
	}
	if got := value(render(on...)); got != "true" {
		t.Errorf("substrate.enabled: SUBSTRATE_DEFAULT=%q, want true", got)
	}
	if got := value(render(append(on, "--set", "substrate.default=false")...)); got != "false" {
		t.Errorf("substrate.default=false: SUBSTRATE_DEFAULT=%q, want false", got)
	}
}
