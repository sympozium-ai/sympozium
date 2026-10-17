package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const testGateToken = "0123456789abcdef0123456789abcdef0123456789abcdef"

// gateVerdictFixture builds a PostRunning run plus its per-run gate Secret,
// and a server whose global bearer is "global-token".
func gateVerdictFixture(t *testing.T, phase sympoziumv1alpha1.AgentRunPhase, withSecret bool) *Server {
	t.Helper()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-run", Namespace: "default", UID: "uid-demo-run"},
		Status:     sympoziumv1alpha1.AgentRunStatus{Phase: phase},
	}
	objs := []runtime.Object{run}
	if withSecret {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "sympozium-run-gate-demo-run", Namespace: "default"},
			Data:       map[string][]byte{"token": []byte(testGateToken)},
		}
		controller := true
		secret.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: sympoziumv1alpha1.GroupVersion.String(), Kind: "AgentRun",
			Name: run.Name, UID: run.UID, Controller: &controller,
		}}
		objs = append(objs, secret)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())

	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("global-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	srv.authToken = &tokenReader{path: path}
	return srv
}

func postVerdict(t *testing.T, srv *Server, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/demo-run/gate-verdict?namespace=default", strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.SetPathValue("name", "demo-run")
	rec := httptest.NewRecorder()
	srv.patchRunGateVerdict(rec, req)
	return rec
}

func TestGateVerdict_AcceptsThePerRunToken(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, true)
	rec := postVerdict(t, srv, testGateToken, `{"action":"approve"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got sympoziumv1alpha1.AgentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Annotations["sympozium.ai/gate-verdict"] == "" {
		t.Error("the verdict annotation was not written")
	}
}

func TestGateVerdict_AcceptsTheGlobalToken(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, true)
	if rec := postVerdict(t, srv, "global-token", `{"action":"approve"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestGateVerdict_RejectsBadCredentials(t *testing.T) {
	cases := []struct {
		name, bearer string
		withSecret   bool
	}{
		{"wrong token", "not-the-token-not-the-token-not-the-token-xxxxxx", true},
		{"no token", "", true},
		// A run with no gate has no Secret. It must look identical to a wrong
		// token, so a caller cannot probe which runs are gated.
		{"missing secret", testGateToken, false},
		{"empty bearer with a secret present", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, tc.withSecret)
			rec := postVerdict(t, srv, tc.bearer, `{"action":"approve"}`)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Another run's token must not work here, which is the whole point of scoping
// the credential per run.
func TestGateVerdict_RejectsAnotherRunsToken(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, true)

	other := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sympozium-run-gate-other-run", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("ffffffffffffffffffffffffffffffffffffffffffffffff")},
	}
	if err := srv.client.Create(t.Context(), other); err != nil {
		t.Fatalf("create other secret: %v", err)
	}

	rec := postVerdict(t, srv, "ffffffffffffffffffffffffffffffffffffffffffffffff", `{"action":"approve"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// A Secret at the run's predictable name that the run does not control is not
// its token, the same rule ensureGateVerdictToken applies when minting.
func TestGateVerdict_RejectsATokenFromASecretTheRunDoesNotOwn(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, false)

	planted := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sympozium-run-gate-demo-run", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte(testGateToken)},
	}
	if err := srv.client.Create(t.Context(), planted); err != nil {
		t.Fatalf("create planted secret: %v", err)
	}

	rec := postVerdict(t, srv, testGateToken, `{"action":"approve"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// AwaitingGate is the in-place retry phase: the pod is parked while the gate
// decides, and the hook reports from there.
func TestGateVerdict_AcceptsAwaitingGate(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhaseAwaitingGate, true)
	rec := postVerdict(t, srv, testGateToken, `{"action":"retry","reason":"tests fail"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got sympoziumv1alpha1.AgentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Annotations["sympozium.ai/gate-verdict-attempt"] != "1" {
		t.Errorf("verdict attempt stamp = %q, want 1", got.Annotations["sympozium.ai/gate-verdict-attempt"])
	}
}

// retry is what a gate hook needs most and the endpoint used to reject it.
func TestGateVerdict_AcceptsRetryWithoutAResponse(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, true)
	rec := postVerdict(t, srv, testGateToken, `{"action":"retry","reason":"build failed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestGateVerdict_StillRequiresAResponseForRejectAndRewrite(t *testing.T) {
	for _, action := range []string{"reject", "rewrite"} {
		t.Run(action, func(t *testing.T) {
			srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, true)
			rec := postVerdict(t, srv, testGateToken, `{"action":"`+action+`"}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGateVerdict_RejectsAnUnknownAction(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, true)
	rec := postVerdict(t, srv, testGateToken, `{"action":"escalate"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// Oversized verdicts fail loudly rather than being truncated into an
// annotation that looks deliberate.
func TestGateVerdict_RejectsAnOversizedBody(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, true)
	body := `{"action":"rewrite","response":"` + strings.Repeat("x", maxGateVerdictBytes+1) + `"}`
	rec := postVerdict(t, srv, testGateToken, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body.String())
	}
}

func TestGateVerdict_RejectsOutsidePostRunning(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhaseRunning, true)
	rec := postVerdict(t, srv, testGateToken, `{"action":"approve"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// An open server stays open here, like everywhere else.
func TestGateVerdict_OpenServerNeedsNoToken(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, true)
	srv.authToken = nil

	if rec := postVerdict(t, srv, "", `{"action":"approve"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// The route is exempt from authMiddleware, so the matcher must not exempt
// anything else.
func TestIsGateVerdictRequest(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/api/v1/runs/demo-run/gate-verdict", true},
		{http.MethodGet, "/api/v1/runs/demo-run/gate-verdict", false},
		{http.MethodPost, "/api/v1/runs/demo-run", false},
		{http.MethodPost, "/api/v1/runs", false},
		{http.MethodPost, "/api/v1/runs//gate-verdict", false},
		{http.MethodPost, "/api/v1/runs/a/b/gate-verdict", false},
		{http.MethodPost, "/api/v1/agents/x/gate-verdict", false},
		{http.MethodPost, "/api/v1/runs/demo-run/gate-verdict/extra", false},
		{http.MethodPost, "/api/v1/runs/demo-run/logs", false},
	}
	for _, tc := range cases {
		if got := isGateVerdictRequest(tc.method, tc.path); got != tc.want {
			t.Errorf("isGateVerdictRequest(%s, %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// The middleware must let the route through so the handler can accept a
// per-run token, and must keep protecting everything adjacent to it.
func TestAuthMiddleware_ExemptsOnlyTheGateVerdictRoute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("global-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	mw := authMiddleware(&tokenReader{path: path}, okHandler)

	if rec := authRequest(t, mw, "POST", "/api/v1/runs/demo-run/gate-verdict", ""); rec.Code != http.StatusOK {
		t.Errorf("gate verdict route: status = %d, want it passed through to the handler", rec.Code)
	}
	if rec := authRequest(t, mw, "GET", "/api/v1/runs/demo-run", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("neighbouring route: status = %d, want 401", rec.Code)
	}
	if rec := authRequest(t, mw, "POST", "/api/v1/runs", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("run creation: status = %d, want 401", rec.Code)
	}
}

// The middleware feeds isGateVerdictRequest the DECODED r.URL.Path, while
// ServeMux matches on escaped segments. The two therefore disagree on
// percent-encoded separators, and both disagreements are pinned here because
// each is safe only by the direction it happens to fall in — a change to
// either side should have to edit this test.
func TestIsGateVerdictRequest_PercentEncodedPaths(t *testing.T) {
	cases := []struct {
		raw        string
		wantPath   string
		wantExempt bool
		why        string
	}{
		{
			raw:        "/api/v1/runs/%2e%2e/gate-verdict",
			wantPath:   "/api/v1/runs/../gate-verdict",
			wantExempt: true,
			why: "decodes to a single dot-dot segment with no slash, so it is exempted AND " +
				"ServeMux routes it, reaching the handler with name \"..\". authorizeGateVerdict " +
				"is then the only check, and it fails closed: sympozium-run-gate-.. is not a " +
				"valid Secret name, so the lookup errors and the caller gets 401.",
		},
		{
			raw:        "/api/v1/runs/a%2Fb/gate-verdict",
			wantPath:   "/api/v1/runs/a/b/gate-verdict",
			wantExempt: false,
			why: "decodes to two segments so it is NOT exempted, while ServeMux still routes " +
				"it as one. A gate hook's per-run token would be refused by the global " +
				"middleware: a functionality gap, not a bypass.",
		},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, tc.raw, nil)
		if req.URL.Path != tc.wantPath {
			t.Errorf("%s: decoded path = %q, want %q", tc.raw, req.URL.Path, tc.wantPath)
		}
		if got := isGateVerdictRequest(req.Method, req.URL.Path); got != tc.wantExempt {
			t.Errorf("%s: exempt = %v, want %v — %s", tc.raw, got, tc.wantExempt, tc.why)
		}
	}
}

// The %2e%2e path above skips authMiddleware and reaches the handler, so the
// handler's own check is the only thing between it and a verdict write. Prove
// it refuses rather than trusting the comment.
func TestGateVerdict_DotDotNameIsRefusedAtTheHandler(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhasePostRunning, true)

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/%2e%2e/gate-verdict?namespace=default",
		strings.NewReader(`{"action":"approve"}`))
	req.Header.Set("Authorization", "Bearer "+testGateToken)
	req.SetPathValue("name", "..") // what ServeMux hands the handler
	rec := httptest.NewRecorder()
	srv.patchRunGateVerdict(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body.String())
	}
}

// setRunAttempt moves the fixture run to a later attempt of an in-place chain.
func setRunAttempt(t *testing.T, srv *Server, attempt int) {
	t.Helper()
	var run sympoziumv1alpha1.AgentRun
	key := types.NamespacedName{Name: "demo-run", Namespace: "default"}
	if err := srv.client.Get(context.Background(), key, &run); err != nil {
		t.Fatalf("get run: %v", err)
	}
	run.Status.Attempt = attempt
	if err := srv.client.Update(context.Background(), &run); err != nil {
		t.Fatalf("update run: %v", err)
	}
}

// A tab still showing attempt 1 must not approve attempt 2: the operator
// never saw it. The client names the attempt it judged; a mismatch is a 409.
func TestGateVerdict_RefusesAVerdictForAnAttemptThatIsNoLongerCurrent(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhaseAwaitingGate, true)
	setRunAttempt(t, srv, 2)

	rec := postVerdict(t, srv, "global-token", `{"action":"approve","attempt":1}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}

	var run sympoziumv1alpha1.AgentRun
	if err := srv.client.Get(context.Background(), types.NamespacedName{Name: "demo-run", Namespace: "default"}, &run); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if v, has := run.Annotations["sympozium.ai/gate-verdict"]; has {
		t.Errorf("a stale verdict was recorded: %s", v)
	}
}

func TestGateVerdict_AcceptsAVerdictForTheCurrentAttempt(t *testing.T) {
	srv := gateVerdictFixture(t, sympoziumv1alpha1.AgentRunPhaseAwaitingGate, true)
	setRunAttempt(t, srv, 2)

	rec := postVerdict(t, srv, "global-token", `{"action":"approve","attempt":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got sympoziumv1alpha1.AgentRun
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Annotations["sympozium.ai/gate-verdict-attempt"] != "2" {
		t.Errorf("verdict attempt stamp = %q, want 2", got.Annotations["sympozium.ai/gate-verdict-attempt"])
	}
}
