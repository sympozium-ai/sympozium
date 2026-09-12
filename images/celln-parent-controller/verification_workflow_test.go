package parentcontroller_test

import (
	"os"
	"sigs.k8s.io/yaml"
	"strings"
	"testing"
)

func TestVerificationCannotRewriteReviewedContract(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/build.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Permissions map[string]string
			Steps       []struct{ Name, Run string }
		}
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	verify, ok := workflow.Jobs["verify"]
	if !ok || verify.Permissions["contents"] != "read" {
		t.Fatal("verification must have read-only repository credentials")
	}
	for _, step := range verify.Steps {
		if strings.Contains(step.Run, "git push") || strings.Contains(step.Run, "git commit") {
			t.Fatalf("verification publishes source changes: %s", step.Name)
		}
	}
}
