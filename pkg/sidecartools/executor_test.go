package sidecartools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

type executorResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	TimedOut bool   `json:"timedOut"`
}

type executorRun struct {
	env []string
	// entry is the script to start; empty runs ExecutorScript directly. The
	// executor's path is passed in SYMPOZIUM_TOOL_EXECUTOR so a wrapper can
	// exec it.
	entry    string
	requests map[string]map[string]any
	wait     time.Duration
}

// runExecutor starts the executor against a temporary IPC directory, writes
// the requests, and returns the results that appear before the deadline. A
// request with no result was never claimed.
func runExecutor(t *testing.T, run executorRun) map[string]executorResult {
	t.Helper()
	for _, bin := range []string{"bash", "jq", "timeout", "base64"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available, skipping tool-executor script test", bin)
		}
	}

	dir := t.TempDir()
	script := filepath.Join(dir, ExecutorScriptName)
	if err := os.WriteFile(script, []byte(ExecutorScript), 0o755); err != nil {
		t.Fatal(err)
	}
	ipc := filepath.Join(dir, "ipc")
	tools := filepath.Join(ipc, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	for id, req := range run.requests {
		data, _ := json.Marshal(req)
		if err := os.WriteFile(filepath.Join(tools, "exec-request-"+id+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entry := run.entry
	if entry == "" {
		entry = script
	}
	cmd := exec.Command("bash", entry)
	cmd.Env = append(os.Environ(), "SYMPOZIUM_IPC_DIR="+ipc, "SYMPOZIUM_TOOL_EXECUTOR="+script)
	cmd.Env = append(cmd.Env, run.env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(ipc, "done"), []byte("done"), 0o644)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Error("tool-executor did not exit after the done marker")
		}
	})

	results := map[string]executorResult{}
	deadline := time.Now().Add(run.wait)
	for time.Now().Before(deadline) && len(results) < len(run.requests) {
		for id := range run.requests {
			data, err := os.ReadFile(filepath.Join(tools, "exec-result-"+id+".json"))
			if err != nil {
				continue
			}
			var res executorResult
			if err := json.Unmarshal(data, &res); err != nil {
				t.Fatalf("result %s is not valid JSON: %v\n%s", id, err, data)
			}
			results[id] = res
		}
		time.Sleep(50 * time.Millisecond)
	}
	return results
}

// fakeTimeout puts a `timeout` stand-in first on PATH and returns the env entry.
func fakeTimeout(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "timeout"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return "PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func TestExecutorScript_ShellAndArgvModes(t *testing.T) {
	results := runExecutor(t, executorRun{
		requests: map[string]map[string]any{
			"shell": {"command": "printf 'a b' | tr a-z A-Z", "workDir": "/"},
			// One argv element containing a newline must stay one argument.
			"argv": {"argv": []string{"printf", "<%s>", "x\ny"}, "workDir": "/"},
		},
		wait: 10 * time.Second,
	})

	if got := results["shell"]; got.ExitCode != 0 || got.Stdout != "A B" {
		t.Errorf("shell mode result = %+v, want stdout %q", got, "A B")
	}
	if got := results["argv"]; got.ExitCode != 0 || got.Stdout != "<x\ny>" {
		t.Errorf("argv mode result = %+v, want stdout %q", got, "<x\ny>")
	}
}

// TOOL_EXECUTOR_MAX_TIMEOUT caps a request's own timeout; the llmfit image
// raises it to 180.
func TestExecutorScript_MaxTimeout(t *testing.T) {
	results := runExecutor(t, executorRun{
		env:      []string{"TOOL_EXECUTOR_MAX_TIMEOUT=1"},
		requests: map[string]map[string]any{"slow": {"command": "sleep 10", "timeout": 60, "workDir": "/"}},
		wait:     8 * time.Second,
	})

	got, ok := results["slow"]
	if !ok {
		t.Fatal("request was not cut off at TOOL_EXECUTOR_MAX_TIMEOUT")
	}
	if !got.TimedOut {
		t.Errorf("result = %+v, want timedOut", got)
	}
}

// TOOL_EXECUTOR_DEFAULT_TIMEOUT applies to a request that sets no timeout.
func TestExecutorScript_DefaultTimeout(t *testing.T) {
	results := runExecutor(t, executorRun{
		env:      []string{"TOOL_EXECUTOR_DEFAULT_TIMEOUT=1"},
		requests: map[string]map[string]any{"slow": {"command": "sleep 10", "workDir": "/"}},
		wait:     8 * time.Second,
	})

	if got, ok := results["slow"]; !ok || !got.TimedOut {
		t.Fatalf("result = %+v (present %v), want a timeout after TOOL_EXECUTOR_DEFAULT_TIMEOUT", got, ok)
	}
}

// busybox timeout, the one in the Alpine skill images, exits 143 (SIGTERM)
// rather than coreutils' 124 when it kills the command.
func TestExecutorScript_BusyboxTimeoutStatus(t *testing.T) {
	t.Run("143 after the limit is a timeout", func(t *testing.T) {
		results := runExecutor(t, executorRun{
			env:      []string{fakeTimeout(t, `sleep "$1"; exit 143`)},
			requests: map[string]map[string]any{"slow": {"command": "sleep 10", "timeout": 1, "workDir": "/"}},
			wait:     8 * time.Second,
		})
		if got := results["slow"]; got.ExitCode != 143 || !got.TimedOut {
			t.Errorf("result = %+v, want exit 143 reported as timedOut", got)
		}
	})

	t.Run("143 before the limit is not", func(t *testing.T) {
		results := runExecutor(t, executorRun{
			env:      []string{fakeTimeout(t, `exit 143`)},
			requests: map[string]map[string]any{"killed": {"command": "true", "timeout": 30, "workDir": "/"}},
			wait:     5 * time.Second,
		})
		if got := results["killed"]; got.ExitCode != 143 || got.TimedOut {
			t.Errorf("result = %+v, want exit 143 not reported as timedOut", got)
		}
	})
}

// A sidecar claims only requests addressed to its SkillPack, or to no one.
func TestExecutorScript_TargetRouting(t *testing.T) {
	results := runExecutor(t, executorRun{
		env: []string{"SYMPOZIUM_SKILL_PACK=k8s-ops"},
		requests: map[string]map[string]any{
			"mine":   {"command": "echo mine", "target": "K8s-Ops", "workDir": "/"},
			"anyone": {"command": "echo anyone", "workDir": "/"},
			"other":  {"command": "echo other", "target": "github-gitops", "workDir": "/"},
		},
		wait: 3 * time.Second,
	})

	for _, id := range []string{"mine", "anyone"} {
		if _, ok := results[id]; !ok {
			t.Errorf("request %q was not claimed", id)
		}
	}
	if _, ok := results["other"]; ok {
		t.Error("request for another sidecar's target was claimed")
	}
}

// The github-gitops image wraps the shared executor: what the wrapper exports
// (the fingerprint helper, its timeouts) must reach the commands the executor runs.
func TestGitopsWrapperRunsSharedExecutor(t *testing.T) {
	for _, bin := range []string{"sha256sum", "awk"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available, skipping github-gitops wrapper test", bin)
		}
	}
	results := runExecutor(t, executorRun{
		entry: filepath.Join("..", "..", "images", "skill-github-gitops", "tool-executor.sh"),
		requests: map[string]map[string]any{
			"fp":      {"command": "sympozium_fingerprint Deployment/web/crashloop", "workDir": "/"},
			"timeout": {"command": `echo "$TOOL_EXECUTOR_DEFAULT_TIMEOUT/$TOOL_EXECUTOR_MAX_TIMEOUT"`, "workDir": "/"},
		},
		wait: 10 * time.Second,
	})

	if got := results["fp"]; got.ExitCode != 0 || !regexp.MustCompile(`^[0-9a-z]{12}$`).MatchString(got.Stdout) {
		t.Errorf("sympozium_fingerprint result = %+v, want a 12-character base-36 id", got)
	}
	if got := results["timeout"]; got.Stdout != "60/300" {
		t.Errorf("wrapper timeouts = %q, want 60/300", got.Stdout)
	}
}
