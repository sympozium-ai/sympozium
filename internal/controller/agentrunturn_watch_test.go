package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnscoped"
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

// A turn prepared for a parent whose cleanup was then confirmed (or whose
// node was lost) is settled and released, instead of waiting forever on a
// parent that will never run again while the run's finalizer waits for it.
func TestTurnOfACleanedUpParentIsReleased(t *testing.T) {
	const incarnation = "blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name       string
		turnOwner  string
		turnParent string
		released   bool
	}{
		{"never enrolled", "", incarnation, true},
		{"same owner", "sha256:owner", incarnation, true},
		{"another owner", "sha256:other", incarnation, false},
		{"another parent", "", "blake3:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "tenant", UID: "run-uid"}, Spec: api.AgentRunSpec{ExecutionLifecycle: "enduring"},
				Status: api.AgentRunStatus{Phase: api.AgentRunPhaseFailed, CellnScoped: &api.CellnScopedStatus{CleanupConfirmed: true, ParentIncarnation: incarnation, Owner: "sha256:owner"}}}
			turn := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "turn", Namespace: "tenant", UID: "turn-uid", Finalizers: []string{agentRunTurnFinalizer}},
				Spec:   api.AgentRunTurnSpec{RunName: "run", RunUID: "run-uid"},
				Status: api.AgentRunTurnStatus{CellnScoped: &api.CellnScopedStatus{ParentIncarnation: tc.turnParent, Owner: tc.turnOwner, PreparationName: "celln-op-x"}}}
			c := fake.NewClientBuilder().WithScheme(newAgentRunTestScheme(t)).WithStatusSubresource(&api.AgentRunTurn{}, &api.AgentRun{}).WithObjects(run, turn).Build()
			if !tc.released {
				if parentCleanupCoversTurn(run, turn) {
					t.Fatal("a turn of another parent or owner is covered")
				}
				return
			}
			r := &AgentRunTurnReconciler{Client: c, APIReader: c, ScopedDispatcher: &cellnscoped.Dispatcher{}}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(turn)}); err != nil {
				t.Fatal(err)
			}
			var got api.AgentRunTurn
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(turn), &got); err != nil {
				t.Fatal(err)
			}
			condition := meta.FindStatusCondition(got.Status.Conditions, "CellnTurnComplete")
			if len(got.Finalizers) != 0 || condition == nil || condition.Reason != "ParentEnded" || !got.Status.CellnScoped.CleanupConfirmed {
				t.Fatalf("finalizers %v condition %+v cleanup %v", got.Finalizers, condition, got.Status.CellnScoped.CleanupConfirmed)
			}
		})
	}
}

// A cancelled turn whose owner confirmed cleanup reads as Cancelled and
// complete, not as the last "waiting" condition recorded before cleanup.
func TestCancelledTurnSettlesAsCancelled(t *testing.T) {
	turn := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "turn", Namespace: "tenant", UID: "turn-uid", Finalizers: []string{agentRunTurnFinalizer}},
		Spec: api.AgentRunTurnSpec{RunName: "run", RunUID: "run-uid", CancelRequested: true},
		Status: api.AgentRunTurnStatus{CellnScoped: &api.CellnScopedStatus{CleanupConfirmed: true, NativePhase: "Cancelled"},
			Conditions: []metav1.Condition{{Type: "CellnTurnComplete", Status: metav1.ConditionFalse, Reason: "CleanupUnconfirmed", LastTransitionTime: metav1.Now()}}}}
	c := fake.NewClientBuilder().WithScheme(newAgentRunTestScheme(t)).WithStatusSubresource(&api.AgentRunTurn{}).WithObjects(turn).Build()
	r := &AgentRunTurnReconciler{Client: c, APIReader: c, ScopedDispatcher: &cellnscoped.Dispatcher{}}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(turn)}); err != nil {
		t.Fatal(err)
	}
	var got api.AgentRunTurn
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(turn), &got); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(got.Status.Conditions, "CellnTurnComplete")
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "Cancelled" || len(got.Finalizers) != 0 {
		t.Fatalf("condition %+v finalizers %v", condition, got.Finalizers)
	}
}

// Settling never relabels a committed answer, even from a stale copy.
func TestSettlingKeepsACommittedAnswer(t *testing.T) {
	committed := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "turn", Namespace: "tenant", UID: "turn-uid"},
		Status: api.AgentRunTurnStatus{CellnScoped: &api.CellnScopedStatus{CleanupConfirmed: true},
			Execution: &api.CellnParentTurnStatus{ID: "turn-uid", Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "indigo"}}}}
	c := fake.NewClientBuilder().WithScheme(newAgentRunTestScheme(t)).WithStatusSubresource(&api.AgentRunTurn{}).WithObjects(committed).Build()
	r := &AgentRunTurnReconciler{Client: c, APIReader: c}
	stale := committed.DeepCopy()
	if err := r.markTurnSettled(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	var got api.AgentRunTurn
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(committed), &got); err != nil {
		t.Fatal(err)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, "CellnTurnComplete"); c == nil || c.Reason != "Committed" {
		t.Fatalf("a turn with a result settled as %+v", c)
	}
	// A second settle, again from the stale copy, keeps it.
	if err := r.markTurnSettled(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(committed), &got); err != nil {
		t.Fatal(err)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, "CellnTurnComplete"); c == nil || c.Reason != "Committed" {
		t.Fatalf("relabelled to %+v", c)
	}
}
