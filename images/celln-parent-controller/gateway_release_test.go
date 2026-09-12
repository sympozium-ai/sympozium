package parentcontroller_test

import (
	"os"
	"sigs.k8s.io/yaml"
	"slices"
	"testing"
)

func TestDedicatedGatewayPublishedByBuildAndRelease(t *testing.T) {
	for path, job := range map[string]string{"../../.github/workflows/build.yaml": "build-and-push", "../../.github/workflows/release.yaml": "release-images"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var workflow struct {
			Jobs map[string]struct {
				Strategy struct{ Matrix struct{ Image []string } }
			}
		}
		if err := yaml.Unmarshal(raw, &workflow); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(workflow.Jobs[job].Strategy.Matrix.Image, "model-gateway") {
			t.Fatalf("dedicated gateway missing from %s", path)
		}
	}
}
