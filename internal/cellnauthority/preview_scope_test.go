package cellnauthority

import (
	"context"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"reflect"
	"strings"
	"testing"
)

func TestPreviewExplicitOneShotMatchesDefaultWithoutAuthorizing(t *testing.T) {
	loader, _, agent, _ := loaderFixture(t)
	intent := api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}}
	implicit, err := loader.Preview(context.Background(), agent, intent)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := loader.PreviewLifecycle(context.Background(), agent, intent, "one-shot")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(implicit, explicit) || explicit.ExecutionAuthorized || explicit.Readiness != "not-established" {
		t.Fatal("explicit lifecycle changed preview meaning or granted authority")
	}
}

func TestLegacyPreviewRefusesSharedScopeBeforeReadingAuthority(t *testing.T) {
	// A nil reader is intentional: scope refusal must precede any grant access.
	loader := Loader{}
	for _, lifecycle := range []string{"", "one-shot", "enduring"} {
		_, _, agent, _ := loaderFixture(t)
		preview, err := loader.PreviewLifecycle(context.Background(), agent, api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}, ClusterToolRefs: []api.ClusterCellnToolRef{{Name: "shared", Revision: "v1"}}}, lifecycle)
		if preview != nil || err == nil || !strings.Contains(err.Error(), "AUTH_PROTOCOL_UNSUPPORTED") {
			t.Fatalf("shared preview dropped scope: %+v %v", preview, err)
		}
	}
}
