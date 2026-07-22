package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── prompt-server result handling tests ───────────────────────────────────────
//
// loadAndEmitRunResult is the part of runPromptServer that reads the
// sidecar's run-result.json payload and writes it to /ipc/output/. These
// tests cover the happy path, the missing-file fallback, and the
// invalid-JSON fallback. runPromptServer's done-watching loop and prompt
// service integration are exercised in benchmarks and integration tests
// against the real /ipc volume; the unit tests pin the file-handling
// contract that any sidecar failure mode eventually flows through.

const resultMarkerPrefix = "__SYMPOZIUM_RESULT__"
const resultMarkerSuffix = "__SYMPOZIUM_END__"

func TestLoadAndEmitRunResult_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	resultPath := filepath.Join(tmp, "run-result.json")
	outputPath := filepath.Join(tmp, "output", "result.json")

	want := agentResult{
		Status:   "success",
		Response: "sidecar finished",
	}
	want.Metrics.DurationMs = 1234
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now().Add(-2 * time.Second)
	got := loadAndEmitRunResult(resultPath, outputPath, start)
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.Response != want.Response {
		t.Errorf("Response = %q, want %q", got.Response, want.Response)
	}
	if got.Metrics.DurationMs <= 0 {
		t.Errorf("DurationMs not populated: %d", got.Metrics.DurationMs)
	}

	out, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("output path not written: %v", err)
	}
	var roundTrip agentResult
	if err := json.Unmarshal(out, &roundTrip); err != nil {
		t.Fatalf("output path not valid JSON: %v", err)
	}
	if roundTrip.Response != want.Response {
		t.Errorf("output Response = %q, want %q", roundTrip.Response, want.Response)
	}
}

func TestLoadAndEmitRunResult_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	resultPath := filepath.Join(tmp, "run-result.json")
	outputPath := filepath.Join(tmp, "output", "result.json")

	start := time.Now()
	got := loadAndEmitRunResult(resultPath, outputPath, start)
	if got.Status != "success" {
		t.Errorf("Status = %q, want %q (fallback should still be success)", got.Status, "success")
	}
	if !strings.Contains(got.Response, "did not write") {
		t.Errorf("Response = %q, want a fallback message indicating missing run-result.json", got.Response)
	}
}

func TestLoadAndEmitRunResult_InvalidJSONWrapped(t *testing.T) {
	tmp := t.TempDir()
	resultPath := filepath.Join(tmp, "run-result.json")
	outputPath := filepath.Join(tmp, "output", "result.json")

	if err := os.WriteFile(resultPath, []byte("not valid json {"), 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	got := loadAndEmitRunResult(resultPath, outputPath, start)
	if got.Status != "success" {
		t.Errorf("Status = %q, want %q (invalid payload should be wrapped, not error)", got.Status, "success")
	}
	if !strings.Contains(got.Response, "not valid json") {
		t.Errorf("Response = %q, want wrapped raw input", got.Response)
	}
}

// TestContextWithSignal_CancelledOnParentDone verifies the helper honours
// parent cancellation (used to make runPromptServer testable without
// actually sending signals).
func TestContextWithSignal_CancelledOnParentDone(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, _ := contextWithSignal(parent)

	// Cancel the parent; the derived ctx should be Done within a short window.
	cancel()
	select {
	case <-ctx.Done():
		// good
	case <-time.After(time.Second):
		t.Fatal("context not cancelled after parent cancellation")
	}
}

// TestWriteJSON verifies the small helper that all the result-writing
// call sites share, to catch any future regression that produces a
// partial write (truncated JSON, missing close brace) which would
// corrupt the sidecar's handoff.
func TestWriteJSON_AtomicAndParseable(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "result.json")
	want := agentResult{Status: "success", Response: "ok"}
	writeJSON(path, want)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("writeJSON produced invalid JSON: %s", data)
	}
	var got agentResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("writeJSON output did not round-trip: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}
