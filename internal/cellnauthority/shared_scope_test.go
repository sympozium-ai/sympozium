package cellnauthority

import (
	"context"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

func TestLegacyOneShotCannotDiscardSharedToolIntent(t *testing.T) {
	loader, c, original := frozenFixture(t)
	ctx := context.Background()
	key := types.NamespacedName{Namespace: "tenant", Name: "run"}
	var run api.AgentRun
	if err := c.Get(ctx, key, &run); err != nil {
		t.Fatal(err)
	}
	tool := original.Snapshot.Tools[0].Identity
	run.Spec.CellnSelection = &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{{Name: tool.Name, Revision: tool.Revision}}, ClusterToolRefs: []api.ClusterCellnToolRef{{Name: "shared-tool", Revision: "v1"}}}
	if err := c.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
	plan, err := loader.FreezeRun(ctx, key, []Selection{{Name: tool.Name, Revision: tool.Revision}}, 33554432)
	if plan != nil || err == nil || !strings.Contains(err.Error(), "AUTH_CAPABILITY_UNSUPPORTED") {
		t.Fatalf("shared intent silently used legacy authority: plan=%v err=%v", plan != nil, err)
	}
}
