package mcpbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-tools.json")

	manifest := &MCPToolManifest{
		Tools: []MCPToolDef{
			{
				Name:        "k8s_net_diagnose_gateway",
				Description: "Diagnose gateway",
				Server:      "k8s-networking",
				Timeout:     30,
				InputSchema: map[string]any{"type": "object"},
			},
			{
				Name:        "otel_analyze_pipeline",
				Description: "Analyze pipeline",
				Server:      "otel-collector",
				Timeout:     60,
				InputSchema: map[string]any{"type": "object"},
			},
		},
	}

	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest failed: %v", err)
	}

	// Verify file exists and is valid JSON
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}

	var loaded MCPToolManifest
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}

	if len(loaded.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(loaded.Tools))
	}
	if loaded.Tools[0].Name != "k8s_net_diagnose_gateway" {
		t.Errorf("tool[0].Name = %q, want %q", loaded.Tools[0].Name, "k8s_net_diagnose_gateway")
	}
	if loaded.Tools[1].Server != "otel-collector" {
		t.Errorf("tool[1].Server = %q, want %q", loaded.Tools[1].Server, "otel-collector")
	}

	// Verify temp file was cleaned up
	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file was not cleaned up")
	}
}

func TestWriteManifestEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-tools.json")

	manifest := &MCPToolManifest{Tools: []MCPToolDef{}}

	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}

	var loaded MCPToolManifest
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("parsing manifest: %v", err)
	}

	if len(loaded.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(loaded.Tools))
	}
}

func TestFilterToolsAllowOnly(t *testing.T) {
	tools := []MCPTool{
		{Name: "get_pods"},
		{Name: "get_nodes"},
		{Name: "delete_pod"},
		{Name: "list_namespaces"},
	}

	filtered := filterTools(tools, []string{"get_pods", "get_nodes"}, nil)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(filtered))
	}
	if filtered[0].Name != "get_pods" || filtered[1].Name != "get_nodes" {
		t.Errorf("unexpected tools: %v", filtered)
	}
}

func TestFilterToolsDenyOnly(t *testing.T) {
	tools := []MCPTool{
		{Name: "get_pods"},
		{Name: "get_nodes"},
		{Name: "delete_pod"},
	}

	filtered := filterTools(tools, nil, []string{"delete_pod"})
	if len(filtered) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(filtered))
	}
	if filtered[0].Name != "get_pods" || filtered[1].Name != "get_nodes" {
		t.Errorf("unexpected tools: %v", filtered)
	}
}

func TestFilterToolsBothAllowAndDeny(t *testing.T) {
	tools := []MCPTool{
		{Name: "get_pods"},
		{Name: "get_nodes"},
		{Name: "delete_pod"},
		{Name: "list_namespaces"},
	}

	// Allow 3, then deny 1 of those
	filtered := filterTools(tools, []string{"get_pods", "get_nodes", "delete_pod"}, []string{"delete_pod"})
	if len(filtered) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(filtered))
	}
	if filtered[0].Name != "get_pods" || filtered[1].Name != "get_nodes" {
		t.Errorf("unexpected tools: %v", filtered)
	}
}

func TestFilterToolsNeither(t *testing.T) {
	tools := []MCPTool{
		{Name: "get_pods"},
		{Name: "get_nodes"},
	}

	filtered := filterTools(tools, nil, nil)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(filtered))
	}
}

func TestFilterToolsEmptyResult(t *testing.T) {
	tools := []MCPTool{
		{Name: "get_pods"},
	}

	filtered := filterTools(tools, []string{"nonexistent"}, nil)
	if len(filtered) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(filtered))
	}
}

func TestWriteManifestCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "mcp-tools.json")

	manifest := &MCPToolManifest{Tools: []MCPToolDef{}}
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest failed: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("manifest file was not created")
	}
}
