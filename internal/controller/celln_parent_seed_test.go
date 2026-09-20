package controller

import (
	"context"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// An automatic continuation is seeded with what the fleet's starter package
// takes: the whole recorded conversation on a current package (taskBytes
// 16384), the newest exchanges within 1536 bytes on an old one, where a
// larger seed would make the guest refuse creation.
func TestCellnParentLostContinuationSizesTheSeedForTheFleetsPackage(t *testing.T) {
	seed := make([]api.ConversationExchange, 5)
	for i := range seed {
		seed[i] = api.ConversationExchange{User: strings.Repeat("u", 200), Assistant: strings.Repeat("a", 1000)}
	}
	for i, tc := range []struct {
		name      string
		taskBytes int64
		want      int
	}{{"current package", 16384, 5}, {"old package", 2048, 1}, {"no profile", 0, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			run := newLosableEnduringRun(t, "lost-seeded", "lost-seeded-uid")
			run.Spec.CellnSelection = &api.CellnCatalogueSelection{RuntimeRef: "celln-native"}
			// A user-requested restart, so the no-progress rule does not apply.
			run.Annotations = map[string]string{cellnparent.ContinuationOriginAnnotation: cellnparent.ContinuationOriginRequested}
			run.Spec.Conversation = &api.ConversationSpec{Continuation: "automatic", ContinuesFrom: "earlier", Depth: 1, Seed: seed}
			var objects []client.Object
			if tc.taskBytes != 0 {
				objects = append(objects,
					&api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: "celln-native"}, Spec: api.AgentRuntimeSpec{CellnProfileRef: &api.CellnRuntimeProfileRef{Name: "celln-native-starter", Revision: "v1"}}},
					&api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-starter"}, Spec: api.CellnRuntimeProfileSpec{Revision: "v1", Limits: api.AgentRuntimeCellnLimits{TaskBytes: tc.taskBytes}}})
			}
			r, current, _ := reconcileUntilParentLost(t, run, "blake3:"+strings.Repeat(string(rune('a'+i)), 64), objects...)
			if current.Status.CellnParent == nil || current.Status.CellnParent.ContinuedBy == "" {
				t.Fatalf("lost run was not continued: %+v", current.Status)
			}
			var next api.AgentRun
			if err := r.Get(context.Background(), types.NamespacedName{Namespace: run.Namespace, Name: current.Status.CellnParent.ContinuedBy}, &next); err != nil {
				t.Fatal(err)
			}
			if got := len(next.Spec.Conversation.Seed); got != tc.want || !cellnparent.SeedFits(next.Spec.Conversation.Seed, cellnparent.SeedBudget(tc.taskBytes)) {
				t.Fatalf("seed carries %d exchanges, want %d", got, tc.want)
			}
		})
	}
}
