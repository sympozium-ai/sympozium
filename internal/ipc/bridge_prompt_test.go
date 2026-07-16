package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
)

// TestHandlePromptRequest_PublishesAuditEvent verifies the bridge forwards
// a sidecar-issued prompt request to the per-run NATS topic so the
// control plane records it. The actual delivery to the agent-runner is
// filesystem-direct (the agent-runner shares /ipc/prompts with the
// sidecar) — this branch only emits the audit topic.
func TestHandlePromptRequest_PublishesAuditEvent(t *testing.T) {
	bus := &testEventBus{}
	bridge := NewBridge(t.TempDir(), "run-42", "agent-alpha", bus, logr.Discard(), "default")

	dir := filepath.Join(bridge.BasePath, DirPrompts)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	body := `{"requestId":"req-123","prompt":"score this candidate","appendContext":true}`
	path := filepath.Join(dir, "request-req-123.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write prompt request: %v", err)
	}

	bridge.handlePromptRequest(context.Background(), FileEvent{Path: path})

	if len(bus.published) != 1 {
		t.Fatalf("expected 1 event, got %d", len(bus.published))
	}
	wantTopic := "agent.prompt.request.run-42"
	if bus.published[0].topic != wantTopic {
		t.Fatalf("topic = %s, want %s", bus.published[0].topic, wantTopic)
	}
	var payload map[string]any
	if err := json.Unmarshal(bus.published[0].event.Data, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["requestId"] != "req-123" {
		t.Fatalf("requestId = %v", payload["requestId"])
	}
}

// TestHandlePromptRequest_DropsUnsafeRequestID ensures path-traversal
// attempts in the requestId are rejected before publication.
func TestHandlePromptRequest_DropsUnsafeRequestID(t *testing.T) {
	bus := &testEventBus{}
	bridge := NewBridge(t.TempDir(), "run-42", "agent-alpha", bus, logr.Discard(), "default")

	dir := filepath.Join(bridge.BasePath, DirPrompts)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	body := `{"requestId":"../../etc/passwd","prompt":"sneaky"}`
	path := filepath.Join(dir, "request-evil.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write prompt request: %v", err)
	}

	bridge.handlePromptRequest(context.Background(), FileEvent{Path: path})

	if len(bus.published) != 0 {
		t.Fatalf("expected unsafe request to be dropped, got %d events", len(bus.published))
	}
}

// TestHandleContextClear_PublishesAuditEvent verifies the bridge forwards
// a sidecar clear-context request. There is no result file — the agent
// runner just resets conversation state in-place.
func TestHandleContextClear_PublishesAuditEvent(t *testing.T) {
	bus := &testEventBus{}
	bridge := NewBridge(t.TempDir(), "run-42", "agent-alpha", bus, logr.Discard(), "default")

	dir := filepath.Join(bridge.BasePath, DirContext)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir context: %v", err)
	}
	body := `{"requestId":"clear-1","reason":"between services"}`
	path := filepath.Join(dir, "clear-clear-1.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write clear request: %v", err)
	}

	bridge.handleContextClear(context.Background(), FileEvent{Path: path})

	if len(bus.published) != 1 {
		t.Fatalf("expected 1 event, got %d", len(bus.published))
	}
	wantTopic := "agent.context.clear.run-42"
	if bus.published[0].topic != wantTopic {
		t.Fatalf("topic = %s, want %s", bus.published[0].topic, wantTopic)
	}
}

// TestHandlePromptResultAudit_PublishesPerRunTopic verifies the bridge
// republishes the agent-runner's filesystem-written prompt result back as
// a NATS audit event for the control plane.
func TestHandlePromptResultAudit_PublishesPerRunTopic(t *testing.T) {
	bus := &testEventBus{}
	bridge := NewBridge(t.TempDir(), "run-42", "agent-alpha", bus, logr.Discard(), "default")

	dir := filepath.Join(bridge.BasePath, DirPrompts)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	body := `{"requestId":"req-1","status":"success","content":"hello"}`
	path := filepath.Join(dir, "result-req-1.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write result: %v", err)
	}

	bridge.handlePromptResultAudit(context.Background(), FileEvent{Path: path})

	if len(bus.published) != 1 {
		t.Fatalf("expected 1 event, got %d", len(bus.published))
	}
	wantTopic := "agent.prompt.result.run-42"
	if bus.published[0].topic != wantTopic {
		t.Fatalf("topic = %s, want %s", bus.published[0].topic, wantTopic)
	}
}
