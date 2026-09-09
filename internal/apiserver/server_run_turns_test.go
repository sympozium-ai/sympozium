package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	for _, change := range []string{"stale-condition", "changed-intent", "not-ready", "active-turn", "initial-failed", "exhausted"} {
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
		case "active-turn":
			changed.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "another-turn", UID: "another-uid"}
		case "initial-failed":
			changed.Status.CellnParent.InitialTurn.Result.Succeeded = false
		case "exhausted":
			changed.Status.CellnParent.AcceptedTurns = changed.Spec.Enduring.MaxTurns - 1
		}
		if err := store.Update(t.Context(), changed); err != nil {
			t.Fatal(err)
		}
		newBody := strings.Replace(body, "request-one", "new-"+change, 1)
		if res := send("POST", "/api/v1/runs/parent/turns", newBody); res.Code != 409 {
			t.Fatalf("%s admitted: %d", change, res.Code)
		}
		if res := send("POST", "/api/v1/runs/parent/turns", body); res.Code != 200 {
			t.Fatalf("%s prevented observation of original request: %d", change, res.Code)
		}
		if err := json.Unmarshal(send("GET", "/api/v1/runs/parent/turns", "").Body.Bytes(), &result); err != nil || len(result.Items) != 1 {
			t.Fatalf("%s created a refused turn: %v", change, err)
		}
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
