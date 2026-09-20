package cellnparentproxy

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExactTurnCancellationRoutes(t *testing.T) {
	base := "/v1/parents/blake3:" + strings.Repeat("a", 64) + "/turns/one"
	for _, tc := range []struct {
		method, suffix string
		allowed        bool
	}{
		{"POST", "/cancel", true}, {"GET", "", true},
		{"GET", "/cancel", false}, {"DELETE", "/cancel", false},
		{"POST", "", false}, {"POST", "/cancel/extra", false},
		{"POST", "/stop", false},
	} {
		if route(tc.method, base+tc.suffix) != tc.allowed {
			t.Fatalf("unexpected route: %s %s", tc.method, tc.suffix)
		}
	}
}

func TestExecutionEdgeIsSeparateFromParentAuthority(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"POST", "/v1/executions", true}, {"GET", "/v1/executions/run-123", true},
		{"DELETE", "/v1/executions/run-123", true}, {"POST", "/v1/executions/run-123/cancel", true},
		{"GET", "/v1/executions/run-123/audit", true}, {"GET", "/v1/capabilities", true},
		{"POST", "/v1/parents", false}, {"GET", "/v1/parents/blake3:" + strings.Repeat("a", 64), false},
		{"POST", "/v1/executions/run-123/audit", false}, {"GET", "/v1/executions/../parents", false},
		{"GET", "/v1/executions/run/extra/path", false}, {"GET", "/v1/executions", false},
	} {
		if executionRoute(tc.method, tc.path) != tc.allowed {
			t.Fatalf("unexpected execution route %s %s", tc.method, tc.path)
		}
		if tc.allowed && route(tc.method, tc.path) {
			t.Fatal("execution route leaked into parent listener")
		}
	}
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(202) }))
	defer backend.Close()
	handler, closeTransport, err := NewExecution(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	for _, path := range []string{"/v1/parents", "/v1/executions?override=true", "/v1/executions%2fescape"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", path, strings.NewReader(`{}`)))
		if response.Code != 404 {
			t.Fatalf("unexpected status for %s: %d", path, response.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("forbidden request reached execution router")
	}
}

func TestFixedTLSParentEdge(t *testing.T) {
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-credential" || r.Header.Get("Idempotency-Key") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-Host") != "" {
			t.Error("authority/header rewrite mismatch")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer backend.Close()
	handler, closeTransport, err := New(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	edge := httptest.NewUnstartedServer(handler)
	edge.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	edge.StartTLS()
	defer edge.Close()
	req, _ := http.NewRequest("POST", edge.URL+"/v1/parents", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer test-credential")
	req.Header.Set("Idempotency-Key", "must-not-retry")
	req.Header.Set("Cookie", "unrelated")
	req.Header.Set("X-Forwarded-Host", "untrusted.example")
	response, err := edge.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 || response.TLS.Version != tls.VersionTLS13 {
		t.Fatal("TLS parent forwarding failed")
	}
	for _, path := range []string{"/v1/executions", "/v1/parents/../../executions", "/v1/parents?override=true", "/v1/parents/not-a-hash", "/v1/parents%2fescape"} {
		req, _ := http.NewRequest("POST", edge.URL+path, strings.NewReader(`{}`))
		response, err := edge.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 404 {
			t.Fatalf("unexpected route %s: %d", path, response.StatusCode)
		}
	}
	oversized := io.LimitReader(strings.NewReader(strings.Repeat("x", 65537)), 65537)
	req, _ = http.NewRequest("POST", edge.URL+"/v1/parents", oversized)
	response, err = edge.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 413 || calls.Load() != 1 {
		t.Fatal("oversized/forbidden request reached dispatcher")
	}
	// A certificate is required even on loopback; never insecure-skip-verify.
	untrusted := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}}
	defer untrusted.CloseIdleConnections()
	if response, err := untrusted.Get(edge.URL + "/v1/parents"); err == nil {
		response.Body.Close()
		t.Fatal("untrusted TLS certificate accepted")
	}
}

func TestParentEdgeRefusesRemoteOrMutableBackend(t *testing.T) {
	for _, origin := range []string{"http://localhost:8787", "http://example.com:8787", "https://127.0.0.1:8787", "http://127.0.0.1:8787/path", "http://user:pass@127.0.0.1:8787", "http://127.0.0.1:8787?x=y"} {
		if _, close, err := New(origin); err == nil {
			close()
			t.Fatalf("accepted backend %s", origin)
		}
	}
}

func TestScopedReceiverEdgeForwardsOnlyTheScopedProtocol(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"POST", "/v1/scoped/prepare", true}, {"POST", "/v1/scoped/start", true},
		{"POST", "/v1/scoped/read", true}, {"POST", "/v1/scoped/cleanup", true},
		{"GET", "/v1/scoped/read", false}, {"POST", "/v1/scoped/", false},
		{"POST", "/v1/scoped/start/extra", false}, {"POST", "/v1/scoped/../parents", false},
		{"POST", "/v1/parents", false}, {"POST", "/v1/executions", false},
		{"POST", "/v1/drain", false}, {"GET", "/v1/health", false},
	} {
		if scopedRoute(tc.method, tc.path) != tc.allowed {
			t.Fatalf("unexpected scoped route %s %s", tc.method, tc.path)
		}
		if tc.allowed && (route(tc.method, tc.path) || executionRoute(tc.method, tc.path)) {
			t.Fatal("scoped route leaked into another listener")
		}
	}
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer operator-only" || r.Header.Get("X-Celln-Execution-Permit") != "execution-only" || r.Header.Get("X-Celln-Model-Permit") != "model-only" {
			t.Error("operator bearer or permits did not reach the receiver unchanged")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	handler, closeTransport, err := NewScoped(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	edge := httptest.NewUnstartedServer(handler)
	edge.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	edge.StartTLS()
	defer edge.Close()
	// The receiver accepts 256 KiB; a prepared operation above the parent
	// protocol's 64 KiB bound must still pass this edge.
	req, _ := http.NewRequest("POST", edge.URL+"/v1/scoped/start", strings.NewReader(strings.Repeat("x", 200000)))
	req.Header.Set("Authorization", "Bearer operator-only")
	req.Header.Set("X-Celln-Execution-Permit", "execution-only")
	req.Header.Set("X-Celln-Model-Permit", "model-only")
	response, err := edge.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || calls.Load() != 1 {
		t.Fatalf("scoped request was not forwarded: %d", response.StatusCode)
	}
	for _, tc := range []struct {
		path string
		body int
		want int
	}{{"/v1/parents", 2, 404}, {"/v1/drain", 2, 404}, {"/v1/scoped/start?owner=other", 2, 404}, {"/v1/scoped/prepare", 262145, 413}} {
		req, _ := http.NewRequest("POST", edge.URL+tc.path, strings.NewReader(strings.Repeat("x", tc.body)))
		response, err := edge.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("unexpected status for %s: %d", tc.path, response.StatusCode)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("forbidden or oversized request reached the receiver")
	}
}
