package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeSidecarTarget(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain lowercase", "github-gitops", "github-gitops"},
		{"mixed case", "Github-Gitops", "github-gitops"},
		{"upper case", "GITHUB-GITOPS", "github-gitops"},
		{"surrounding whitespace", "  github-gitops\n", "github-gitops"},
		{"tab and newline", "\tgithub-gitops\n", "github-gitops"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeSidecarTarget(c.in)
			if got != c.want {
				t.Fatalf("normalizeSidecarTarget(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestExecRequestJSONIncludesTarget locks in the IPC protocol contract: when
// Target is set, the JSON payload written to /ipc/tools/exec-request-*.json
// MUST contain a top-level "target" field with the literal string value. The
// skill-sidecar tool-executor scripts depend on this field name.
func TestExecRequestJSONIncludesTarget(t *testing.T) {
	req := execRequest{
		ID:      "req-1",
		Command: "gh issue list",
		WorkDir: "/workspace",
		Timeout: 30,
		Target:  "github-gitops",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := generic["target"].(string)
	if !ok {
		t.Fatalf("target field missing or not a string in JSON: %s", string(data))
	}
	if got != "github-gitops" {
		t.Fatalf("target = %q, want %q", got, "github-gitops")
	}
}

// TestExecRequestJSONOmitsEmptyTarget verifies the legacy compatibility path:
// when Target is empty, the JSON payload MUST NOT contain a "target" key. Old
// (unmigrated) sidecar images do not understand the field; emitting an empty
// string would still cause `jq -r '.target // ""'` to behave correctly, but
// the omitempty tag preserves byte-level compatibility with the pre-fix
// protocol so existing parsers / fixtures see no diff.
func TestExecRequestJSONOmitsEmptyTarget(t *testing.T) {
	req := execRequest{
		ID:      "req-2",
		Command: "kubectl get pods",
		WorkDir: "/workspace",
		Timeout: 30,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"target"`) {
		t.Fatalf("expected no target key in JSON when empty, got: %s", string(data))
	}
}

// TestExecuteCommandToolDefAdvertisesTarget asserts the tool schema exposed to
// the LLM continues to advertise an optional `target` parameter and that
// `command` remains the only required field. This guards against accidental
// schema regressions that would either drop target routing or break callers
// that omit target.
func TestExecuteCommandToolDefAdvertisesTarget(t *testing.T) {
	var def *ToolDef
	for i := range defaultTools() {
		td := defaultTools()[i]
		if td.Name == ToolExecuteCommand {
			def = &td
			break
		}
	}
	if def == nil {
		t.Fatalf("execute_command tool not found in defaultTools()")
	}
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing or wrong type in execute_command schema")
	}
	if _, ok := props["target"]; !ok {
		t.Fatalf("execute_command schema is missing the optional 'target' property: %v", props)
	}
	required, _ := def.Parameters["required"].([]string)
	for _, r := range required {
		if r == "target" {
			t.Fatalf("'target' must be optional, but appears in required: %v", required)
		}
	}
}
