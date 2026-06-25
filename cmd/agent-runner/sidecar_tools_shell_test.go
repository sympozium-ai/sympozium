package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The exact argv-decode pipeline used by every images/skill-*/tool-executor.sh
// argv branch. Kept in lock-step with those scripts: if you change the decode
// loop there, change it here too. It prints one line per decoded argv element,
// wrapped in <…> so the test can assert exact boundaries.
const executorArgvDecodeSnippet = `
set -euo pipefail
cmd_argv=()
while IFS= read -r _b64; do cmd_argv+=("$(printf '%s' "$_b64" | base64 -d)"); done < <(jq -r '.argv[] | @base64' "$1")
for a in "${cmd_argv[@]}"; do printf '<%s>\n' "$a"; done
`

// TestExecutorArgvBranch_NewlineSafe is the cross-language regression guard for
// the highest-value security property: an argv element containing a newline must
// reach the wrapped binary as exactly ONE argument. It feeds the bash decode
// snippet the same request buildSidecarExecRequest produces.
func TestExecutorArgvBranch_NewlineSafe(t *testing.T) {
	for _, bin := range []string{"bash", "jq", "base64"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available, skipping shell-level argv test", bin)
		}
	}

	tool := sidecarToolEntry{
		Name:           "t",
		Target:         "svc",
		Exec:           []string{"the-binary"},
		InputMode:      "args",
		PositionalArgs: []string{"value"},
	}
	// A value an LLM can trivially produce: a newline followed by what looks
	// like a flag. The old `jq -r '.argv[]'` reader would split this into two
	// arguments and inject `--kubeconfig=...`.
	req, err := buildSidecarExecRequest(context.Background(), tool, `{"value":"web\n--kubeconfig=/evil/admin.conf"}`)
	if err != nil {
		t.Fatalf("buildSidecarExecRequest: %v", err)
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal req: %v", err)
	}

	reqPath := filepath.Join(t.TempDir(), "exec-request.json")
	if err := os.WriteFile(reqPath, data, 0o644); err != nil {
		t.Fatalf("write request: %v", err)
	}

	out, err := exec.Command("bash", "-c", executorArgvDecodeSnippet, "bash", reqPath).Output()
	if err != nil {
		t.Fatalf("running executor argv snippet: %v", err)
	}

	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	// the-binary, --, web\n--kubeconfig=... (one multi-line element)
	want := []string{
		"<the-binary>",
		"<-->",
		"<web",
		"--kubeconfig=/evil/admin.conf>",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("decoded argv boundaries wrong.\n got: %q\nwant: %q", got, want)
	}

	// The newline value must NOT have produced a standalone --kubeconfig argv
	// element (which would be the case if it were split on the newline).
	for _, line := range got {
		if line == "<--kubeconfig=/evil/admin.conf>" {
			t.Fatal("newline value was split into a separate --kubeconfig argument (flag injection)")
		}
	}
}
