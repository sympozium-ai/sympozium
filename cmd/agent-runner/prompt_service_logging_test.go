package main

import (
	"bytes"
	"encoding/json"
	"log"
	"strings"
	"testing"
)

// ── prompt-service LLM logging tests ─────────────────────────────────────────
//
// These tests pin the "prompt service req:" / "prompt service res:" log
// lines emitted by runPromptServiceLoop. The agent-runner's kubectl-log
// triage story (VEL-1203) depends on these lines actually appearing next to
// the existing metadata line, and being byte-capped so a runaway multi-turn
// sidecar doesn't fill pod logs with megabytes per request.

func withLogCapture(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	buf := &bytes.Buffer{}
	oldOut := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0) // strip timestamps so substring matching is stable
	return buf, func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	}
}

func withLogMaxBytes(t *testing.T, n int) func() {
	t.Helper()
	old := promptServiceLogMaxBytes
	promptServiceLogMaxBytes = n
	return func() { promptServiceLogMaxBytes = old }
}

// TestLogPromptServiceReq_EmitsFullPromptWhenUnderCap covers the happy
// path: an in-cap prompt produces a "req:" line containing the full
// prompt text, the byte length, and the schema byte length.
func TestLogPromptServiceReq_EmitsFullPromptWhenUnderCap(t *testing.T) {
	buf, restore := withLogCapture(t)
	defer restore()
	defer withLogMaxBytes(t, 2048)()

	req := ipcPromptRequest{
		RequestID: "req-1",
		Prompt:    "Summarise the AI capabilities of chatgpt.com.",
		Schema:    json.RawMessage(`{"type":"object"}`),
	}
	logPromptServiceReq(req)

	out := buf.String()
	if !strings.Contains(out, "prompt service req:") {
		t.Fatalf("missing req: header in %q", out)
	}
	if !strings.Contains(out, "requestId=req-1") {
		t.Fatalf("missing requestId in %q", out)
	}
	if !strings.Contains(out, "promptBytes=45") {
		t.Fatalf("missing/exact promptBytes in %q", out)
	}
	if !strings.Contains(out, "schemaBytes=17") {
		t.Fatalf("missing/exact schemaBytes in %q", out)
	}
	if !strings.Contains(out, "Summarise the AI capabilities of chatgpt.com.") {
		t.Fatalf("prompt body not in log line: %q", out)
	}
	if strings.Contains(out, "truncated") {
		t.Fatalf("unexpected truncation marker in %q", out)
	}
}

// TestLogPromptServiceReq_TruncatesOverCap verifies the cap-based
// truncation works: the prompt is shortened to promptServiceLogMaxBytes
// and a "truncated N bytes" marker is appended so operators know how
// much was elided.
func TestLogPromptServiceReq_TruncatesOverCap(t *testing.T) {
	buf, restore := withLogCapture(t)
	defer restore()
	defer withLogMaxBytes(t, 8)()

	big := strings.Repeat("a", 64)
	req := ipcPromptRequest{RequestID: "req-big", Prompt: big}
	logPromptServiceReq(req)

	out := buf.String()
	if !strings.Contains(out, "promptBytes=64") {
		t.Fatalf("expected full promptBytes=64 in %q", out)
	}
	if !strings.Contains(out, "truncated 56 bytes") {
		t.Fatalf("expected truncation marker for 64-8=56 bytes in %q", out)
	}
	// Emitted body should be at most the cap + marker suffix length, not
	// the full 64 chars.
	body := strings.TrimPrefix(strings.TrimSpace(out), "prompt service req: requestId=req-big promptBytes=64 schemaBytes=0 prompt=")
	if len(body) >= len(big) {
		t.Fatalf("expected body to be truncated, got %d bytes of body in %q", len(body), out)
	}
}

// TestLogPromptServiceReq_Disabled confirms a 0 cap suppresses the line
// entirely (so clusters can opt out via PROMPT_SERVICE_LOG_MAX_BYTES=0
// without paying the per-turn allocation cost).
func TestLogPromptServiceReq_Disabled(t *testing.T) {
	buf, restore := withLogCapture(t)
	defer restore()
	defer withLogMaxBytes(t, 0)()

	logPromptServiceReq(ipcPromptRequest{RequestID: "x", Prompt: "anything"})

	if got := buf.String(); got != "" {
		t.Fatalf("expected no output, got %q", got)
	}
}

// TestLogPromptServiceRes_PrefersParsedJSON confirms that when the
// LLMResponse included a structured JSON parse, we log the parsed JSON
// verbatim — this is the "actual LLM decision" operators care about per
// VEL-1203.
func TestLogPromptServiceRes_PrefersParsedJSON(t *testing.T) {
	buf, restore := withLogCapture(t)
	defer restore()
	defer withLogMaxBytes(t, 2048)()

	res := ipcPromptResult{
		RequestID: "res-1",
		Status:    "success",
		Content:   `{"action":"save","url":"https://chatgpt.com"}`,
		Parsed:    json.RawMessage(`{"action":"save","url":"https://chatgpt.com","confidence":0.92}`),
	}
	logPromptServiceRes(res)

	out := buf.String()
	if !strings.Contains(out, "prompt service res:") {
		t.Fatalf("missing res: header in %q", out)
	}
	if !strings.Contains(out, `payloadKind=parsed`) {
		t.Fatalf("missing payloadKind=parsed in %q", out)
	}
	if !strings.Contains(out, `"confidence":0.92`) {
		t.Fatalf("parsed JSON not emitted verbatim (expected confidence key) in %q", out)
	}
	if strings.Contains(out, "prompt service req:") {
		t.Fatalf("res helper should not emit req lines; got %q", out)
	}
}

// TestLogPromptServiceRes_ContentFallback covers the path where the LLM
// answered but did not return parseable JSON — the log line falls back
// to the textual Content.
func TestLogPromptServiceRes_ContentFallback(t *testing.T) {
	buf, restore := withLogCapture(t)
	defer restore()
	defer withLogMaxBytes(t, 2048)()

	res := ipcPromptResult{
		RequestID: "res-2",
		Status:    "success",
		Content:   "the LLM said hello",
	}
	logPromptServiceRes(res)

	out := buf.String()
	if !strings.Contains(out, `payloadKind=content`) {
		t.Fatalf("missing payloadKind=content fallback in %q", out)
	}
	if !strings.Contains(out, "the LLM said hello") {
		t.Fatalf("content body not in log line: %q", out)
	}
}

// TestLogPromptServiceRes_ErrorPath makes sure error responses get
// captured even when no payload content is present.
func TestLogPromptServiceRes_ErrorPath(t *testing.T) {
	buf, restore := withLogCapture(t)
	defer restore()
	defer withLogMaxBytes(t, 2048)()

	res := ipcPromptResult{
		RequestID: "res-3",
		Status:    "error",
		Error:     "rate limit exceeded",
	}
	logPromptServiceRes(res)

	out := buf.String()
	if !strings.Contains(out, `status=error`) {
		t.Fatalf("missing status=error in %q", out)
	}
	if !strings.Contains(out, `payloadKind=error`) {
		t.Fatalf("missing payloadKind=error in %q", out)
	}
	if !strings.Contains(out, "rate limit exceeded") {
		t.Fatalf("error text not in log line: %q", out)
	}
}

// TestLogPromptServiceRes_TruncatesOverCap confirms the same cap
// truncation applies on the response side.
func TestLogPromptServiceRes_TruncatesOverCap(t *testing.T) {
	buf, restore := withLogCapture(t)
	defer restore()
	defer withLogMaxBytes(t, 16)()

	big := strings.Repeat("z", 200)
	res := ipcPromptResult{
		RequestID: "res-big",
		Status:    "success",
		Content:   big,
	}
	logPromptServiceRes(res)

	out := buf.String()
	if !strings.Contains(out, "payloadBytes=200") {
		t.Fatalf("expected full payloadBytes=200 in %q", out)
	}
	if !strings.Contains(out, "truncated 184 bytes") {
		t.Fatalf("expected truncation marker for 200-16=184 bytes in %q", out)
	}
}

// TestLogPromptServiceRes_Disabled confirms the env-var knob hides
// the response log too.
func TestLogPromptServiceRes_Disabled(t *testing.T) {
	buf, restore := withLogCapture(t)
	defer restore()
	defer withLogMaxBytes(t, 0)()

	logPromptServiceRes(ipcPromptResult{RequestID: "y", Status: "success", Content: "hi"})

	if got := buf.String(); got != "" {
		t.Fatalf("expected no output, got %q", got)
	}
}
