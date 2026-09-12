package cellnauthority

import (
	"context"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestLegacyParentCannotDropSharedCatalogueSelection(t *testing.T) {
	for _, legacyTools := range []bool{false, true} {
		loader, store, agent, selection := loaderFixture(t)
		tools := []api.CellnCatalogueToolRef{}
		if legacyTools {
			tools = append(tools, api.CellnCatalogueToolRef{Name: selection[0].Name, Revision: selection[0].Revision})
		}
		run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: agent.Namespace, UID: "parent-uid", Generation: 1}, Spec: api.AgentRunSpec{
			AgentRef: agent.Name, Backend: "celln", ExecutionLifecycle: "enduring", Task: api.NewStringTask("remember violet"),
			Enduring: &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 2}, CellnSelection: &api.CellnCatalogueSelection{ToolRefs: tools, ClusterToolRefs: []api.ClusterCellnToolRef{{Name: "shared-tool", Revision: "v1"}}},
		}}
		if err := store.Create(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		frozen, err := loader.FreezeParentRun(context.Background(), client.ObjectKeyFromObject(run))
		if frozen != nil || err == nil || !strings.Contains(err.Error(), "AUTH_CAPABILITY_UNSUPPORTED") {
			t.Fatalf("legacy parent discarded shared scope: returnedPlan=%v err=%v", frozen != nil, err)
		}
	}
}
