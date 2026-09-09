package cellnauthority

import (
	"context"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestParentSelectionPinsIntentAndRevalidatesToolAuthority(t *testing.T) {
	for _, change := range []string{"none", "task", "limits", "tool-revision", "grant-withdrawn", "one-shot", "implicit-tools", "duplicate-tools"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			loader, store, agent, selection := loaderFixture(t)
			run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: agent.Namespace, UID: "parent-uid", Generation: 1}, Spec: api.AgentRunSpec{
				AgentRef: agent.Name, Backend: "celln", ExecutionLifecycle: "enduring", Task: api.NewStringTask("remember violet"),
				Enduring: &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 2}, CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{{Name: selection[0].Name, Revision: selection[0].Revision}}},
			}}
			if err := store.Create(ctx, run); err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKeyFromObject(run)
			frozen, err := loader.FreezeParentRun(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if len(frozen.Snapshot.Tools) != 1 || len(frozen.Snapshot.Sources) != 3 || frozen.Run.UID != run.UID {
				t.Fatal("selection not pinned")
			}
			if _, err := loader.FreezeRun(ctx, key, selection, 33554432); err == nil {
				t.Fatal("parent entered one-shot planner")
			}
			switch change {
			case "task":
				run.Spec.Task = api.NewStringTask("changed")
			case "limits":
				run.Spec.Enduring.MaxTurns = 3
			case "tool-revision":
				run.Spec.CellnSelection.ToolRefs[0].Revision = "changed"
			case "one-shot":
				run.Spec.ExecutionLifecycle = "one-shot"
				run.Spec.Enduring = nil
			case "implicit-tools":
				run.Spec.CellnSelection.ToolRefs = nil
			case "duplicate-tools":
				run.Spec.CellnSelection.ToolRefs = append(run.Spec.CellnSelection.ToolRefs, run.Spec.CellnSelection.ToolRefs[0])
			case "grant-withdrawn":
				var grant corev1.ConfigMap
				if err := store.Get(ctx, types.NamespacedName{Namespace: "operator", Name: "agent"}, &grant); err != nil {
					t.Fatal(err)
				}
				if err := store.Delete(ctx, &grant); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Update(ctx, run); err != nil {
				t.Fatal(err)
			}
			err = loader.RevalidateParentSelection(ctx, *frozen)
			if (err == nil) != (change == "none") {
				t.Fatalf("%s revalidation: %v", change, err)
			}
		})
	}
}
