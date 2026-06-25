package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSidecarTools(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "sidecar-tools-test.json")

	manifest := sidecarToolManifest{
		Tools: []sidecarToolEntry{
			{
				Name:           "sd_select_services",
				Description:    "Select services for enrichment",
				Target:         "SD-Select-Services",
				Subcommand:     "select",
				InputMode:      "args",
				Timeout:        90,
				PositionalArgs: []string{"batchSize"},
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"batchSize": map[string]any{"type": "integer"},
					},
				},
			},
			{
				Name:        "sd_fetch_page",
				Description: "Fetch a web page",
				Target:      "sd-fetch",
				Subcommand:  "fetch-page",
				Exec:        "python /app/fetch.py",
				InputMode:   "stdin",
				Parameters:  map[string]any{"type": "object"},
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

	sidecarToolRegistryMu.Lock()
	sidecarToolRegistry = map[string]sidecarToolEntry{}
	sidecarToolRegistryMu.Unlock()

	tools := loadSidecarTools(dir)
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	if tools[0].Name != "sd_select_services" {
		t.Errorf("tools[0].Name = %q, want %q", tools[0].Name, "sd_select_services")
	}

	entry, ok := lookupSidecarTool("sd_select_services")
	if !ok {
		t.Fatal("sd_select_services not found in registry")
	}
	if entry.Target != "sd-select-services" {
		t.Errorf("target not normalized: got %q, want %q", entry.Target, "sd-select-services")
	}
	if entry.Timeout != 90 {
		t.Errorf("timeout = %d, want 90", entry.Timeout)
	}

	entry2, ok := lookupSidecarTool("sd_fetch_page")
	if !ok {
		t.Fatal("sd_fetch_page not found in registry")
	}
	if entry2.Exec != "python /app/fetch.py" {
		t.Errorf("exec = %q, want %q", entry2.Exec, "python /app/fetch.py")
	}
}

func TestLoadSidecarTools_InvalidInputMode(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "sidecar-tools-bad.json")

	manifest := sidecarToolManifest{
		Tools: []sidecarToolEntry{
			{
				Name:       "bad_mode",
				Target:     "test",
				Subcommand: "run",
				InputMode:  "invalid",
				Parameters: map[string]any{"type": "object"},
			},
		},
	}

	data, _ := json.Marshal(manifest)
	os.WriteFile(manifestPath, data, 0o644)

	sidecarToolRegistryMu.Lock()
	sidecarToolRegistry = map[string]sidecarToolEntry{}
	sidecarToolRegistryMu.Unlock()

	loadSidecarTools(dir)

	entry, ok := lookupSidecarTool("bad_mode")
	if !ok {
		t.Fatal("bad_mode not found in registry")
	}
	if entry.InputMode != "args" {
		t.Errorf("invalid inputMode should default to args, got %q", entry.InputMode)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"with spaces", "'with spaces'"},
		{"it's", "'it'\\''s'"},
		{"$(whoami)", "'$(whoami)'"},
		{"a;rm -rf /", "'a;rm -rf /'"},
		{`"double"`, `'"double"'`},
		{"back`tick`", "'back`tick`'"},
	}

	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildSidecarCommand_ArgsMode(t *testing.T) {
	tests := []struct {
		name     string
		tool     sidecarToolEntry
		argsJSON string
		wantCmd  string
	}{
		{
			name: "simple positional args with default exec",
			tool: sidecarToolEntry{
				Subcommand:     "select",
				InputMode:      "args",
				PositionalArgs: []string{"batchSize"},
			},
			argsJSON: `{"batchSize": 20}`,
			wantCmd:  "node /app/dist/cli.js select '20'",
		},
		{
			name: "custom exec prefix",
			tool: sidecarToolEntry{
				Subcommand:     "run",
				Exec:           "python /app/main.py",
				InputMode:      "args",
				PositionalArgs: []string{"name"},
			},
			argsJSON: `{"name": "test"}`,
			wantCmd:  "python /app/main.py run 'test'",
		},
		{
			name: "positional args with shell metacharacters",
			tool: sidecarToolEntry{
				Subcommand:     "query",
				InputMode:      "args",
				PositionalArgs: []string{"q"},
			},
			argsJSON: `{"q": "my service; rm -rf /"}`,
			wantCmd:  "node /app/dist/cli.js query 'my service; rm -rf /'",
		},
		{
			name: "no positional args",
			tool: sidecarToolEntry{
				Subcommand: "list",
				InputMode:  "args",
			},
			argsJSON: `{}`,
			wantCmd:  "node /app/dist/cli.js list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := buildSidecarCommand(tt.tool, tt.argsJSON)
			if cmd != tt.wantCmd {
				t.Errorf("got:\n  %s\nwant:\n  %s", cmd, tt.wantCmd)
			}
		})
	}
}

func TestBuildSidecarCommand_StdinMode(t *testing.T) {
	tool := sidecarToolEntry{
		Subcommand: "fetch-page",
		InputMode:  "stdin",
	}
	argsJSON := `{"url": "https://example.com", "tier": 1}`

	cmd := buildSidecarCommand(tool, argsJSON)

	if !contains(cmd, "echo ") || !contains(cmd, " | node /app/dist/cli.js fetch-page") {
		t.Errorf("stdin command should pipe through echo: %s", cmd)
	}
}

func TestBuildSidecarCommand_StdinWithPositionalStrip(t *testing.T) {
	tool := sidecarToolEntry{
		Subcommand:     "process",
		InputMode:      "stdin",
		PositionalArgs: []string{"id"},
	}
	argsJSON := `{"id": "abc", "data": "hello"}`

	cmd := buildSidecarCommand(tool, argsJSON)

	if !contains(cmd, "'abc'") {
		t.Errorf("positional arg should appear quoted: %s", cmd)
	}
	if !contains(cmd, `"data"`) {
		t.Errorf("remaining args should be piped as JSON: %s", cmd)
	}
	if contains(cmd, `"id"`) {
		t.Errorf("positional arg should be stripped from stdin JSON: %s", cmd)
	}
}

func TestExecuteSidecarTool_Timeout(t *testing.T) {
	tool := sidecarToolEntry{
		Name:       "slow_tool",
		Target:     "test",
		Subcommand: "slow",
		InputMode:  "args",
		Timeout:    90,
	}

	// We can't fully test executeCommand without the IPC mechanism,
	// but we can verify the command is built correctly.
	cmd := buildSidecarCommand(tool, `{}`)
	if cmd != "node /app/dist/cli.js slow" {
		t.Errorf("unexpected command: %s", cmd)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Unused context for test compilation — tests reference it indirectly.
var _ = context.Background
