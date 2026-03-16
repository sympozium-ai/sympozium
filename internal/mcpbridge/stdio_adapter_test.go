package mcpbridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStdioAdapter_HealthzAlive(t *testing.T) {
	m := NewStdioManager("cat", nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	a := NewStdioAdapter(m, "test-server", 0)
	a.ready.Store(true)

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	a.handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestStdioAdapter_HealthzDead(t *testing.T) {
	m := NewStdioManager("cat", nil, nil)
	// Not started
	a := NewStdioAdapter(m, "test-server", 0)

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	a.handleHealthz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestStdioAdapter_ReadyzReady(t *testing.T) {
	m := NewStdioManager("cat", nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	a := NewStdioAdapter(m, "test-server", 0)
	a.ready.Store(true)

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	a.handleReadyz(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestStdioAdapter_JSONRPC(t *testing.T) {
	m := NewStdioManager("cat", nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	a := NewStdioAdapter(m, "test-server", 0)
	a.ready.Store(true)

	rpcReq := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(rpcReq))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	a.handleJSONRPC(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}
	if resp["method"] != "tools/list" {
		t.Errorf("expected method tools/list in echo response, got %v", resp["method"])
	}
}

func TestStdioAdapter_JSONRPCWhenDead(t *testing.T) {
	m := NewStdioManager("cat", nil, nil)
	// Not started
	a := NewStdioAdapter(m, "test-server", 0)

	rpcReq := `{"jsonrpc":"2.0","id":1,"method":"test"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(rpcReq))
	w := httptest.NewRecorder()

	a.handleJSONRPC(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}
