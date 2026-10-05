package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// A change to a run re-queues exactly its turns, so a turn waiting on a run
// that failed or is being deleted re-checks at once instead of in backoff.
func TestTurnsOfRunMapsARunToItsTurns(t *testing.T) {
	turn := func(name, namespace, run string) *api.AgentRunTurn {
		return &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: api.AgentRunTurnSpec{RunName: run}}
	}
	c := fake.NewClientBuilder().WithScheme(newAgentRunTestScheme(t)).WithObjects(
		turn("a-1", "team", "a"), turn("a-2", "team", "a"), turn("b-1", "team", "b"), turn("a-1", "other", "a"),
	).Build()
	r := &AgentRunTurnReconciler{Client: c, APIReader: c}
	got := map[client.ObjectKey]bool{}
	for _, req := range r.turnsOfRun(context.Background(), &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "team"}}) {
		got[req.NamespacedName] = true
	}
	if len(got) != 2 || !got[client.ObjectKey{Namespace: "team", Name: "a-1"}] || !got[client.ObjectKey{Namespace: "team", Name: "a-2"}] {
		t.Fatalf("mapped %v", got)
	}
}
