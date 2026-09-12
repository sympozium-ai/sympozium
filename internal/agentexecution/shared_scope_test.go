package agentexecution

import (
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"strings"
	"testing"
)

func TestSharedCatalogueNeverFallsThroughLegacyResolution(t *testing.T) {
	for _, tools := range [][]api.CellnCatalogueToolRef{nil, {}, {{Name: "legacy", Revision: "v1"}}} {
		selection := &api.CellnCatalogueSelection{ToolRefs: tools, ClusterToolRefs: []api.ClusterCellnToolRef{{Name: "shared", Revision: "v1"}}}
		for _, model := range []string{"", "model"} {
			result, err := Resolve(nil, Input{Backend: "celln", CellnSelection: selection, Model: model})
			if err == nil || !strings.Contains(err.Error(), "AUTH_CAPABILITY_UNSUPPORTED") || result.Backend != "" {
				t.Fatalf("shared scope entered legacy/default-model path: %+v %v", result, err)
			}
		}
	}
}
