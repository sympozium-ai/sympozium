package controller

import (
	"context"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTurnObservationPreservesIdentityAndEvidence(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	turn := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "turn", Namespace: "test", UID: "original", Generation: 1}, Spec: api.AgentRunTurnSpec{RunName: "missing", RunUID: "parent", Message: "data"}}
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRunTurn{}).WithObjects(turn).Build()
	r := &AgentRunTurnReconciler{Client: store, APIReader: store, ParentConfigPath: "private-config-must-not-appear"}
	ctx := context.Background()
	key := client.ObjectKeyFromObject(turn)
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	fresh := &api.AgentRunTurn{}
	if err := store.Get(ctx, key, fresh); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(fresh.Status.Conditions, "CellnTurnComplete")
	if condition == nil || condition.Reason != "ReconciliationRequired" || condition.Status != "False" || condition.ObservedGeneration != 1 || strings.Contains(condition.Message, "private-config") || fresh.Status.Execution != nil {
		t.Fatal("missing safe observation or invented execution")
	}
	version := fresh.ResourceVersion
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := store.Get(ctx, key, fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.ResourceVersion != version {
		t.Fatal("unchanged observation rewrote status")
	}
	for _, changed := range []string{"uid", "generation"} {
		stale := fresh.DeepCopy()
		if changed == "uid" {
			stale.UID = "replaced"
		} else {
			stale.Generation++
		}
		if err := r.recordTurnObservation(ctx, store, stale, false); err != nil {
			t.Fatal(err)
		}
		if err := store.Get(ctx, key, fresh); err != nil {
			t.Fatal(err)
		}
		if fresh.ResourceVersion != version {
			t.Fatal("stale observation changed current turn")
		}
	}
	fresh.Status.Execution = &api.CellnParentTurnStatus{ID: "original", Message: "data", Child: "child", Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: false, Answer: "bounded failure"}}
	if err := store.Status().Update(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if err := r.recordTurnObservation(ctx, store, turn, true); err != nil {
		t.Fatal(err)
	}
	if err := store.Get(ctx, key, fresh); err != nil {
		t.Fatal(err)
	}
	condition = meta.FindStatusCondition(fresh.Status.Conditions, "CellnTurnComplete")
	if condition == nil || condition.Status != "True" || condition.Reason != "Committed" || fresh.Status.Execution.Result.Succeeded || !fresh.Status.Execution.Attempted {
		t.Fatal("observation overwrote committed failure or attempt")
	}
}
