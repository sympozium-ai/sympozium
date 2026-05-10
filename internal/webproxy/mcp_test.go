package webproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteError(t *testing.T) {
	tests := []struct {
		name   string
		msg    string
		status int
	}{
		{"not found", "not found", http.StatusNotFound},
		{"internal error", "internal server error", http.StatusInternalServerError},
		{"bad request", "invalid input", http.StatusBadRequest},
		{"empty message", "", http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeError(w, tc.status, tc.msg)

			if w.Code != tc.status {
				t.Errorf("expected status %d, got %d", tc.status, w.Code)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("expected Content-Type application/json, got %q", ct)
			}

			body := w.Body.String()
			if !strings.Contains(body, `"type":"error"`) {
				t.Errorf("expected error type in body, got %q", body)
			}
			if tc.msg != "" && !strings.Contains(body, tc.msg) {
				t.Errorf("expected message %q in body, got %q", tc.msg, body)
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	data := map[string]any{"status": "ok", "count": 42}
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, data)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"count":42`) {
		t.Errorf("expected count 42 in body, got: %s", body)
	}
}

func TestWriteJSON_NilValue(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, nil)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body != "null" {
		t.Errorf("expected null body, got %q", body)
	}
}

func TestBuildSessionKey(t *testing.T) {
	key := buildSessionKey("my-instance")
	if len(key) == 0 {
		t.Error("expected non-empty session key")
	}
	if !strings.Contains(key, "my-instance") {
		t.Errorf("expected key to contain instance name, got %q", key)
	}
	if !strings.HasPrefix(key, "mcp-") {
		t.Errorf("expected key to start with 'mcp-', got %q", key)
	}
}

func TestBuildSessionKey_DifferentInputs(t *testing.T) {
	key1 := buildSessionKey("instance-a")
	key2 := buildSessionKey("instance-b")
	if key1 == key2 {
		t.Error("expected different session keys for different instance names")
	}
}

func TestFormatMCPToolResult_Success(t *testing.T) {
	result := formatMCPToolResult(`{"message": "all good"}`)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0]["type"] != "text" {
		t.Errorf("expected type=text, got %v", result[0]["type"])
	}
	if result[0]["text"] != `{"message": "all good"}` {
		t.Errorf("expected text to match, got %v", result[0]["text"])
	}
}

func TestFormatMCPToolResult_TrimsWhitespace(t *testing.T) {
	result := formatMCPToolResult("  hello world  \n")
	if result[0]["text"] != "hello world" {
		t.Errorf("expected trimmed text, got %q", result[0]["text"])
	}
}

func TestFormatMCPToolResult_Error(t *testing.T) {
	result := formatMCPToolResult("__ERROR__ something went wrong")
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0]["type"] != "text" {
		t.Errorf("expected type=text, got %v", result[0]["type"])
	}
	// Error prefix is passed through as-is (not stripped)
	if result[0]["text"] != "__ERROR__ something went wrong" {
		t.Errorf("expected exact error text, got %v", result[0]["text"])
	}
}

func TestFormatMCPToolResult_EmptyString(t *testing.T) {
	result := formatMCPToolResult("")
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0]["text"] != "" {
		t.Errorf("expected empty text, got %q", result[0]["text"])
	}
}
