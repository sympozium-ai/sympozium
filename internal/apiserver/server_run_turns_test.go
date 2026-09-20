package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCancelRunTurnRecordsIntentAgainstExactUIDs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "parent", UID: "parent-uid"},
		Spec: api.AgentRunSpec{Backend: "celln", ExecutionLifecycle: "enduring", CellnSelection: &api.CellnCatalogueSelection{}, Enduring: &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 3, MaxModelRequests: 6, MaxOutputTokens: 3072}}}
	run.Status.CellnParent = &api.CellnParentStatus{CreateAttempted: true, Binding: api.CellnParentBinding{RunUID: string(run.UID), Incarnation: "blake3:" + strings.Repeat("a", 64)}}
	turn, err := cellnparent.NewTurn(run, "followup", "hello")
	if err != nil {
		t.Fatal(err)
	}
	turn.UID = "turn-uid"
	execution, err := cellnparent.BindTurn(run, turn)
	if err != nil {
		t.Fatal(err)
	}
	execution.Attempted = true
	turn.Status.Execution = &execution
	run.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: turn.Name, UID: string(turn.UID)}
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}, &api.AgentRunTurn{}).WithObjects(run, turn).Build()
	handler := NewServer(store, nil, nil, logr.Discard()).Handler(nil)
	send := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest("POST", "/api/v1/runs/parent/turns/followup/cancel", strings.NewReader(body)))
		return r
	}
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"runUID":"old","turnUID":"turn-uid"}`, 400},
		{`{"runUID":"parent-uid","turnUID":"old"}`, 409},
		{`{"runUID":"parent-uid","turnUID":"turn-uid","owner":"elsewhere"}`, 400},
		{`{"runUID":"parent-uid","turnUID":"turn-uid"} {}`, 400},
	} {
		if got := send(tc.body); got.Code != tc.code {
			t.Fatalf("%d: %s", got.Code, got.Body.String())
		}
	}
	body := `{"runUID":"parent-uid","turnUID":"turn-uid"}`
	if got := send(body); got.Code != 202 {
		t.Fatalf("%d: %s", got.Code, got.Body.String())
	}
	if got := send(body); got.Code != 200 {
		t.Fatalf("repeated intent: %d", got.Code)
	}
	var saved api.AgentRunTurn
	if err := store.Get(t.Context(), client.ObjectKeyFromObject(turn), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Spec.CancelRequested || saved.Status.CancelAttempted || saved.Status.Execution.Result != nil {
		t.Fatal("API failed to record intent or fabricated execution outcome")
	}
	var parent api.AgentRun
	if err := store.Get(t.Context(), client.ObjectKeyFromObject(run), &parent); err != nil {
		t.Fatal(err)
	}
	if parent.Status.CellnParent.ActiveTurn == nil {
		t.Fatal("request released parent slot")
	}
}

func TestRunTurnAPIIsBoundedIdempotentAndScoped(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "parent", UID: "parent-uid"}, Spec: api.AgentRunSpec{Backend: "celln", ExecutionLifecycle: "enduring", CellnSelection: &api.CellnCatalogueSelection{}, Enduring: &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 3, MaxModelRequests: 6, MaxOutputTokens: 3072}}, Status: api.AgentRunStatus{Phase: api.AgentRunPhaseRunning, CellnParent: &api.CellnParentStatus{CreateAttempted: true, InitialTurn: &api.CellnParentTurnStatus{Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "ready"}}}}}
	run.Generation = 1
	run.Status.Conditions = []metav1.Condition{{Type: "CellnParentReady", Status: "True", ObservedGeneration: 1}}
	digest, err := cellnparent.SpecDigest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	hash := "blake3:" + strings.Repeat("a", 64)
	run.Status.CellnParent.Binding = api.CellnParentBinding{RunUID: string(run.UID), SpecSHA256: digest, Target: "https://owner.example", Principal: "test", LaunchProfile: hash, Incarnation: hash}
	other := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "other"}, Spec: api.AgentRunTurnSpec{RunName: "parent", RunUID: "parent-uid", Message: "private"}}
	old := other.DeepCopy()
	old.Namespace = "default"
	old.Name = "old"
	old.Spec.RunUID = "previous-parent-uid"
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, other, old).Build()
	handler := NewServer(store, nil, nil, logr.Discard()).Handler(nil)
	send := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	body := `{"runUID":"parent-uid","requestId":"request-one","message":"What was my value?"}`
	first := send("POST", "/api/v1/runs/parent/turns", body)
	if first.Code != 202 {
		t.Fatalf("create %d: %s", first.Code, first.Body.String())
	}
	var turn api.AgentRunTurn
	if err := json.Unmarshal(first.Body.Bytes(), &turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.OwnerReferences) != 1 || turn.OwnerReferences[0].UID != run.UID || turn.Status.Execution != nil {
		t.Fatal("API minted execution authority or omitted parent owner")
	}
	if res := send("POST", "/api/v1/runs/parent/turns", body); res.Code != 200 {
		t.Fatal("same request was not observed idempotently")
	}
	for _, test := range []struct {
		body string
		code int
	}{
		{strings.Replace(body, "What was my value?", "changed input", 1), 409},
		{strings.Replace(body, "parent-uid", "stale-uid", 1), 400},
		{`{"runUID":"parent-uid","requestId":"new","message":"hello","launchProfile":"injected"}`, 400},
		{`{"runUID":"parent-uid","requestId":"new","message":"` + strings.Repeat("a", 2049) + `"}`, 400},
	} {
		if res := send("POST", "/api/v1/runs/parent/turns", test.body); res.Code != test.code {
			t.Fatalf("unexpected status %d expected %d", res.Code, test.code)
		}
	}
	list := send("GET", "/api/v1/runs/parent/turns", "")
	var result struct {
		Items []api.AgentRunTurn `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Name != turn.Name {
		t.Fatal("history leaked namespace or previous run UID")
	}
	if res := send(http.MethodGet, "/api/v1/runs/parent/turns?namespace=missing", ""); res.Code != 404 {
		t.Fatal("parent namespace ignored")
	}
	for change, reason := range map[string]string{
		"stale-condition": "parent readiness does not match current run intent",
		"changed-intent":  "parent readiness does not match current run intent",
		"not-ready":       "parent not ready (ReconciliationRequired)",
		"active-turn":     "parent already owns a different turn",
		"initial-pending": "initial turn still running",
		"parent-lost":     "parent lost (ContextLost)",
		"lease-expired":   "parent lease expired",
		"exhausted":       "turn limit reached (3 of 3 turns used",
	} {
		changed := run.DeepCopy()
		changed.ResourceVersion = ""
		if err := store.Get(t.Context(), client.ObjectKeyFromObject(run), changed); err != nil {
			t.Fatal(err)
		}
		version := changed.ResourceVersion
		changed = run.DeepCopy()
		changed.ResourceVersion = version
		switch change {
		case "stale-condition":
			changed.Generation++
		case "changed-intent":
			changed.Spec.Enduring.MaxTurns++
			changed.Status.Conditions[0].ObservedGeneration = changed.Generation
		case "not-ready":
			changed.Status.Conditions[0].Status = "False"
			changed.Status.Conditions[0].Reason = "ReconciliationRequired"
		case "active-turn":
			changed.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "another-turn", UID: "another-uid"}
		case "initial-pending":
			changed.Status.CellnParent.InitialTurn.Result = nil
		case "parent-lost":
			changed.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "ContextLost", ReachedReady: true}
		case "lease-expired":
			changed.Status.CellnParent.AdmittedAt = &metav1.Time{Time: time.Now().Add(-time.Hour)}
		case "exhausted":
			changed.Status.CellnParent.AcceptedTurns = changed.Spec.Enduring.MaxTurns - 1
		}
		if err := store.Update(t.Context(), changed); err != nil {
			t.Fatal(err)
		}
		newBody := strings.Replace(body, "request-one", "new-"+change, 1)
		if res := send("POST", "/api/v1/runs/parent/turns", newBody); res.Code != 409 || !strings.Contains(res.Body.String(), reason) || res.Header().Get("X-Sympozium-Turn-Refusal") == "" {
			t.Fatalf("%s: status %d body %q, want 409 explaining %q", change, res.Code, res.Body.String(), reason)
		}
		if res := send("POST", "/api/v1/runs/parent/turns", body); res.Code != 200 {
			t.Fatalf("%s prevented observation of original request: %d", change, res.Code)
		}
		if err := json.Unmarshal(send("GET", "/api/v1/runs/parent/turns", "").Body.Bytes(), &result); err != nil || len(result.Items) != 1 {
			t.Fatalf("%s created a refused turn: %v", change, err)
		}
	}
}

// A committed initial turn that FAILED leaves the parent's context intact: the
// owner still reports Ready, so the conversation takes the next message exactly
// as it would after a failed later turn. It stays refused whenever the owner's
// state is not known to be ready, each time saying why.
func TestRunTurnAPIAcceptsFollowUpAfterFailedInitialTurn(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	hash := "blake3:" + strings.Repeat("a", 64)
	for name, test := range map[string]struct {
		change func(*api.AgentRun)
		code   int
		reason string
	}{
		"ready":            {func(*api.AgentRun) {}, 202, ""},
		"ready-turn-spent": {func(run *api.AgentRun) { run.Status.CellnParent.AcceptedTurns = 1 }, 202, ""},
		"turn-limit":       {func(run *api.AgentRun) { run.Status.CellnParent.AcceptedTurns = 2 }, 409, "turn limit reached (3 of 3 turns used"},
		"context-lost": {func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "ContextLost", ReachedReady: true}
			run.Status.Conditions[0].Status, run.Status.Conditions[0].Reason = "False", "ContextLost"
		}, 409, "parent lost (ContextLost)"},
		"stopped": {func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "Stopped", ReachedReady: true}
		}, 409, "parent lost (Stopped)"},
		"reconciliation-required": {func(run *api.AgentRun) {
			run.Status.Conditions[0].Status, run.Status.Conditions[0].Reason = "False", "ReconciliationRequired"
		}, 409, "parent not ready (ReconciliationRequired)"},
		"never-ready": {func(run *api.AgentRun) { run.Status.Conditions = nil }, 409, "parent not ready (no readiness reported)"},
		"failed-run":  {func(run *api.AgentRun) { run.Status.Phase = api.AgentRunPhaseFailed }, 409, "run is Failed, not Running"},
		"uncommitted": {func(run *api.AgentRun) { run.Status.CellnParent.InitialTurn.Result = nil }, 409, "initial turn still running"},
	} {
		t.Run(name, func(t *testing.T) {
			run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "parent", UID: "parent-uid", Generation: 1}, Spec: api.AgentRunSpec{Backend: "celln", ExecutionLifecycle: "enduring", CellnSelection: &api.CellnCatalogueSelection{}, Enduring: &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 3, MaxModelRequests: 6, MaxOutputTokens: 3072}}, Status: api.AgentRunStatus{Phase: api.AgentRunPhaseRunning, CellnParent: &api.CellnParentStatus{CreateAttempted: true, InitialTurn: &api.CellnParentTurnStatus{Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: false, Answer: "Turn failed; no result committed: child refused"}}}}}
			run.Status.Conditions = []metav1.Condition{{Type: "CellnParentReady", Status: "True", Reason: "Ready", ObservedGeneration: 1}}
			digest, err := cellnparent.SpecDigest(run.Spec)
			if err != nil {
				t.Fatal(err)
			}
			run.Status.CellnParent.Binding = api.CellnParentBinding{RunUID: string(run.UID), SpecSHA256: digest, Target: "https://owner.example", Principal: "test", LaunchProfile: hash, Incarnation: hash}
			test.change(run)
			store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
			response := httptest.NewRecorder()
			NewServer(store, nil, nil, logr.Discard()).Handler(nil).ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/runs/parent/turns", strings.NewReader(`{"runUID":"parent-uid","requestId":"after-failure","message":"try again"}`)))
			if response.Code != test.code || !strings.Contains(response.Body.String(), test.reason) {
				t.Fatalf("status %d body %q, want %d explaining %q", response.Code, response.Body.String(), test.code, test.reason)
			}
			var turns api.AgentRunTurnList
			if err := store.List(t.Context(), &turns); err != nil {
				t.Fatal(err)
			}
			if want := map[int]int{202: 1, 409: 0}[test.code]; len(turns.Items) != want {
				t.Fatalf("stored %d turns, want %d", len(turns.Items), want)
			}
		})
	}
}

func TestRunTurnAPIAcceptsReadyScopedEnduringParent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	incarnation := "blake3:" + strings.Repeat("c", 64)
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "scoped-parent", UID: "scoped-parent-uid", Generation: 3}, Spec: api.AgentRunSpec{Backend: "celln", AgentRef: "agent", Task: api.NewStringTask("initial"), ExecutionLifecycle: "enduring", CellnSelection: &api.CellnCatalogueSelection{}, Enduring: &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 3, MaxModelRequests: 6, MaxOutputTokens: 3072}}, Status: api.AgentRunStatus{Phase: api.AgentRunPhaseRunning, CellnScoped: &api.CellnScopedStatus{PreparationName: "celln-op-" + strings.Repeat("a", 64), PreparationUID: "p", DecisionName: "celln-final-" + strings.Repeat("b", 64), DecisionUID: "d", ReceiverID: "operation", Owner: "owner", ParentIncarnation: incarnation, StartAttempted: true, NativePhase: "Running", ReceiptDigest: "sha256:" + strings.Repeat("d", 64), Output: "initial answer"}, Conditions: []metav1.Condition{{Type: "CellnScopedExecution", Status: metav1.ConditionTrue, Reason: "EnduringParentReady", ObservedGeneration: 3}}}}
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	handler := NewServer(store, nil, nil, logr.Discard()).Handler(nil)
	response := httptest.NewRecorder()
	body := `{"runUID":"scoped-parent-uid","requestId":"followup","message":"continue"}`
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/runs/scoped-parent/turns", strings.NewReader(body)))
	if response.Code != http.StatusAccepted {
		t.Fatalf("scoped turn submission returned %d: %s", response.Code, response.Body.String())
	}
	var turn api.AgentRunTurn
	if err := json.Unmarshal(response.Body.Bytes(), &turn); err != nil || len(turn.OwnerReferences) != 1 || turn.OwnerReferences[0].UID != run.UID || turn.Status.CellnScoped != nil {
		t.Fatalf("submission minted authority or lost root ownership: turn=%+v err=%v", turn, err)
	}
}

type unavailableTurnStore struct{ client.Client }

func (unavailableTurnStore) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("private storage details must not escape")
}

func TestRunTurnStorageFailureIsNotNotFound(t *testing.T) {
	handler := NewServer(unavailableTurnStore{}, nil, nil, logr.Discard()).Handler(nil)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/api/v1/runs/parent/turns", nil))
		if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private storage") {
			t.Fatalf("%s exposed storage details or misreported absence: %d", method, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs/parent", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private storage") {
		t.Fatal("run detail confused unavailable storage with confirmed absence")
	}
}

// A user restart is marked as requested so the controller never mistakes it
// for its own automatic continuation, and it works even on a run that is
// itself an automatic continuation.
func TestContinueRunMarksRequestedOrigin(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "parent", UID: "parent-uid",
		Annotations: map[string]string{cellnparent.ContinuationOriginAnnotation: cellnparent.ContinuationOriginAutomatic}},
		Spec: api.AgentRunSpec{AgentRef: "agent", Backend: "celln", ExecutionLifecycle: "enduring", Task: api.NewStringTask(cellnparent.ResumeMessage), CellnSelection: &api.CellnCatalogueSelection{},
			Enduring:     &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 3, MaxModelRequests: 6, MaxOutputTokens: 3072},
			Conversation: &api.ConversationSpec{Continuation: "automatic", ContinuesFrom: "earlier", Depth: 1}}}
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	handler := NewServer(store, nil, nil, logr.Discard()).Handler(nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/runs/parent/continue?namespace=default&uid=parent-uid&keep=true", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d: %s", rec.Code, rec.Body.String())
	}
	var next api.AgentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &next); err != nil {
		t.Fatal(err)
	}
	if next.Annotations[cellnparent.ContinuationOriginAnnotation] != cellnparent.ContinuationOriginRequested || cellnparent.IsAutomaticContinuation(&next) {
		t.Fatalf("restart not marked requested: %+v", next.Annotations)
	}
	if next.Spec.Conversation == nil || next.Spec.Conversation.ContinuesFrom != "parent" || next.Spec.Conversation.Depth != 2 {
		t.Fatalf("restart conversation: %+v", next.Spec.Conversation)
	}
}
