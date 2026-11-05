package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sympozium-ai/sympozium/internal/tooloutput"
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

// writeReadFileFixture creates a file under /tmp — the only allowlisted
// read_file prefix that is writable on a dev machine (t.TempDir() lands in
// /var/folders on macOS, which the allowlist rejects).
func writeReadFileFixture(t *testing.T, content string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "readfiletool")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// tenLines returns "l1\n" through "l10\n".
func tenLines() string {
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "l%d\n", i)
	}
	return b.String()
}

func TestReadFileTool_Default(t *testing.T) {
	content := "hello\nworld\n"
	path := writeReadFileFixture(t, content)

	got := readFileTool(map[string]any{"path": path})
	if got != content {
		t.Fatalf("default read = %q, want %q", got, content)
	}
}

func TestReadFileTool_RangedReads(t *testing.T) {
	path := writeReadFileFixture(t, tenLines())

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"offset and limit", map[string]any{"path": path, "offset": float64(4), "limit": float64(3)},
			"l4\nl5\nl6\n... (showing lines 4-6 of 10; continue with offset=7)"},
		{"offset only reads to EOF", map[string]any{"path": path, "offset": float64(8)},
			"l8\nl9\nl10"},
		{"limit only", map[string]any{"path": path, "limit": float64(2)},
			"l1\nl2\n... (showing lines 1-2 of 10; continue with offset=3)"},
		{"offset past EOF", map[string]any{"path": path, "offset": float64(11)},
			"Error: offset 11 is past the end of the file (10 lines)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := readFileTool(c.args)
			if got != c.want {
				t.Fatalf("readFileTool(%v) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

func TestReadFileTool_CapTruncatesAtLineBoundary(t *testing.T) {
	t.Setenv(tooloutput.MaxBytesEnv, "")
	// 500 lines of 40 chars (41 bytes with newline) — well past the 8000-byte cap.
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "%04d%s\n", i, strings.Repeat("x", 36))
	}
	path := writeReadFileFixture(t, b.String())

	got := readFileTool(map[string]any{"path": path})
	hint := "\n... (truncated at line 195 of 500; continue with offset=196)"
	if !strings.HasSuffix(got, hint) {
		t.Fatalf("truncated read missing continuation hint %q, got tail %q", hint, got[len(got)-80:])
	}
	body := strings.TrimSuffix(got, hint)
	if len(body) > 8_000 {
		t.Fatalf("truncated body is %d bytes, want <= 8000", len(body))
	}
	if !strings.HasSuffix(body, "0195"+strings.Repeat("x", 36)) {
		t.Fatalf("body does not end at a complete line, tail = %q", body[len(body)-50:])
	}

	// Following the hint resumes exactly where the first read stopped.
	next := readFileTool(map[string]any{"path": path, "offset": float64(196)})
	if !strings.HasPrefix(next, "0196"+strings.Repeat("x", 36)) {
		t.Fatalf("continuation read starts with %q, want line 196", next[:40])
	}
}

func TestReadFileTool_SingleLineOverCap(t *testing.T) {
	t.Setenv(tooloutput.MaxBytesEnv, "")
	path := writeReadFileFixture(t, strings.Repeat("y", 9_000))

	got := readFileTool(map[string]any{"path": path})
	want := strings.Repeat("y", 8_000) + "\n... (truncated mid-line 1 of 1; line exceeds the 8000-byte cap, so line-based pagination cannot continue)"
	if got != want {
		t.Fatalf("over-cap single line read tail = %q, want mid-line truncation message", got[len(got)-80:])
	}
}

func TestReadFileTool_ConfiguredLimit(t *testing.T) {
	t.Setenv(tooloutput.MaxBytesEnv, "10")
	path := writeReadFileFixture(t, "first\nsecond\nthird\n")
	got := readFileTool(map[string]any{"path": path})
	want := "first\n... (truncated at line 1 of 3; continue with offset=2)"
	if got != want {
		t.Fatalf("readFileTool() = %q, want %q", got, want)
	}
}

func TestReadFileTool_ConfiguredLimitPreservesUTF8(t *testing.T) {
	t.Setenv(tooloutput.MaxBytesEnv, "4")
	path := writeReadFileFixture(t, "ab日本語\n")
	got := readFileTool(map[string]any{"path": path})
	if !utf8.ValidString(got) {
		t.Fatalf("readFileTool() returned invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "ab\n... (truncated mid-line 1 of 1; line exceeds the 4-byte cap") {
		t.Fatalf("readFileTool() = %q, want UTF-8-safe truncation hint", got)
	}
}

func TestFormatExecResult_ConfiguredLimit(t *testing.T) {
	t.Setenv(tooloutput.MaxBytesEnv, "5")
	got := formatExecResult(execResult{Stdout: "abcdefgh"})
	want := "abcde\n... (output truncated at 5 bytes; use command pagination or narrower output to retrieve the rest)"
	if got != want {
		t.Fatalf("formatExecResult() = %q, want %q", got, want)
	}
}

func TestReadFileTool_AccessDenied(t *testing.T) {
	got := readFileTool(map[string]any{"path": "/etc/passwd"})
	if !strings.Contains(got, "access denied") {
		t.Fatalf("read outside allowlist = %q, want access denied error", got)
	}
}

// outsideDir returns a temp dir guaranteed to be outside the tools' allowed
// roots. t.TempDir() cannot be used for this: on Linux it lands under /tmp,
// which is allowlisted. The package working directory (the repo checkout) is
// outside the roots on both CI and dev machines.
func outsideDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "outside-roots")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if underAnyPrefix(abs, resolvePrefixes(readableRoots)) {
		t.Skipf("working directory %s is inside the allowed roots; cannot test escapes", abs)
	}
	return abs
}

func TestReadFileTool_SymlinkEscapeDenied(t *testing.T) {
	outside := filepath.Join(outsideDir(t), "secret.txt")
	if err := os.WriteFile(outside, []byte("s3cret"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	link := filepath.Join(filepath.Dir(writeReadFileFixture(t, "unused")), "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	got := readFileTool(map[string]any{"path": link})
	if !strings.Contains(got, "resolves outside") {
		t.Fatalf("read through escaping symlink = %q, want denial", got)
	}
}

func TestWriteFileTool_PrefixBoundary(t *testing.T) {
	// "/tmpfoo" must not count as under "/tmp".
	got := writeFileTool(map[string]any{"path": "/tmpfoo/x.txt", "content": "x"})
	if !strings.Contains(got, "access denied") {
		t.Fatalf("write to /tmpfoo = %q, want access denied error", got)
	}
}

func TestWriteFileTool_SymlinkEscapeDenied(t *testing.T) {
	outside := outsideDir(t)
	link := filepath.Join(filepath.Dir(writeReadFileFixture(t, "unused")), "linkdir")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	got := writeFileTool(map[string]any{"path": filepath.Join(link, "f.txt"), "content": "x"})
	if !strings.Contains(got, "resolves outside") {
		t.Fatalf("write through escaping symlink = %q, want denial", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "f.txt")); !os.IsNotExist(err) {
		t.Fatalf("file was created outside the allowed roots (err=%v)", err)
	}
}

func TestWriteFileTool_CreatesNestedDirs(t *testing.T) {
	dir := filepath.Dir(writeReadFileFixture(t, "unused"))
	path := filepath.Join(dir, "a", "b", "f.txt")

	got := writeFileTool(map[string]any{"path": path, "content": "nested"})
	if !strings.Contains(got, "Successfully wrote") {
		t.Fatalf("nested write failed: %q", got)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "nested" {
		t.Fatalf("nested file = %q (err=%v), want %q", data, err, "nested")
	}
}

// TestReadFileToolDefAdvertisesRange asserts the read_file schema exposes the
// optional offset/limit parameters and that 'path' remains the only required
// field.
func TestReadFileToolDefAdvertisesRange(t *testing.T) {
	var def *ToolDef
	for i := range defaultTools() {
		td := defaultTools()[i]
		if td.Name == ToolReadFile {
			def = &td
			break
		}
	}
	if def == nil {
		t.Fatalf("read_file tool not found in defaultTools()")
	}
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing or wrong type in read_file schema")
	}
	for _, p := range []string{"offset", "limit"} {
		if _, ok := props[p]; !ok {
			t.Fatalf("read_file schema is missing the optional %q property: %v", p, props)
		}
	}
	required, _ := def.Parameters["required"].([]string)
	if len(required) != 1 || required[0] != "path" {
		t.Fatalf("read_file required = %v, want [path]", required)
	}
}
