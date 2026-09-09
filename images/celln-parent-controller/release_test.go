package parentcontroller_test

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestCellnReleaseDependencyIsPinned(t *testing.T) {
	raw, err := os.ReadFile("celln-release.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		Version       string
		ArchiveSHA256 string
		ImageDigest   string
	}
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	for value, expression := range map[string]string{pin.Version: `^v[0-9]+\.[0-9]+\.[0-9]+$`, pin.ArchiveSHA256: `^[a-f0-9]{64}$`, pin.ImageDigest: `^sha256:[a-f0-9]{64}$`} {
		if !regexp.MustCompile(expression).MatchString(value) {
			t.Fatalf("invalid release pin %q", value)
		}
	}
}

func TestReleasePublishesEveryStandardBuildImage(t *testing.T) {
	images := func(path, job string) []string {
		t.Helper()
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
		result := workflow.Jobs[job].Strategy.Matrix.Image
		if len(result) == 0 {
			t.Fatal("missing image matrix")
		}
		return result
	}
	released := map[string]bool{}
	for _, name := range images("../../.github/workflows/release.yaml", "release-images") {
		released[name] = true
	}
	for _, name := range images("../../.github/workflows/build.yaml", "build-and-push") {
		if !released[name] {
			t.Errorf("image %s builds on main but is absent from releases", name)
		}
	}
}
