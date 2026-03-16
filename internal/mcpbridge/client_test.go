package mcpbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockMCPServer creates a test HTTP server that implements the MCP protocol.
func mockMCPServer(t *testing.T, tools []MCPTool, callHandler func(params MCPToolCallParams) (*MCPToolCallResult, *JSONRPCError)) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "test-session-123")

		var result any
		var rpcErr *JSONRPCError

		switch req.Method {
		case "initialize":
			result = MCPInitializeResult{
				ProtocolVersion: "2025-03-26",
				ServerInfo:      MCPImplementation{Name: "test-server", Version: "1.0"},
			}

		case "notifications/initialized":
			// Notification — no response needed, but we return 200
			w.WriteHeader(http.StatusOK)
			return

		case "tools/list":
			result = MCPToolsListResult{Tools: tools}

		case "tools/call":
			if callHandler != nil {
				paramsBytes, _ := json.Marshal(req.Params)
				var params MCPToolCallParams
				json.Unmarshal(paramsBytes, &params)
				result, rpcErr = callHandler(params)
			} else {
				result = MCPToolCallResult{
					Content: []MCPContent{{Type: "text", Text: "default response"}},
				}
			}

		default:
			rpcErr = &JSONRPCError{Code: -32601, Message: "method not found"}
		}

		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      &req.ID,
		}

		if rpcErr != nil {
			resp.Error = rpcErr
		} else {
			resultBytes, _ := json.Marshal(result)
			resp.Result = resultBytes
		}

		json.NewEncoder(w).Encode(resp)
	}))
}

func TestClientDiscoverTools(t *testing.T) {
	tools := []MCPTool{
		{
			Name:        "diagnose_gateway",
			Description: "Diagnose a Gateway API resource",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{"type": "string"},
				},
			},
		},
		{
			Name:        "list_routes",
			Description: "List HTTP routes",
			InputSchema: map[string]any{"type": "object"},
		},
	}

	srv := mockMCPServer(t, tools, nil)
	defer srv.Close()

	client := NewClient(ServerConfig{
		Name:        "test",
		URL:         srv.URL,
		ToolsPrefix: "test",
		Timeout:     10,
		Transport:   "streamable-http",
	})

	discovered, err := client.DiscoverTools(context.Background())
	if err != nil {
		t.Fatalf("DiscoverTools failed: %v", err)
	}

	if len(discovered) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(discovered))
	}

	if discovered[0].Name != "diagnose_gateway" {
		t.Errorf("tool[0].Name = %q, want %q", discovered[0].Name, "diagnose_gateway")
	}
	if discovered[1].Description != "List HTTP routes" {
		t.Errorf("tool[1].Description = %q, want %q", discovered[1].Description, "List HTTP routes")
	}

	// Verify session ID was captured
	if client.sessionID != "test-session-123" {
		t.Errorf("sessionID = %q, want %q", client.sessionID, "test-session-123")
	}
}

func TestClientCallTool(t *testing.T) {
	callHandler := func(params MCPToolCallParams) (*MCPToolCallResult, *JSONRPCError) {
		if params.Name != "my_tool" {
			return nil, &JSONRPCError{Code: -1, Message: "unknown tool"}
		}
		return &MCPToolCallResult{
			Content: []MCPContent{
				{Type: "text", Text: "tool result: success"},
			},
		}, nil
	}

	srv := mockMCPServer(t, nil, callHandler)
	defer srv.Close()

	client := NewClient(ServerConfig{
		Name:    "test",
		URL:     srv.URL,
		Timeout: 10,
	})

	// Initialize first
	if err := client.initialize(context.Background()); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}

	args := json.RawMessage(`{"key":"value"}`)
	result, err := client.CallTool(context.Background(), "my_tool", args, nil)
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if result.IsError {
		t.Error("expected success, got isError=true")
	}
	if len(result.Content) != 1 || result.Content[0].Text != "tool result: success" {
		t.Errorf("unexpected result content: %+v", result.Content)
	}
}

func TestClientCallToolWithMeta(t *testing.T) {
	var receivedMeta map[string]any

	callHandler := func(params MCPToolCallParams) (*MCPToolCallResult, *JSONRPCError) {
		receivedMeta = params.Meta
		return &MCPToolCallResult{
			Content: []MCPContent{{Type: "text", Text: "ok"}},
		}, nil
	}

	srv := mockMCPServer(t, nil, callHandler)
	defer srv.Close()

	client := NewClient(ServerConfig{Name: "test", URL: srv.URL, Timeout: 10})
	_ = client.initialize(context.Background())

	meta := map[string]any{"traceparent": "00-abc123-def456-01"}
	_, err := client.CallTool(context.Background(), "tool", nil, meta)
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if receivedMeta == nil {
		t.Fatal("meta not received by server")
	}
	if receivedMeta["traceparent"] != "00-abc123-def456-01" {
		t.Errorf("traceparent = %v, want %q", receivedMeta["traceparent"], "00-abc123-def456-01")
	}
}

func TestClientCallToolError(t *testing.T) {
	callHandler := func(params MCPToolCallParams) (*MCPToolCallResult, *JSONRPCError) {
		return &MCPToolCallResult{
			Content: []MCPContent{{Type: "text", Text: "something went wrong"}},
			IsError: true,
		}, nil
	}

	srv := mockMCPServer(t, nil, callHandler)
	defer srv.Close()

	client := NewClient(ServerConfig{Name: "test", URL: srv.URL, Timeout: 10})
	_ = client.initialize(context.Background())

	result, err := client.CallTool(context.Background(), "tool", nil, nil)
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if !result.IsError {
		t.Error("expected isError=true")
	}
}

func TestClientJSONRPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      intPtr(1),
			Error:   &JSONRPCError{Code: -32600, Message: "invalid request"},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := NewClient(ServerConfig{Name: "test", URL: srv.URL, Timeout: 10})
	err := client.initialize(context.Background())
	if err == nil {
		t.Fatal("expected error from JSON-RPC error response")
	}
	if !contains(err.Error(), "invalid request") {
		t.Errorf("error %q does not contain 'invalid request'", err.Error())
	}
}

func TestClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewClient(ServerConfig{Name: "test", URL: srv.URL, Timeout: 10})
	err := client.initialize(context.Background())
	if err == nil {
		t.Fatal("expected error from HTTP 500")
	}
	if !contains(err.Error(), "HTTP 500") {
		t.Errorf("error %q does not contain 'HTTP 500'", err.Error())
	}
}

func TestClientAuth(t *testing.T) {
	var receivedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      intPtr(1),
			Result:  json.RawMessage(`{"protocolVersion":"2025-03-26","serverInfo":{"name":"test","version":"1.0"}}`),
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	t.Setenv("TEST_MCP_TOKEN", "my-secret-token")

	client := NewClient(ServerConfig{
		Name:    "test",
		URL:     srv.URL,
		Timeout: 10,
		Auth:    &AuthConfig{Type: "bearer", SecretKey: "TEST_MCP_TOKEN"},
	})

	_ = client.initialize(context.Background())

	if receivedAuth != "Bearer my-secret-token" {
		t.Errorf("Authorization = %q, want %q", receivedAuth, "Bearer my-secret-token")
	}
}

func TestClientResponseSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Write more than MaxResponseSize
		big := make([]byte, MaxResponseSize+100)
		for i := range big {
			big[i] = 'x'
		}
		w.Write(big)
	}))
	defer srv.Close()

	client := NewClient(ServerConfig{Name: "test", URL: srv.URL, Timeout: 10})
	err := client.initialize(context.Background())
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	if !contains(err.Error(), "maximum size") {
		t.Errorf("error %q does not mention maximum size", err.Error())
	}
}

func intPtr(i int64) *int64 { return &i }
