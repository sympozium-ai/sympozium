package controller

import (
	"context"
	"encoding/json"
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
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A conversation whose FIRST turn failed is not a dead end: the owner kept the
// parent's context and reports Ready, so the turn controller claims the slot,
// dispatches the user's next message once and commits its result, spending one
// follow-up turn. The same run with a lost parent dispatches nothing.
func TestTurnControllerDispatchesFollowUpAfterFailedInitialTurn(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	incarnation := "blake3:" + strings.Repeat("e", 64)
	for name, lost := range map[string]bool{"parent ready": false, "parent lost": true} {
		t.Run(name, func(t *testing.T) {
			var posts atomic.Int32
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					fmt.Fprintf(w, `{"kind":"completed","apiVersion":"celln.parent-context/v1","turnId":%q,"succeeded":true,"answer":"recovered"}`, body["turnId"])
					return
				}
				fmt.Fprintf(w, `{"incarnation":%q,"status":"Ready","statusIsLiveOwnerObservation":true,"retryAuthorized":false}`, incarnation)
			}))
			defer owner.Close()
			run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "first-turn-failed", Namespace: "tenant", UID: "first-turn-failed-uid", Generation: 1}, Spec: api.AgentRunSpec{
				Backend: "celln", ExecutionLifecycle: "enduring", Task: api.NewStringTask("remember violet"),
				Enduring:       &api.EnduringRunSpec{LeaseSeconds: 600, MaxTurns: 4},
				CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}},
			}}
			digest, err := cellnparent.SpecDigest(run.Spec)
			if err != nil {
				t.Fatal(err)
			}
			binding := api.CellnParentBinding{Target: owner.URL, Principal: "tenant/first-turn-failed-uid", RunUID: string(run.UID), SpecSHA256: digest, LaunchProfile: incarnation, Incarnation: incarnation}
			run.Status.Phase = api.AgentRunPhaseRunning
			run.Status.Conditions = []metav1.Condition{
				{Type: "CellnParentReady", Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 1},
				{Type: "CellnInitialTurnComplete", Status: metav1.ConditionTrue, Reason: "Committed", ObservedGeneration: 1},
			}
			run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true, AdmittedAt: &metav1.Time{Time: time.Now().UTC()},
				InitialTurn: &api.CellnParentTurnStatus{ID: "initial", Message: "remember violet", Child: "blake3:" + strings.Repeat("f", 64), Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: false, Answer: "Turn failed; no result committed: child refused: guest exited with code 1"}}}
			if lost {
				run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "ContextLost", ReachedReady: true}
				run.Status.Conditions[0].Status, run.Status.Conditions[0].Reason = metav1.ConditionFalse, "ContextLost"
			}
			next, err := cellnparent.NewTurn(run, "turn-after-failure", "try again")
			if err != nil {
				t.Fatal(err)
			}
			next.UID = "turn-after-failure-uid"
			store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}, &api.AgentRunTurn{}).WithObjects(run, next).Build()
			root := t.TempDir()
			token := filepath.Join(root, "token")
			if err := os.WriteFile(token, []byte("failed-initial-turn-controller-credential"), 0600); err != nil {
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
			reconciler := &AgentRunTurnReconciler{Client: store, APIReader: store, ParentConfigPath: path}
			for range 4 {
				if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(next)}); err != nil {
					t.Fatal(err)
				}
			}
			var turn api.AgentRunTurn
			if err := store.Get(ctx, client.ObjectKeyFromObject(next), &turn); err != nil {
				t.Fatal(err)
			}
			var parent api.AgentRun
			if err := store.Get(ctx, client.ObjectKeyFromObject(run), &parent); err != nil {
				t.Fatal(err)
			}
			condition := meta.FindStatusCondition(turn.Status.Conditions, "CellnTurnComplete")
			if lost {
				if posts.Load() != 0 || turn.Status.Execution != nil || parent.Status.CellnParent.AcceptedTurns != 0 || condition == nil || condition.Status != metav1.ConditionFalse {
					t.Fatalf("lost parent took a turn: posts=%d execution=%+v accepted=%d condition=%+v", posts.Load(), turn.Status.Execution, parent.Status.CellnParent.AcceptedTurns, condition)
				}
				return
			}
			if posts.Load() != 1 || turn.Status.Execution == nil || turn.Status.Execution.Result == nil || turn.Status.Execution.Result.Answer != "recovered" {
				t.Fatalf("follow-up not dispatched exactly once: posts=%d execution=%+v", posts.Load(), turn.Status.Execution)
			}
			if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "Committed" {
				t.Fatalf("follow-up not committed: %+v", condition)
			}
			if parent.Status.CellnParent.AcceptedTurns != 1 || parent.Status.CellnParent.ActiveTurn != nil || parent.Status.Phase != api.AgentRunPhaseRunning {
				t.Fatalf("budget or slot wrong after follow-up: %+v", parent.Status.CellnParent)
			}
		})
	}
}
