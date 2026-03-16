package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMCPTools(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "mcp-tools.json")

	manifest := mcpToolManifest{
		Tools: []mcpToolEntry{
			{
				Name:        "k8s_net_diagnose_gateway",
				Description: "Diagnose a Gateway API resource",
				Server:      "k8s-networking",
				Timeout:     30,
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"namespace": map[string]any{"type": "string"},
					},
				},
			},
			{
				Name:        "otel_analyze_pipeline",
				Description: "Analyze an OTel pipeline",
				Server:      "otel-collector",
				Timeout:     60,
				InputSchema: map[string]any{"type": "object"},
			},
		},
	}

	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	// Reset global registry
	mcpToolRegistryMu.Lock()
	mcpToolRegistry = map[string]mcpToolEntry{}
	mcpToolRegistryMu.Unlock()

	tools := loadMCPTools(manifestPath)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	// Check first tool
	if tools[0].Name != "k8s_net_diagnose_gateway" {
		t.Errorf("tools[0].Name = %q, want %q", tools[0].Name, "k8s_net_diagnose_gateway")
	}
	if tools[0].Description != "Diagnose a Gateway API resource [MCP: k8s-networking]" {
		t.Errorf("tools[0].Description = %q", tools[0].Description)
	}
	if tools[0].Parameters == nil {
		t.Error("tools[0].Parameters is nil")
	}

	// Check registry was populated
	if _, ok := lookupMCPTool("k8s_net_diagnose_gateway"); !ok {
		t.Error("mcpToolRegistry missing k8s_net_diagnose_gateway")
	}
	if _, ok := lookupMCPTool("otel_analyze_pipeline"); !ok {
		t.Error("mcpToolRegistry missing otel_analyze_pipeline")
	}
}

func TestLoadMCPToolsNoFile(t *testing.T) {
	// Reset
	mcpToolRegistryMu.Lock()
	mcpToolRegistry = map[string]mcpToolEntry{}
	mcpToolRegistryMu.Unlock()

	// Use a short timeout by testing with a non-existent path
	// This would normally wait 15s but the file will never appear
	// so we test the "no manifest" path by providing an empty manifest
	dir := t.TempDir()
	emptyManifest := filepath.Join(dir, "mcp-tools.json")
	if err := os.WriteFile(emptyManifest, []byte(`{"tools":[]}`), 0o644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	tools := loadMCPTools(emptyManifest)
	if tools != nil {
		t.Errorf("expected nil for empty manifest, got %d tools", len(tools))
	}
}

func TestLoadMCPToolsInvalidJSON(t *testing.T) {
	mcpToolRegistryMu.Lock()
	mcpToolRegistry = map[string]mcpToolEntry{}
	mcpToolRegistryMu.Unlock()

	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-tools.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	tools := loadMCPTools(path)
	if tools != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestFormatMCPResult(t *testing.T) {
	tests := []struct {
		name   string
		result mcpResult
		want   string
	}{
		{
			name: "success with text content",
			result: mcpResult{
				ID:      "1",
				Success: true,
				Content: mustMarshal([]mcpContent{{Type: "text", Text: "gateway is healthy"}}),
			},
			want: "gateway is healthy",
		},
		{
			name: "success with multiple content blocks",
			result: mcpResult{
				ID:      "2",
				Success: true,
				Content: mustMarshal([]mcpContent{
					{Type: "text", Text: "line 1"},
					{Type: "text", Text: "line 2"},
				}),
			},
			want: "line 1\nline 2",
		},
		{
			name: "error with message",
			result: mcpResult{
				ID:      "3",
				Success: false,
				Error:   "connection refused",
			},
			want: "MCP Error: connection refused",
		},
		{
			name: "isError with content",
			result: mcpResult{
				ID:      "4",
				Success: false,
				IsError: true,
				Content: mustMarshal([]mcpContent{{Type: "text", Text: "tool failed"}}),
			},
			want: "MCP Error: tool failed",
		},
		{
			name: "success with empty content",
			result: mcpResult{
				ID:      "5",
				Success: true,
				Content: mustMarshal([]mcpContent{}),
			},
			want: "(no output)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatMCPResult(tt.result)
			if got != tt.want {
				t.Errorf("formatMCPResult() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteMCPToolWritesRequest(t *testing.T) {
	dir := t.TempDir()

	// Override IPC path for testing
	oldDir := mcpToolsDir
	mcpToolsDir = dir
	t.Cleanup(func() { mcpToolsDir = oldDir })

	tool := mcpToolEntry{
		Name:    "test_ping",
		Server:  "test-srv",
		Timeout: 2,
	}

	// Write a result file before calling executeMCPTool so it returns quickly
	// We need to know the request ID - use a goroutine to watch for the request
	go func() {
		// Poll for the request file to appear
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			entries, _ := os.ReadDir(dir)
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) == ".json" && len(entry.Name()) > 12 && entry.Name()[:12] == "mcp-request-" {
					// Read the request to get the ID
					reqData, err := os.ReadFile(filepath.Join(dir, entry.Name()))
					if err != nil {
						continue
					}
					var req mcpRequest
					if err := json.Unmarshal(reqData, &req); err != nil {
						continue
					}
					// Write the result
					result := mcpResult{
						ID:      req.ID,
						Success: true,
						Content: mustMarshal([]mcpContent{{Type: "text", Text: "pong"}}),
					}
					resData, _ := json.Marshal(result)
					resPath := filepath.Join(dir, "mcp-result-"+req.ID+".json")
					os.WriteFile(resPath, resData, 0o644)
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	output := executeMCPTool(context.Background(), tool, `{"key":"value"}`)
	if output != "pong" {
		t.Errorf("executeMCPTool() = %q, want %q", output, "pong")
	}
}

func TestExecuteMCPToolTimeout(t *testing.T) {
	dir := t.TempDir()

	// Override IPC path for testing
	oldDir := mcpToolsDir
	mcpToolsDir = dir
	t.Cleanup(func() { mcpToolsDir = oldDir })

	tool := mcpToolEntry{
		Name:    "slow_tool",
		Server:  "test",
		Timeout: 1, // 1 second timeout + 10s buffer = 11s, but context will cancel
	}

	// Use a context with short deadline
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	output := executeMCPTool(ctx, tool, `{}`)
	if output != "Error: context cancelled while waiting for MCP tool result" {
		t.Errorf("expected context cancelled error, got %q", output)
	}
}

func TestExecuteMCPToolInvalidArgs(t *testing.T) {
	output := executeMCPTool(context.Background(), mcpToolEntry{Name: "t", Server: "s"}, "not json")
	if output != "Error: invalid JSON arguments for MCP tool call" {
		t.Errorf("expected invalid JSON error, got %q", output)
	}
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
