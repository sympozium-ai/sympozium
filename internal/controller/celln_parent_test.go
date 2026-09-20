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
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/zeebo/blake3"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type parentAdmissionFunc func(context.Context, types.NamespacedName) error

func (f parentAdmissionFunc) Admit(ctx context.Context, key types.NamespacedName) error {
	return f(ctx, key)
}

func (parentAdmissionFunc) SupportsPlatform() bool { return false }

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
	// A platform refusal surfaces only its stable reason code, never a path.
	r.ParentAdmission = parentAdmissionFunc(func(context.Context, types.NamespacedName) error {
		return &cellnauthority.PlatformResolutionError{Reason: cellnauthority.ReasonPolicyWithdrawn, Detail: "no execution policy selects the namespace /var/lib/secret-path"}
	})
	if _, err := r.reconcileCellnParent(context.Background(), run); err == nil {
		t.Fatal("platform refusal ignored")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &fresh); err != nil {
		t.Fatal(err)
	}
	if condition = meta.FindStatusCondition(fresh.Status.Conditions, "CellnParentReady"); condition == nil || !strings.Contains(condition.Message, cellnauthority.ReasonPolicyWithdrawn) || strings.Contains(condition.Message, "/var/lib") {
		t.Fatalf("platform refusal not reported by stable reason: %+v", condition)
	}
	// A path-free authored detail is shown so the console can say what to
	// fix; a wrapped error after it never is.
	r.ParentAdmission = parentAdmissionFunc(func(context.Context, types.NamespacedName) error {
		return cellnauthority.Refuse(cellnauthority.ReasonToolUnknown, "cluster tool %q unavailable: %v", "web-fetch", fmt.Errorf("dial tcp 10.0.0.1:443"))
	})
	if _, err := r.reconcileCellnParent(context.Background(), run); err == nil {
		t.Fatal("detailed platform refusal ignored")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &fresh); err != nil {
		t.Fatal(err)
	}
	if condition = meta.FindStatusCondition(fresh.Status.Conditions, "CellnParentReady"); condition == nil || !strings.Contains(condition.Message, `(AUTH_TOOL_UNKNOWN): cluster tool "web-fetch" unavailable. Ask`) || strings.Contains(condition.Message, "10.0.0.1") {
		t.Fatalf("platform refusal detail not reported safely: %+v", condition)
	}
	r.ParentAdmission = parentAdmissionFunc(func(context.Context, types.NamespacedName) error { return fmt.Errorf("no prepared registration") })
	if _, err := r.reconcileCellnParent(context.Background(), run); err == nil {
		t.Fatal("generic refusal after platform refusal ignored")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &fresh); err != nil {
		t.Fatal(err)
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
	if err := r.recordParentAdmissionPending(context.Background(), run, fmt.Errorf("stale")); err != nil {
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

// TestCellnParentWarmPrepLossRecordsOwnerOutcome reproduces the #525 signature:
// create accepted, owner reports Initializing, then ContextLost before Ready
// is ever observed, with no turn submitted. The run must fail without replay
// and freeze the observable failure signature on the status.
func TestCellnParentWarmPrepLossRecordsOwnerOutcome(t *testing.T) {
	id := "blake3:" + strings.Repeat("b", 64)
	run := newLosableEnduringRun(t, "warm-prep-loss", "warm-prep-uid")
	_, current, creates := reconcileUntilParentLost(t, run, id)
	if creates != 1 {
		t.Fatalf("lost parent was replayed: creates=%d", creates)
	}
	ready := meta.FindStatusCondition(current.Status.Conditions, "CellnParentReady")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "ContextLost" {
		t.Fatalf("loss condition missing: %+v", current.Status.Conditions)
	}
	outcome := current.Status.CellnParent.OwnerOutcome
	if outcome == nil || outcome.Status != "ContextLost" || outcome.ReachedReady || outcome.ObservedAt.IsZero() {
		t.Fatalf("owner outcome not frozen: %+v", current.Status.CellnParent)
	}
	for _, text := range []string{ready.Message, current.Status.Error} {
		if !strings.Contains(text, "reachedReady=false") || !strings.Contains(text, id) {
			t.Fatalf("failure signature missing from %q", text)
		}
	}
}

// TestCellnParentLostContinuationIsNotContinuedAgain reproduces the observed
// loop: an automatic continuation whose parent is lost before any follow-up
// turn was continued again every reconcile cycle. It must now fail with a
// stable message, while an original run, a continuation that made progress
// and a user-requested restart are still continued.
func TestCellnParentLostContinuationIsNotContinuedAgain(t *testing.T) {
	seed := []api.ConversationExchange{{User: "Remember the word saffron.", Assistant: "Noted: saffron."}}
	asContinuation := func(t *testing.T, run *api.AgentRun, origin string) *api.AgentRun {
		t.Helper()
		next, err := cellnparent.Continuation(run, seed, origin, cellnparent.LegacySeedBytes)
		if err != nil {
			t.Fatal(err)
		}
		run.Spec = next.Spec
		run.Annotations = next.Annotations
		return run
	}
	cases := []struct {
		name      string
		build     func(t *testing.T) (*api.AgentRun, []client.Object)
		continued bool
	}{
		{name: "original run", continued: true, build: func(t *testing.T) (*api.AgentRun, []client.Object) {
			return newLosableEnduringRun(t, "lost-original", "lost-original-uid"), nil
		}},
		{name: "automatic continuation without follow-up", continued: false, build: func(t *testing.T) (*api.AgentRun, []client.Object) {
			return asContinuation(t, newLosableEnduringRun(t, "lost-again", "lost-again-uid"), cellnparent.ContinuationOriginAutomatic), nil
		}},
		{name: "automatic continuation with a committed follow-up", continued: true, build: func(t *testing.T) (*api.AgentRun, []client.Object) {
			run := asContinuation(t, newLosableEnduringRun(t, "lost-after-work", "lost-after-work-uid"), cellnparent.ContinuationOriginAutomatic)
			turn := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "lost-after-work-t1", Namespace: run.Namespace}, Spec: api.AgentRunTurnSpec{RunName: run.Name, RunUID: string(run.UID), Message: "and seven"},
				Status: api.AgentRunTurnStatus{Execution: &api.CellnParentTurnStatus{Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "Seven, noted."}}}}
			return run, []client.Object{turn}
		}},
		{name: "requested restart", continued: true, build: func(t *testing.T) (*api.AgentRun, []client.Object) {
			return asContinuation(t, newLosableEnduringRun(t, "lost-restart", "lost-restart-uid"), cellnparent.ContinuationOriginRequested), nil
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			run, objects := tc.build(t)
			id := "blake3:" + strings.Repeat(fmt.Sprint(i), 64)
			r, current, creates := reconcileUntilParentLost(t, run, id, objects...)
			if creates != 1 {
				t.Fatalf("lost parent was replayed: creates=%d", creates)
			}
			var runs api.AgentRunList
			if err := r.List(ctx, &runs, client.InNamespace(run.Namespace)); err != nil {
				t.Fatal(err)
			}
			if !tc.continued {
				if len(runs.Items) != 1 || current.Status.CellnParent.ContinuedBy != "" {
					t.Fatalf("stalled continuation was continued: runs=%d continuedBy=%q", len(runs.Items), current.Status.CellnParent.ContinuedBy)
				}
				if !strings.HasPrefix(current.Status.Error, cellnparent.StalledContinuationMessage) {
					t.Fatalf("unstable failure message: %q", current.Status.Error)
				}
				condition := meta.FindStatusCondition(current.Status.Conditions, "CellnContinuation")
				if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "LostBeforeFollowUp" || condition.Message != cellnparent.StalledContinuationMessage {
					t.Fatalf("continuation condition missing: %+v", current.Status.Conditions)
				}
				if outcome := current.Status.CellnParent.OwnerOutcome; outcome == nil || outcome.Status != "ContextLost" {
					t.Fatalf("owner outcome must still be recorded: %+v", current.Status.CellnParent)
				}
				return
			}
			if len(runs.Items) != 2 || current.Status.CellnParent.ContinuedBy == "" || !strings.Contains(current.Status.Error, "continued as "+current.Status.CellnParent.ContinuedBy) {
				t.Fatalf("lost run was not continued: runs=%d status=%+v", len(runs.Items), current.Status)
			}
			var next api.AgentRun
			if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: current.Status.CellnParent.ContinuedBy}, &next); err != nil {
				t.Fatal(err)
			}
			if next.Annotations[cellnparent.ContinuationOriginAnnotation] != cellnparent.ContinuationOriginAutomatic || next.Spec.Conversation.ContinuesFrom != run.Name {
				t.Fatalf("controller continuation not marked automatic: %+v", next.ObjectMeta)
			}
		})
	}
}

func newLosableEnduringRun(t *testing.T, name string, uid types.UID) *api.AgentRun {
	t.Helper()
	run := newTestCellnRun(t, name, uid)
	run.Spec.Celln = nil
	run.Spec.CellnSelection = &api.CellnCatalogueSelection{}
	run.Spec.ExecutionLifecycle = "enduring"
	run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 2, MaxModelRequests: 2, MaxOutputTokens: 1024}
	run.Finalizers = []string{agentRunFinalizer}
	return run
}

// reconcileUntilParentLost admits run against a fake owner that reports
// Initializing twice, then ContextLost, and reconciles until the run fails.
// It returns the reconciler, the failed run and the number of parent creates.
func reconcileUntilParentLost(t *testing.T, run *api.AgentRun, id string, objects ...client.Object) (*AgentRunReconciler, api.AgentRun, int32) {
	t.Helper()
	ctx := context.Background()
	var creates, polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/parents" {
			creates.Add(1)
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"incarnation":%q,"initializationPending":true,"retryAuthorized":false}`, id)
			return
		}
		if polls.Add(1) <= 2 {
			fmt.Fprintf(w, `{"incarnation":%q,"status":"Initializing","statusIsLiveOwnerObservation":true,"retryAuthorized":false}`, id)
			return
		}
		fmt.Fprintf(w, `{"incarnation":%q,"status":"ContextLost","statusIsLiveOwnerObservation":false,"retryAuthorized":false}`, id)
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
		Binding: api.CellnParentBinding{Target: server.URL, Principal: "tenant/warm-prep-uid", RunUID: string(run.UID), SpecSHA256: digest, LaunchProfile: id, Incarnation: id}}}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r := newAgentRunTestReconciler(t, append([]client.Object{run}, objects...)...)
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
	for range 8 {
		if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
			t.Fatal(err)
		}
		if current.Status.Phase == api.AgentRunPhaseFailed {
			break
		}
		if _, err := r.reconcilePending(ctx, logr.Discard(), &current); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != api.AgentRunPhaseFailed {
		t.Fatalf("parent loss did not fail the run: %+v", current.Status)
	}
	return r, current, creates.Load()
}

type platformAdmissionFunc func(context.Context, types.NamespacedName) error

func (f platformAdmissionFunc) Admit(ctx context.Context, key types.NamespacedName) error {
	return f(ctx, key)
}
func (platformAdmissionFunc) SupportsPlatform() bool { return true }

// A one-shot on the platform is a single-turn parent: admitted and started
// like an enduring run, finished with the initial turn's answer as the run
// result, and its parent stopped by the completed reconciliation.
func TestPlatformOneShotFinishesWithItsAnswerAndStopsTheParent(t *testing.T) {
	ctx := context.Background()
	id := "blake3:" + strings.Repeat("c", 64)
	run := newTestCellnRun(t, "one-shot-parent", "one-shot-uid")
	run.Spec.Celln = nil
	run.Spec.Model = api.ModelSpec{ConnectionRef: "celln-native", Model: "deepseek-chat"}
	run.Spec.CellnSelection = &api.CellnCatalogueSelection{RuntimeRef: "celln-native", ToolRefs: []api.CellnCatalogueToolRef{}, ClusterToolRefs: []api.ClusterCellnToolRef{{Name: "workspace-read", Revision: "v1"}}}
	run.Finalizers = []string{agentRunFinalizer}
	if !run.Spec.PlatformOneShotShape() {
		t.Fatal("fixture is not a platform one-shot")
	}
	var creates, stops, turns atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/v1/parents":
			creates.Add(1)
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"incarnation":%q,"initializationPending":true,"retryAuthorized":false}`, id)
		case strings.HasSuffix(req.URL.Path, "/stop"):
			stops.Add(1)
			fmt.Fprintf(w, `{"incarnation":%q,"status":"Stopped","retryAuthorized":false}`, id)
		case strings.HasSuffix(req.URL.Path, "/turns"):
			turns.Add(1)
			fmt.Fprint(w, `{"kind":"completed","apiVersion":"celln.parent-context/v1","turnId":"initial","succeeded":true,"answer":"Botswana is in southern Africa."}`)
		default:
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
		Binding: api.CellnParentBinding{Target: server.URL, Principal: "tenant/one-shot-uid", RunUID: string(run.UID), SpecSHA256: digest, LaunchProfile: id, Incarnation: id}}}}
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
	r.ParentAdmission = platformAdmissionFunc(func(context.Context, types.NamespacedName) error { return nil })
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
	if creates.Load() != 1 || current.Status.Phase != api.AgentRunPhaseRunning || current.Status.CellnIssuance != nil || current.Status.CellnActionID != "" || current.Status.JobName != "" {
		t.Fatalf("one-shot did not take the parent path: %+v creates=%d", current.Status, creates.Load())
	}
	for range 3 {
		if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
			t.Fatal(err)
		}
		if current.Status.Phase != api.AgentRunPhaseRunning {
			break
		}
		if _, err := r.reconcileRunning(ctx, logr.Discard(), &current); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
		t.Fatal(err)
	}
	if turns.Load() != 1 || current.Status.Phase != api.AgentRunPhaseSucceeded || current.Status.Result != "Botswana is in southern Africa." || !meta.IsStatusConditionTrue(current.Status.Conditions, "CellnInitialTurnComplete") {
		t.Fatalf("one-shot did not finish with its answer: phase=%s result=%q turns=%d", current.Status.Phase, current.Status.Result, turns.Load())
	}
	if _, err := r.reconcileCompleted(ctx, logr.Discard(), &current); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 1 {
		t.Fatalf("completed one-shot did not stop its parent: stops=%d", stops.Load())
	}
}
