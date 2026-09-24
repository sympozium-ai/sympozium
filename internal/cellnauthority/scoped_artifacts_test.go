package cellnauthority

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"os"
	"reflect"
	"testing"
)

func artifactLimits() api.CellnToolLimits {
	return api.CellnToolLimits{TimeoutMillis: 30000, MemoryBytes: 64 << 20, ArgumentBytes: 8192, OutputBytes: 8192, Workspace: "none", Effects: "external-side-effects", Artifacts: &api.CellnArtifactLimits{Operation: "write", MaxOperations: 4, MaxFiles: 8, MaxFileBytes: 4096, MaxTotalBytes: 16384}}
}

// Shared signed-vs-material vectors are byte-identical to Celln's consumer.
func TestScopedArtifactsPairedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../test/fixtures/scoped-artifacts/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "d8d8dc6a3505d517d515bc492d86996cd5e3f907e8c9718d3195ecf20c14c800" {
		t.Fatal("paired fixture changed")
	}
	var f struct {
		APIVersion string         `json:"apiVersion"`
		Declared   map[string]any `json:"declared"`
		Effects    string         `json:"effects"`
		Vectors    []struct {
			Name     string         `json:"name"`
			Patch    map[string]any `json:"patch"`
			Accepted bool           `json:"accepted"`
		} `json:"vectors"`
	}
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.APIVersion != "celln.scoped-artifacts/v1" || len(f.Vectors) != 15 {
		t.Fatal("fixture contract")
	}
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			m := map[string]any{}
			for k, x := range f.Declared {
				m[k] = x
			}
			for k, x := range v.Patch {
				m[k] = x
			}
			encoded, _ := json.Marshal(m)
			a := api.CellnArtifactLimits{}
			d := json.NewDecoder(bytes.NewReader(encoded))
			d.DisallowUnknownFields()
			l := artifactLimits()
			err := d.Decode(&a)
			l.Artifacts = &a
			valid := err == nil && validToolLimits(l)
			if valid {
				effective, e := intersectToolLimits(artifactLimits(), l)
				valid = e == nil && reflect.DeepEqual(effective, l)
			}
			if valid != v.Accepted {
				t.Fatalf("accepted %v want %v", valid, v.Accepted)
			}
		})
	}
}

func TestScopedArtifactValidationAndAttenuation(t *testing.T) {
	for name, mutate := range map[string]func(*api.CellnToolLimits){
		"operations":    func(l *api.CellnToolLimits) { l.Artifacts.MaxOperations = 65 },
		"files":         func(l *api.CellnToolLimits) { l.Artifacts.MaxFiles = 257 },
		"file-bytes":    func(l *api.CellnToolLimits) { l.Artifacts.MaxFileBytes = 4097 },
		"total-bytes":   func(l *api.CellnToolLimits) { l.Artifacts.MaxTotalBytes = 1048577 },
		"write-effects": func(l *api.CellnToolLimits) { l.Effects = "none" },
		"read-effects":  func(l *api.CellnToolLimits) { l.Artifacts.Operation = "read" },
		"append":        func(l *api.CellnToolLimits) { l.Artifacts.Operation = "append" },
		"both-brokers":  func(l *api.CellnToolLimits) { l.HTTPS = &api.CellnHTTPSLimits{} },
	} {
		t.Run(name, func(t *testing.T) {
			l := artifactLimits()
			mutate(&l)
			if validToolLimits(l) {
				t.Fatal("invalid authority accepted")
			}
		})
	}
	base := artifactLimits()
	requested := *base.DeepCopy()
	requested.Artifacts.MaxOperations = 2
	got, err := intersectToolLimits(base, requested)
	if err != nil || got.Artifacts.MaxOperations != 2 || base.Artifacts.MaxOperations != 4 {
		t.Fatal("minimum/copy failure", err)
	}
	requested.Artifacts = nil
	if _, err = intersectToolLimits(base, requested); err == nil {
		t.Fatal("omission accepted")
	}
	requested = artifactLimits()
	requested.Artifacts.Operation = "read"
	requested.Effects = "none"
	if _, err = intersectToolLimits(base, requested); err == nil {
		t.Fatal("operation mismatch accepted")
	}
}
