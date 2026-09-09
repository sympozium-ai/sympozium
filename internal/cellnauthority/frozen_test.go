package cellnauthority

import (
	"context"
	"encoding/json"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func frozenFixture(t *testing.T) (Loader, client.Client, *FrozenSelection) {
	t.Helper()
	l, c, _, selection := loaderFixture(t)
	var run api.AgentRun
	if err := json.Unmarshal([]byte(`{"metadata":{"namespace":"tenant","name":"run","uid":"run-uid","generation":1},"spec":{"agentRef":"agent","backend":"celln","task":"use the lent tool"}}`), &run); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(context.Background(), &run); err != nil {
		t.Fatal(err)
	}
	frozen, err := l.FreezeRun(context.Background(), client.ObjectKeyFromObject(&run), selection, 33554432)
	if err != nil {
		t.Fatal(err)
	}
	return l, c, frozen
}

func TestFrozenSelectionRoundTripAndCurrentApprovals(t *testing.T) {
	l, _, frozen := frozenFixture(t)
	bytes, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	var restored FrozenSelection
	if err := json.Unmarshal(bytes, &restored); err != nil {
		t.Fatal(err)
	}
	if err := l.Revalidate(context.Background(), restored); err != nil {
		t.Fatal(err)
	}
	if restored.Run.UID != "run-uid" || restored.Run.SpecSHA256 == "" {
		t.Fatal("run identity not pinned")
	}
}

func TestOneShotCatalogueRefusesParentLifecycleAtFreezeAndRevalidation(t *testing.T) {
	for _, mode := range []string{"enduring", "unknown", "stray-limits", "saved-parent", "one-shot"} {
		t.Run(mode, func(t *testing.T) {
			l, c, frozen := frozenFixture(t)
			ctx := context.Background()
			key := types.NamespacedName{Namespace: "tenant", Name: "run"}
			var run api.AgentRun
			if err := c.Get(ctx, key, &run); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "enduring", "unknown", "one-shot":
				run.Spec.ExecutionLifecycle = mode
			case "stray-limits":
				run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 300, MaxTurns: 4}
			case "saved-parent":
				run.Status.CellnParent = &api.CellnParentStatus{}
			}
			if err := c.Update(ctx, &run); err != nil {
				t.Fatal(err)
			}
			_, _, err := l.readRun(ctx, key)
			if mode == "one-shot" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("one-shot planning admitted parent intent/state")
			}
			if err := l.Revalidate(ctx, *frozen); err == nil {
				t.Fatal("old one-shot selection remained valid")
			}
		})
	}
}

func TestFrozenSelectionNeverRetargetsOrExpandsOnRetry(t *testing.T) {
	for _, mode := range []string{"run-task", "run-recreated", "source-revision", "source-withdrawn", "prepared-tampered", "limits-expanded", "tools-removed", "runtime-tampered"} {
		t.Run(mode, func(t *testing.T) {
			l, c, frozen := frozenFixture(t)
			ctx := context.Background()
			switch mode {
			case "run-task", "run-recreated":
				var run api.AgentRun
				if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant", Name: "run"}, &run); err != nil {
					t.Fatal(err)
				}
				if mode == "run-task" {
					if err := json.Unmarshal([]byte(`"different task"`), &run.Spec.Task); err != nil {
						t.Fatal(err)
					}
					if err := c.Update(ctx, &run); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := c.Delete(ctx, &run); err != nil {
						t.Fatal(err)
					}
					run.UID = "new-uid"
					run.ResourceVersion = ""
					if err := c.Create(ctx, &run); err != nil {
						t.Fatal(err)
					}
				}
			case "source-revision", "source-withdrawn":
				var cm corev1.ConfigMap
				if err := c.Get(ctx, l.OperatorSource, &cm); err != nil {
					t.Fatal(err)
				}
				if mode == "source-withdrawn" {
					if err := c.Delete(ctx, &cm); err != nil {
						t.Fatal(err)
					}
				} else {
					cm.Labels = map[string]string{"review": "changed"}
					if err := c.Update(ctx, &cm); err != nil {
						t.Fatal(err)
					}
				}
			case "prepared-tampered":
				frozen.Prepared.Composition.Sources[0] = "blake3:replacement"
			case "limits-expanded":
				frozen.Snapshot.Tools[0].Limits.OutputBytes++
			case "tools-removed":
				frozen.Snapshot.Tools = nil
			case "runtime-tampered":
				frozen.Snapshot.RuntimeSpec.Celln.EntryPoint = "/replacement"
			}
			if err := l.Revalidate(ctx, *frozen); err == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}
