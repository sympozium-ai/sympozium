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

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/zeebo/blake3"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type parentAdmissionFunc func(context.Context, types.NamespacedName) error

func (f parentAdmissionFunc) Admit(ctx context.Context, key types.NamespacedName) error {
	return f(ctx, key)
}

func TestCellnParentAdmissionFailureDoesNotStart(t *testing.T) {
	run := newTestCellnRun(t, "refused-parent", "refused-parent-uid")
	run.Spec.Celln = nil
	run.Spec.CellnSelection = &api.CellnCatalogueSelection{}
	run.Spec.ExecutionLifecycle = "enduring"
	run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 2}
	r := newAgentRunTestReconciler(t, run)
	r.ParentConfigPath = t.TempDir()
	calls := 0
	r.ParentAdmission = parentAdmissionFunc(func(context.Context, types.NamespacedName) error {
		calls++
		return fmt.Errorf("no prepared registration")
	})
	if _, err := r.reconcileCellnParent(context.Background(), run); err == nil || calls != 1 {
		t.Fatalf("admission refusal ignored: %v", err)
	}
	var fresh api.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.Status.CellnParent != nil || fresh.Status.JobName != "" || fresh.Status.CellnActionID != "" {
		t.Fatal("admission refusal produced execution identity")
	}
	condition := meta.FindStatusCondition(fresh.Status.Conditions, "CellnParentReady")
	if condition == nil || condition.Status != "False" || condition.Reason != "AdmissionPending" || strings.Contains(condition.Message, "no prepared registration") {
		t.Fatal("missing safe admission status")
	}
	version := fresh.ResourceVersion
	if _, err := r.reconcileCellnParent(context.Background(), &fresh); err == nil {
		t.Fatal("second refusal ignored")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.ResourceVersion != version {
		t.Fatal("unchanged admission observation rewrote status")
	}
	// A stale refusal must not replace a newer parent's readiness observation.
	fresh.Status.CellnParent = &api.CellnParentStatus{}
	meta.RemoveStatusCondition(&fresh.Status.Conditions, "CellnParentReady")
	if err := r.Status().Update(context.Background(), &fresh); err != nil {
		t.Fatal(err)
	}
	if err := r.recordParentAdmissionPending(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &fresh); err != nil {
		t.Fatal(err)
	}
	if meta.FindStatusCondition(fresh.Status.Conditions, "CellnParentReady") != nil {
		t.Fatal("stale refusal overwrote bound parent status")
	}
}

func TestCellnParentConfiguredStartupAndUncertainCleanup(t *testing.T) {
	proveCellnParentController(t, false)
}

func TestCellnParentCommittedAnswerSurvivesContextLossWithoutReplay(t *testing.T) {
	proveCellnParentController(t, true)
}

func proveCellnParentController(t *testing.T, loseReply bool) {
	ctx := context.Background()
	id := "blake3:" + strings.Repeat("a", 64)
	run := newTestCellnRun(t, "enduring-parent", "parent-uid")
	run.Spec.Celln = nil
	run.Spec.CellnSelection = &api.CellnCatalogueSelection{}
	run.Spec.ExecutionLifecycle = "enduring"
	run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 2, MaxModelRequests: 2, MaxOutputTokens: 1024}
	run.Finalizers = []string{agentRunFinalizer}
	var creates, stops, turns atomic.Int32
	var lost atomic.Bool
	wire, _ := json.Marshal([]string{id, "initial"})
	child := fmt.Sprintf("blake3:%x", blake3.Sum256(wire))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/v1/parents":
			creates.Add(1)
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"incarnation":%q,"initializationPending":true,"retryAuthorized":false}`, id)
		case strings.HasSuffix(req.URL.Path, "/stop"):
			stops.Add(1)
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":"uncertain"}`)
		case strings.HasSuffix(req.URL.Path, "/turns"):
			turns.Add(1)
			if loseReply {
				lost.Store(true)
				w.WriteHeader(502)
				return
			}
			fmt.Fprint(w, `{"kind":"completed","apiVersion":"celln.parent-context/v1","turnId":"initial","succeeded":true,"answer":"initial answer"}`)
		case strings.HasSuffix(req.URL.Path, "/turns/initial"):
			fmt.Fprintf(w, `{"ownerStatus":"ContextLost","retryAuthorized":false,"turn":{"stage":"parent-committed","record":{"version":1,"parent":%q,"child":%q,"turnId":"initial","succeeded":true,"answer":"initial answer"}}}`, id, child)
		default:
			if lost.Load() {
				fmt.Fprintf(w, `{"incarnation":%q,"status":"ContextLost","statusIsLiveOwnerObservation":false,"retryAuthorized":false}`, id)
				return
			}
			fmt.Fprintf(w, `{"incarnation":%q,"status":"Ready","statusIsLiveOwnerObservation":true,"retryAuthorized":false}`, id)
		}
	}))
	defer server.Close()
	digest, err := cellnparent.SpecDigest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("controller-parent-only-test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	config := cellnparent.ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []cellnparent.RunApproval{{Namespace: run.Namespace, Name: run.Name, TokenFile: token,
		Binding: api.CellnParentBinding{Target: server.URL, Principal: "tenant/parent-uid", RunUID: string(run.UID), SpecSHA256: digest, LaunchProfile: id, Incarnation: id}}}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r := newAgentRunTestReconciler(t, run)
	r.ParentConfigPath = path
	admissions := 0
	r.ParentAdmission = parentAdmissionFunc(func(context.Context, types.NamespacedName) error {
		admissions++
		if admissions > 1 {
			return fmt.Errorf("bound parent must not be readmitted")
		}
		return nil
	})
	var current api.AgentRun
	for range 3 {
		if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
			t.Fatal(err)
		}
		if _, err := r.reconcilePending(ctx, logr.Discard(), &current); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
		t.Fatal(err)
	}
	if creates.Load() != 1 || current.Status.Phase != api.AgentRunPhaseRunning || !meta.IsStatusConditionTrue(current.Status.Conditions, "CellnParentReady") || current.Status.JobName != "" || current.Status.CellnActionID != "" {
		t.Fatalf("wrong execution path: %+v creates=%d", current.Status, creates.Load())
	}
	if admissions != 1 {
		t.Fatalf("parent admission called %d times", admissions)
	}
	if _, err := r.reconcileRunning(ctx, logr.Discard(), &current); err != nil {
		t.Fatal(err)
	}
	if creates.Load() != 1 {
		t.Fatal("running reconciliation recreated parent")
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
		t.Fatal(err)
	}
	expectedPhase := api.AgentRunPhaseRunning
	if loseReply {
		if _, err := r.reconcileRunning(ctx, logr.Discard(), &current); err != nil {
			t.Fatal(err)
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
			t.Fatal(err)
		}
		expectedPhase = api.AgentRunPhaseFailed
		if !meta.IsStatusConditionFalse(current.Status.Conditions, "CellnParentReady") || !meta.IsStatusConditionTrue(current.Status.Conditions, "CellnInitialTurnComplete") {
			t.Fatal("turn outcome and parent context loss conflated")
		}
	}
	if turns.Load() != 1 {
		t.Fatal("initial turn resubmitted")
	}
	if current.Status.CellnParent.InitialTurn == nil || current.Status.CellnParent.InitialTurn.Result == nil || current.Status.CellnParent.InitialTurn.Result.Answer != "initial answer" || current.Status.Phase != expectedPhase {
		t.Fatalf("initial turn was not durably completed within enduring run: %+v", current.Status.CellnParent)
	}
	result, err := r.reconcileDelete(ctx, logr.Discard(), &current)
	if err != nil || result.RequeueAfter == 0 || stops.Load() != 1 {
		t.Fatalf("uncertain cleanup: %+v %v", result, err)
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Finalizers) != 1 || !current.Status.CellnParent.CreateAttempted {
		t.Fatal("uncertain parent was forgotten")
	}
}
