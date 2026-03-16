package mcpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBridgeDiscoverToolsWithPrefix(t *testing.T) {
	tools := []MCPTool{
		{Name: "diagnose_gateway", Description: "Diagnose gateway", InputSchema: map[string]any{"type": "object"}},
		{Name: "list_routes", Description: "List routes", InputSchema: map[string]any{"type": "object"}},
	}

	srv := mockMCPServer(t, tools, nil)
	defer srv.Close()

	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{Name: "k8s-net", URL: srv.URL, ToolsPrefix: "k8s_net", Timeout: 10, Transport: "streamable-http"},
		},
	}

	ipcDir := t.TempDir()
	manifestPath := filepath.Join(ipcDir, "mcp-tools.json")

	bridge := NewBridge(cfg, ipcDir, manifestPath, "test-run")
	manifest, err := bridge.discoverTools(context.Background())
	if err != nil {
		t.Fatalf("discoverTools failed: %v", err)
	}

	if len(manifest.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(manifest.Tools))
	}

	// Verify prefix application (ADR-004)
	if manifest.Tools[0].Name != "k8s_net_diagnose_gateway" {
		t.Errorf("tool[0].Name = %q, want %q", manifest.Tools[0].Name, "k8s_net_diagnose_gateway")
	}
	if manifest.Tools[1].Name != "k8s_net_list_routes" {
		t.Errorf("tool[1].Name = %q, want %q", manifest.Tools[1].Name, "k8s_net_list_routes")
	}

	// Verify server routing index
	if bridge.toolIndex["k8s_net_diagnose_gateway"] != "k8s-net" {
		t.Error("tool index not populated correctly")
	}
}

func TestBridgeDiscoverToolsWithAllowFilter(t *testing.T) {
	tools := []MCPTool{
		{Name: "get_pods", Description: "Get pods", InputSchema: map[string]any{"type": "object"}},
		{Name: "get_nodes", Description: "Get nodes", InputSchema: map[string]any{"type": "object"}},
		{Name: "delete_pod", Description: "Delete pod", InputSchema: map[string]any{"type": "object"}},
	}

	srv := mockMCPServer(t, tools, nil)
	defer srv.Close()

	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{
				Name: "k8s", URL: srv.URL, ToolsPrefix: "k8s", Timeout: 10, Transport: "streamable-http",
				ToolsAllow: []string{"get_pods", "get_nodes"},
			},
		},
	}

	bridge := NewBridge(cfg, t.TempDir(), filepath.Join(t.TempDir(), "manifest.json"), "test")
	manifest, err := bridge.discoverTools(context.Background())
	if err != nil {
		t.Fatalf("discoverTools failed: %v", err)
	}

	if len(manifest.Tools) != 2 {
		t.Fatalf("expected 2 tools (filtered), got %d", len(manifest.Tools))
	}
	if manifest.Tools[0].Name != "k8s_get_pods" {
		t.Errorf("tool[0].Name = %q, want %q", manifest.Tools[0].Name, "k8s_get_pods")
	}

	// Verify filtered tool is NOT in the index
	if _, ok := bridge.toolIndex["k8s_delete_pod"]; ok {
		t.Error("filtered tool should not be in toolIndex")
	}
}

func TestBridgeDiscoverToolsWithDenyFilter(t *testing.T) {
	tools := []MCPTool{
		{Name: "get_pods", Description: "Get pods", InputSchema: map[string]any{"type": "object"}},
		{Name: "delete_pod", Description: "Delete pod", InputSchema: map[string]any{"type": "object"}},
	}

	srv := mockMCPServer(t, tools, nil)
	defer srv.Close()

	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{
				Name: "k8s", URL: srv.URL, ToolsPrefix: "k8s", Timeout: 10, Transport: "streamable-http",
				ToolsDeny: []string{"delete_pod"},
			},
		},
	}

	bridge := NewBridge(cfg, t.TempDir(), filepath.Join(t.TempDir(), "manifest.json"), "test")
	manifest, err := bridge.discoverTools(context.Background())
	if err != nil {
		t.Fatalf("discoverTools failed: %v", err)
	}

	if len(manifest.Tools) != 1 {
		t.Fatalf("expected 1 tool (filtered), got %d", len(manifest.Tools))
	}
	if manifest.Tools[0].Name != "k8s_get_pods" {
		t.Errorf("tool[0].Name = %q, want %q", manifest.Tools[0].Name, "k8s_get_pods")
	}
}

func TestBridgeDiscoverToolsMultiServer(t *testing.T) {
	tools1 := []MCPTool{{Name: "tool_a", Description: "Tool A", InputSchema: map[string]any{"type": "object"}}}
	tools2 := []MCPTool{{Name: "tool_b", Description: "Tool B", InputSchema: map[string]any{"type": "object"}}}

	srv1 := mockMCPServer(t, tools1, nil)
	defer srv1.Close()
	srv2 := mockMCPServer(t, tools2, nil)
	defer srv2.Close()

	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{Name: "srv1", URL: srv1.URL, ToolsPrefix: "s1", Timeout: 10, Transport: "streamable-http"},
			{Name: "srv2", URL: srv2.URL, ToolsPrefix: "s2", Timeout: 10, Transport: "streamable-http"},
		},
	}

	bridge := NewBridge(cfg, t.TempDir(), filepath.Join(t.TempDir(), "manifest.json"), "test")
	manifest, err := bridge.discoverTools(context.Background())
	if err != nil {
		t.Fatalf("discoverTools failed: %v", err)
	}

	if len(manifest.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(manifest.Tools))
	}

	if manifest.Tools[0].Name != "s1_tool_a" {
		t.Errorf("tool[0].Name = %q, want %q", manifest.Tools[0].Name, "s1_tool_a")
	}
	if manifest.Tools[1].Name != "s2_tool_b" {
		t.Errorf("tool[1].Name = %q, want %q", manifest.Tools[1].Name, "s2_tool_b")
	}
}

func TestBridgeDiscoverToolsServerUnavailable(t *testing.T) {
	tools := []MCPTool{{Name: "available_tool", Description: "Works", InputSchema: map[string]any{"type": "object"}}}
	srv := mockMCPServer(t, tools, nil)
	defer srv.Close()

	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{Name: "down", URL: "http://localhost:1", ToolsPrefix: "down", Timeout: 1, Transport: "streamable-http"},
			{Name: "up", URL: srv.URL, ToolsPrefix: "up", Timeout: 10, Transport: "streamable-http"},
		},
	}

	bridge := NewBridge(cfg, t.TempDir(), filepath.Join(t.TempDir(), "manifest.json"), "test")
	manifest, err := bridge.discoverTools(context.Background())
	if err != nil {
		t.Fatalf("discoverTools should not fail: %v", err)
	}

	// Only the available server's tools should be in the manifest
	if len(manifest.Tools) != 1 {
		t.Fatalf("expected 1 tool (from available server), got %d", len(manifest.Tools))
	}
	if manifest.Tools[0].Name != "up_available_tool" {
		t.Errorf("tool[0].Name = %q, want %q", manifest.Tools[0].Name, "up_available_tool")
	}
}

func TestBridgeResolveByPrefix(t *testing.T) {
	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{Name: "k8s-net", URL: "http://localhost", ToolsPrefix: "k8s_net", Timeout: 10},
			{Name: "otel", URL: "http://localhost", ToolsPrefix: "otel", Timeout: 10},
		},
	}

	bridge := NewBridge(cfg, "", "", "")
	// Populate tool index
	bridge.toolIndex["k8s_net_diagnose_gateway"] = "k8s-net"
	bridge.toolIndex["otel_analyze_pipeline"] = "otel"

	tests := []struct {
		prefixed     string
		wantServer   string
		wantToolName string
	}{
		{"k8s_net_diagnose_gateway", "k8s-net", "diagnose_gateway"},
		{"otel_analyze_pipeline", "otel", "analyze_pipeline"},
		{"unknown_tool", "", "unknown_tool"},
	}

	for _, tt := range tests {
		server, tool := bridge.resolveByPrefix(tt.prefixed)
		if server != tt.wantServer {
			t.Errorf("resolveByPrefix(%q) server = %q, want %q", tt.prefixed, server, tt.wantServer)
		}
		if tool != tt.wantToolName {
			t.Errorf("resolveByPrefix(%q) tool = %q, want %q", tt.prefixed, tool, tt.wantToolName)
		}
	}
}

func TestBridgeHandleRequest(t *testing.T) {
	callHandler := func(params MCPToolCallParams) (*MCPToolCallResult, *JSONRPCError) {
		if params.Name != "diagnose_gateway" {
			return nil, &JSONRPCError{Code: -1, Message: "unknown tool: " + params.Name}
		}
		return &MCPToolCallResult{
			Content: []MCPContent{{Type: "text", Text: "gateway is healthy"}},
		}, nil
	}

	srv := mockMCPServer(t, nil, callHandler)
	defer srv.Close()

	ipcDir := t.TempDir()
	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{Name: "k8s-net", URL: srv.URL, ToolsPrefix: "k8s_net", Timeout: 10, Transport: "streamable-http"},
		},
	}

	bridge := NewBridge(cfg, ipcDir, filepath.Join(ipcDir, "mcp-tools.json"), "test")

	// Set up client (normally done during discoverTools)
	client := NewClient(cfg.Servers[0])
	_ = client.initialize(context.Background())
	bridge.clients["k8s-net"] = client
	bridge.toolIndex["k8s_net_diagnose_gateway"] = "k8s-net"

	// Write a request file
	req := MCPRequest{
		ID:        "test-123",
		Tool:      "k8s_net_diagnose_gateway",
		Arguments: json.RawMessage(`{"namespace":"default"}`),
	}
	reqData, _ := json.Marshal(req)
	reqPath := filepath.Join(ipcDir, "mcp-request-test-123.json")
	os.WriteFile(reqPath, reqData, 0o644)

	// Process the request
	bridge.handleRequest(context.Background(), reqPath)

	// Check result file was written
	resPath := filepath.Join(ipcDir, "mcp-result-test-123.json")
	resData, err := os.ReadFile(resPath)
	if err != nil {
		t.Fatalf("result file not found: %v", err)
	}

	var result MCPResult
	if err := json.Unmarshal(resData, &result); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.ID != "test-123" {
		t.Errorf("result.ID = %q, want %q", result.ID, "test-123")
	}

	// Verify content
	var content []MCPContent
	json.Unmarshal(result.Content, &content)
	if len(content) != 1 || content[0].Text != "gateway is healthy" {
		t.Errorf("unexpected content: %s", string(result.Content))
	}

	// Verify request file was cleaned up
	if _, err := os.Stat(reqPath); !os.IsNotExist(err) {
		t.Error("request file was not cleaned up")
	}
}

func TestBridgeHandleRequestFilteredTool(t *testing.T) {
	ipcDir := t.TempDir()
	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{Name: "k8s-net", URL: "http://localhost", ToolsPrefix: "k8s_net", Timeout: 10},
		},
	}
	bridge := NewBridge(cfg, ipcDir, "", "test")
	// Only register one tool in the index (simulating filtering)
	bridge.toolIndex["k8s_net_get_pods"] = "k8s-net"

	// Try to call a tool that was filtered out
	req := MCPRequest{ID: "filtered-1", Tool: "k8s_net_delete_pod", Arguments: json.RawMessage(`{}`)}
	reqData, _ := json.Marshal(req)
	reqPath := filepath.Join(ipcDir, "mcp-request-filtered-1.json")
	os.WriteFile(reqPath, reqData, 0o644)

	bridge.handleRequest(context.Background(), reqPath)

	resPath := filepath.Join(ipcDir, "mcp-result-filtered-1.json")
	resData, err := os.ReadFile(resPath)
	if err != nil {
		t.Fatalf("result file not found: %v", err)
	}

	var result MCPResult
	json.Unmarshal(resData, &result)

	if result.Success {
		t.Error("expected failure for filtered tool")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
	if !contains(result.Error, "filtered") {
		t.Errorf("error should mention filtering, got: %s", result.Error)
	}
}

func TestBridgeHandleRequestServerNotFound(t *testing.T) {
	ipcDir := t.TempDir()
	cfg := &ServersConfig{Servers: []ServerConfig{}}
	bridge := NewBridge(cfg, ipcDir, "", "test")

	req := MCPRequest{ID: "err-1", Tool: "nonexistent_tool", Arguments: json.RawMessage(`{}`)}
	reqData, _ := json.Marshal(req)
	reqPath := filepath.Join(ipcDir, "mcp-request-err-1.json")
	os.WriteFile(reqPath, reqData, 0o644)

	bridge.handleRequest(context.Background(), reqPath)

	resPath := filepath.Join(ipcDir, "mcp-result-err-1.json")
	resData, err := os.ReadFile(resPath)
	if err != nil {
		t.Fatalf("result file not found: %v", err)
	}

	var result MCPResult
	json.Unmarshal(resData, &result)

	if result.Success {
		t.Error("expected failure")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
}

func TestBridgeEndToEnd(t *testing.T) {
	// Full end-to-end: discover tools, write manifest, handle request via watcher
	tools := []MCPTool{
		{Name: "ping", Description: "Ping test", InputSchema: map[string]any{"type": "object"}},
	}
	callHandler := func(params MCPToolCallParams) (*MCPToolCallResult, *JSONRPCError) {
		return &MCPToolCallResult{
			Content: []MCPContent{{Type: "text", Text: "pong"}},
		}, nil
	}

	srv := mockMCPServer(t, tools, callHandler)
	defer srv.Close()

	ipcDir := t.TempDir()
	toolsDir := filepath.Join(ipcDir, "tools")
	outputDir := filepath.Join(ipcDir, "output")
	os.MkdirAll(toolsDir, 0o755)
	os.MkdirAll(outputDir, 0o755)

	manifestPath := filepath.Join(toolsDir, "mcp-tools.json")

	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{Name: "test-srv", URL: srv.URL, ToolsPrefix: "tst", Timeout: 10, Transport: "streamable-http"},
		},
	}

	bridge := NewBridge(cfg, toolsDir, manifestPath, "e2e-run")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Discover tools
	manifest, err := bridge.discoverTools(ctx)
	if err != nil {
		t.Fatalf("discoverTools: %v", err)
	}
	if err := WriteManifest(manifestPath, manifest); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	// Verify manifest
	if len(manifest.Tools) != 1 || manifest.Tools[0].Name != "tst_ping" {
		t.Fatalf("unexpected manifest: %+v", manifest.Tools)
	}

	// Start watcher in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- bridge.watchAndDispatch(ctx)
	}()

	// Give watcher time to start
	time.Sleep(200 * time.Millisecond)

	// Write an MCP request
	req := MCPRequest{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		Tool:      "tst_ping",
		Arguments: json.RawMessage(`{}`),
	}
	reqData, _ := json.Marshal(req)
	reqPath := filepath.Join(toolsDir, fmt.Sprintf("mcp-request-%s.json", req.ID))
	if err := os.WriteFile(reqPath, reqData, 0o644); err != nil {
		t.Fatalf("writing request: %v", err)
	}

	// Poll for result
	resPath := filepath.Join(toolsDir, fmt.Sprintf("mcp-result-%s.json", req.ID))
	var result MCPResult
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(resPath)
		if err == nil {
			if json.Unmarshal(data, &result) == nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if result.ID == "" {
		t.Fatal("timeout waiting for MCP result")
	}

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	var content []MCPContent
	json.Unmarshal(result.Content, &content)
	if len(content) != 1 || content[0].Text != "pong" {
		t.Errorf("unexpected content: %s", string(result.Content))
	}

	// Signal agent done to stop watcher
	cancel()
	<-errCh
}

func TestDiscoverAndWriteManifest(t *testing.T) {
	tools := []MCPTool{
		{Name: "get_pods", Description: "Get pods", InputSchema: map[string]any{"type": "object"}},
		{Name: "get_nodes", Description: "Get nodes", InputSchema: map[string]any{"type": "object"}},
	}

	srv := mockMCPServer(t, tools, nil)
	defer srv.Close()

	cfg := &ServersConfig{
		Servers: []ServerConfig{
			{Name: "k8s", URL: srv.URL, ToolsPrefix: "k8s", Timeout: 10, Transport: "streamable-http"},
		},
	}

	ipcDir := t.TempDir()
	manifestPath := filepath.Join(ipcDir, "mcp-tools.json")

	bridge := NewBridge(cfg, ipcDir, manifestPath, "test-run")
	err := bridge.DiscoverAndWriteManifest(context.Background())
	if err != nil {
		t.Fatalf("DiscoverAndWriteManifest failed: %v", err)
	}

	// Verify manifest file was written
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("Failed to read manifest: %v", err)
	}

	var manifest MCPToolManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("Failed to parse manifest: %v", err)
	}

	if len(manifest.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(manifest.Tools))
	}

	if manifest.Tools[0].Name != "k8s_get_pods" {
		t.Errorf("tool[0].Name = %q, want %q", manifest.Tools[0].Name, "k8s_get_pods")
	}
}

func TestExtractTraceparent(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		valid   bool
		traceID string
		spanID  string
		sampled bool
	}{
		{
			name:    "valid sampled",
			input:   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			valid:   true,
			traceID: "4bf92f3577b34da6a3ce929d0e0e4736",
			spanID:  "00f067aa0ba902b7",
			sampled: true,
		},
		{
			name:    "valid not sampled",
			input:   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00",
			valid:   true,
			traceID: "4bf92f3577b34da6a3ce929d0e0e4736",
			spanID:  "00f067aa0ba902b7",
			sampled: false,
		},
		{name: "empty string", input: "", valid: false},
		{name: "wrong version", input: "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", valid: false},
		{name: "too few parts", input: "00-abc-01", valid: false},
		{name: "invalid trace id", input: "00-invalidtraceid-00f067aa0ba902b7-01", valid: false},
		{name: "invalid span id", input: "00-4bf92f3577b34da6a3ce929d0e0e4736-invalidspan-01", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := extractTraceparent(tt.input)
			if tt.valid {
				if !sc.IsValid() {
					t.Fatal("expected valid SpanContext")
				}
				if sc.TraceID().String() != tt.traceID {
					t.Errorf("traceID = %s, want %s", sc.TraceID(), tt.traceID)
				}
				if sc.SpanID().String() != tt.spanID {
					t.Errorf("spanID = %s, want %s", sc.SpanID(), tt.spanID)
				}
				if sc.IsSampled() != tt.sampled {
					t.Errorf("sampled = %v, want %v", sc.IsSampled(), tt.sampled)
				}
				if !sc.IsRemote() {
					t.Error("expected remote=true")
				}
			} else {
				if sc.IsValid() {
					t.Error("expected invalid SpanContext")
				}
			}
		})
	}
}
