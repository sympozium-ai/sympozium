package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

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
