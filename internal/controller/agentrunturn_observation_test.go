package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
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
		if err := r.recordTurnObservation(ctx, store, stale, nil); err != nil {
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
	if err := r.recordTurnObservation(ctx, store, turn, errors.New("unconfirmed outcome")); err != nil {
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

func TestTurnObservationReportsExpiredLease(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// Direct observation: an expired-lease refusal is terminal for new turns,
	// not a transient reconciliation failure.
	turn := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "turn", Namespace: "test", UID: "original", Generation: 1}, Spec: api.AgentRunTurnSpec{RunName: "missing", RunUID: "parent", Message: "data"}}
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRunTurn{}).WithObjects(turn).Build()
	r := &AgentRunTurnReconciler{Client: store, APIReader: store}
	expired := fmt.Errorf("slot claim: %w", cellnparent.ErrParentLeaseExpired)
	if err := r.recordTurnObservation(ctx, store, turn, expired); err != nil {
		t.Fatal(err)
	}
	fresh := &api.AgentRunTurn{}
	if err := store.Get(ctx, client.ObjectKeyFromObject(turn), fresh); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(fresh.Status.Conditions, "CellnTurnComplete")
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "LeaseExpired" || !strings.Contains(condition.Message, "lease") {
		t.Fatalf("expiry not visible: %+v", condition)
	}
	// Full reconcile: an expired original lease refuses the claim, spends no
	// budget, and still records the terminal reason on the turn.
	admittedAt := time.Now().UTC().Add(-time.Hour)
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "lease-run", Namespace: "tenant", UID: "lease-run-uid", Generation: 1}, Spec: api.AgentRunSpec{
		Backend: "celln", ExecutionLifecycle: "enduring", Task: api.NewStringTask("remember violet"),
		Enduring:       &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 4},
		CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}},
	}}
	digest, err := cellnparent.SpecDigest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	incarnation := "blake3:" + strings.Repeat("c", 64)
	binding := api.CellnParentBinding{Target: "https://owner.example", Principal: "tenant/lease-run-uid", RunUID: string(run.UID), SpecSHA256: digest, LaunchProfile: incarnation, Incarnation: incarnation}
	run.Status.Phase = api.AgentRunPhaseRunning
	run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true, AdmittedAt: &metav1.Time{Time: admittedAt},
		InitialTurn: &api.CellnParentTurnStatus{ID: "initial", Message: "hello", Child: "blake3:" + strings.Repeat("d", 64), Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "ready"}}}
	run.Generation = 1
	run.Status.Conditions = []metav1.Condition{{Type: "CellnParentReady", Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 1}}
	next, err := cellnparent.NewTurn(run, "turn-one", "follow up")
	if err != nil {
		t.Fatal(err)
	}
	next.UID = "turn-one-uid"
	live := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}, &api.AgentRunTurn{}).WithObjects(run, next).Build()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("lease-expiry-observation-credential-long-enough"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cellnparent.ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []cellnparent.RunApproval{{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: token}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	liveReconciler := &AgentRunTurnReconciler{Client: live, APIReader: live, ParentConfigPath: path}
	result, err := liveReconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(next)})
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("expired lease did not requeue cleanly: %+v %v", result, err)
	}
	observed := &api.AgentRunTurn{}
	if err := live.Get(ctx, client.ObjectKeyFromObject(next), observed); err != nil {
		t.Fatal(err)
	}
	condition = meta.FindStatusCondition(observed.Status.Conditions, "CellnTurnComplete")
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "LeaseExpired" {
		t.Fatalf("reconciled expiry not visible: %+v", condition)
	}
	var saved api.AgentRun
	if err := live.Get(ctx, client.ObjectKeyFromObject(run), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status.CellnParent.AcceptedTurns != 0 {
		t.Fatal("expired claim spent turn budget")
	}
}
