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
