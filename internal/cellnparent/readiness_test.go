package cellnparent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// readyParentFixture is a Running enduring parent whose initial turn committed
// with the given outcome and whose owner reports Ready for the current intent.
func readyParentFixture(t *testing.T, initialSucceeded bool, maxTurns int32) (*api.AgentRun, api.CellnParentBinding) {
	t.Helper()
	run, binding := admissionFixture(t)
	run.Spec.Enduring.MaxTurns = maxTurns
	digest, err := SpecDigest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	binding.SpecSHA256 = digest
	run.Generation = 1
	run.Status.Phase = api.AgentRunPhaseRunning
	run.Status.Conditions = []metav1.Condition{{Type: ParentReadyCondition, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 1}}
	answer := "ready"
	if !initialSucceeded {
		answer = "Turn failed; no result committed: child refused: guest exited with code 1"
	}
	run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true, InitialTurn: &api.CellnParentTurnStatus{ID: "initial", Message: "hello", Child: testID, Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: initialSucceeded, Answer: answer}}}
	return run, binding
}

func TestTurnReadiness(t *testing.T) {
	now := time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)
	owner := &api.CellnActiveTurn{Name: "mine", UID: "mine-uid"}
	for name, test := range map[string]struct {
		initialSucceeded bool
		change           func(*api.AgentRun)
		claimant         *api.CellnActiveTurn
		reason           string // "" means the turn is accepted
		message          string
		is               error
	}{
		"initial succeeded and parent ready": {initialSucceeded: true},
		// (a) a committed failure with a Ready parent is as open as a success.
		"initial failed and parent ready":        {},
		"later turn failed and parent ready":     {initialSucceeded: true, change: func(run *api.AgentRun) { run.Status.CellnParent.AcceptedTurns = 1 }},
		"initial failed, one follow-up spent":    {change: func(run *api.AgentRun) { run.Status.CellnParent.AcceptedTurns = 1 }},
		"initial failed, owner busy with a turn": {change: func(run *api.AgentRun) { run.Status.Conditions[0].Reason = "TurnActive" }},
		// (b) the owner's state is lost, stopped, refused or unknown.
		"context lost": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "ContextLost", ReachedReady: true}
		}, reason: TurnRefusedParentLost, message: "parent lost (ContextLost)"},
		"stopped": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "Stopped", ReachedReady: true}
		}, reason: TurnRefusedParentLost, message: "parent lost (Stopped)"},
		"teardown uncertain": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "TeardownUncertain"}
		}, reason: TurnRefusedParentLost, message: "parent lost (TeardownUncertain)"},
		"create refused": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: OwnerCreateRefused}
		}, reason: TurnRefusedParentLost, message: "parent lost (CreateRefused)"},
		"reconciliation required": {change: func(run *api.AgentRun) {
			run.Status.Conditions[0].Status, run.Status.Conditions[0].Reason = metav1.ConditionFalse, "ReconciliationRequired"
		}, reason: TurnRefusedParentNotReady, message: "parent not ready (ReconciliationRequired)"},
		"never reported ready":       {change: func(run *api.AgentRun) { run.Status.Conditions = nil }, reason: TurnRefusedParentNotReady, message: "no readiness reported"},
		"readiness for older intent": {change: func(run *api.AgentRun) { run.Generation = 2 }, reason: TurnRefusedStaleReadiness, message: "does not match current run intent"},
		"intent changed under current readiness": {change: func(run *api.AgentRun) {
			run.Spec.Enduring.MaxTurns++
		}, reason: TurnRefusedChangedIntent, message: "does not match current run intent"},
		"run failed":   {change: func(run *api.AgentRun) { run.Status.Phase = api.AgentRunPhaseFailed }, reason: TurnRefusedNotRunning, message: "run is Failed"},
		"not admitted": {change: func(run *api.AgentRun) { run.Status.CellnParent = nil }, reason: TurnRefusedNotAdmitted, message: "not started"},
		"one-shot":     {change: func(run *api.AgentRun) { run.Spec.ExecutionLifecycle, run.Spec.Enduring = "", nil }, reason: TurnRefusedNotEnduring, message: "one-shot"},
		// (c) the initial turn is not committed yet.
		"initial turn not attempted": {change: func(run *api.AgentRun) { run.Status.CellnParent.InitialTurn = nil }, reason: TurnRefusedInitialPending, message: "initial turn still running"},
		"initial turn uncommitted":   {change: func(run *api.AgentRun) { run.Status.CellnParent.InitialTurn.Result = nil }, reason: TurnRefusedInitialPending, message: "initial turn still running"},
		// Serialization, lease, budget and deletion are unchanged by the outcome.
		"another turn active": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "other", UID: "other-uid"}
		}, claimant: owner, reason: TurnRefusedActiveTurn, message: "already owns a different turn", is: ErrTurnBusy},
		"active turn, no claimant": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "mine", UID: "mine-uid"}
		}, reason: TurnRefusedActiveTurn, message: "already owns a different turn", is: ErrTurnBusy},
		"slot owner re-claims after lease and readiness went": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "mine", UID: "mine-uid"}
			run.Status.CellnParent.AdmittedAt = &metav1.Time{Time: now.Add(-time.Hour)}
			run.Status.Conditions[0].Status = metav1.ConditionFalse
		}, claimant: owner},
		"slot owner of a lost parent": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "mine", UID: "mine-uid"}
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "ContextLost"}
		}, claimant: owner, reason: TurnRefusedParentLost, message: "parent lost (ContextLost)"},
		"lease expired": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.AdmittedAt = &metav1.Time{Time: now.Add(-time.Hour)}
		}, reason: TurnRefusedLeaseExpired, message: "parent lease expired", is: ErrParentLeaseExpired},
		"max turns reached": {change: func(run *api.AgentRun) { run.Status.CellnParent.AcceptedTurns = 2 }, reason: TurnRefusedTurnLimitReached, message: "turn limit reached (3 of 3 turns used, including the initial turn)"},
		"max turns reached by a failed initial turn alone": {change: func(run *api.AgentRun) {
			run.Spec.Enduring.MaxTurns = 1
			run.Status.CellnParent.Binding.SpecSHA256, _ = SpecDigest(run.Spec)
		}, reason: TurnRefusedTurnLimitReached, message: "turn limit reached (1 of 1 turns used"},
		"deletion in progress": {change: func(run *api.AgentRun) {
			run.DeletionTimestamp = &metav1.Time{Time: now}
		}, reason: TurnRefusedDeleting, message: "being deleted"},
	} {
		t.Run(name, func(t *testing.T) {
			run, _ := readyParentFixture(t, test.initialSucceeded, 3)
			if test.change != nil {
				test.change(run)
			}
			err := TurnReadiness(run, test.claimant, now)
			if test.reason == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var refusal *TurnRefusal
			if !errors.As(err, &refusal) || refusal.Reason != test.reason || !strings.Contains(refusal.Message, test.message) {
				t.Fatalf("got %v, want %s explaining %q", err, test.reason, test.message)
			}
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("%v does not wrap %v", err, test.is)
			}
		})
	}
}

func TestTurnsUsedCountsTheInitialTurnWhateverItsOutcome(t *testing.T) {
	if TurnsUsed(nil) != 0 || TurnsUsed(&api.CellnParentStatus{}) != 0 {
		t.Fatal("an unstarted parent has used no turns")
	}
	failed := &api.CellnParentStatus{AcceptedTurns: 2, InitialTurn: &api.CellnParentTurnStatus{Result: &api.CellnParentTurnResult{Succeeded: false, Answer: "failed"}}}
	if TurnsUsed(failed) != 3 {
		t.Fatalf("TurnsUsed = %d, want 3: a failed initial turn still spent its turn", TurnsUsed(failed))
	}
}

// A follow-up after a FAILED initial turn is claimed, dispatched once and
// committed like any other; the failed turn still counts toward maxTurns, so
// the ceiling holds and the next turn is refused without reaching the owner.
func TestFollowUpDispatchesAfterFailedInitialTurnWithinBudget(t *testing.T) {
	ctx := context.Background()
	run, binding := readyParentFixture(t, false, 2)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			posts.Add(1)
			fmt.Fprintf(w, `{"kind":"completed","apiVersion":"celln.parent-context/v1","turnId":%q,"succeeded":true,"answer":"recovered"}`, body["turnId"])
			return
		}
		fmt.Fprintf(w, `{"incarnation":%q,"status":"Ready","statusIsLiveOwnerObservation":true,"retryAuthorized":false}`, testID)
	}))
	defer server.Close()
	binding.Target = server.URL
	run.Status.CellnParent.Binding = binding
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}, &api.AgentRunTurn{}).WithObjects(run).Build()
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("failed-initial-turn-parent-credential"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: token}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	reconcile := func(name string) (types.NamespacedName, error) {
		turn, err := NewTurn(run, name, "try again")
		if err != nil {
			t.Fatal(err)
		}
		turn.UID = types.UID(name + "-uid")
		if err := store.Create(ctx, turn); err != nil {
			t.Fatal(err)
		}
		key := client.ObjectKeyFromObject(turn)
		for range 4 {
			done, err := ReconcileTurn(ctx, store, store, key, path)
			if err != nil || done {
				return key, err
			}
		}
		t.Fatal("turn never completed")
		return key, nil
	}
	key, err := reconcile("second")
	if err != nil {
		t.Fatalf("follow-up after a failed initial turn refused: %v", err)
	}
	var saved api.AgentRunTurn
	if err := store.Get(ctx, key, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status.Execution == nil || saved.Status.Execution.Result == nil || !saved.Status.Execution.Result.Succeeded || saved.Status.Execution.Result.Answer != "recovered" {
		t.Fatalf("follow-up result not durable: %+v", saved.Status.Execution)
	}
	var refusal *TurnRefusal
	if _, err := reconcile("third"); !errors.As(err, &refusal) || refusal.Reason != TurnRefusedTurnLimitReached {
		t.Fatalf("turn beyond maxTurns: %v", err)
	}
	var parent api.AgentRun
	if err := store.Get(ctx, client.ObjectKeyFromObject(run), &parent); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 || parent.Status.CellnParent.AcceptedTurns != 1 || TurnsUsed(parent.Status.CellnParent) != 2 || parent.Status.CellnParent.ActiveTurn != nil {
		t.Fatalf("posts=%d accepted=%d active=%v: the failed initial turn must count once and the ceiling must hold", posts.Load(), parent.Status.CellnParent.AcceptedTurns, parent.Status.CellnParent.ActiveTurn)
	}
}
