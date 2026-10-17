package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
)

// A parked attempt's result must not tear the bridge down: the runner is still
// alive holding its conversation, and an exiting bridge would take the pod's
// only channel to the control plane with it.
func TestHandleOutputFile_AttemptResultDoesNotEndTheRun(t *testing.T) {
	base := t.TempDir()
	outputDir := filepath.Join(base, DirOutput)
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		t.Fatalf("mkdir output: %v", err)
	}
	bus := &testEventBus{}
	bridge := NewBridge(base, "run-9", "agent-alpha", bus, logr.Discard(), "default")

	path := filepath.Join(outputDir, "result-1.json")
	body, _ := json.Marshal(AttemptResult{Attempt: 1, Status: "success", Response: "draft"})
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write attempt result: %v", err)
	}

	bridge.handleOutputFile(context.Background(), FileEvent{Path: path})

	select {
	case <-bridge.agentDone:
		t.Fatal("an attempt result signalled agent completion; the bridge would exit mid-chain")
	default:
	}
	if len(bus.published) != 0 {
		t.Errorf("published %d event(s) for a parked attempt, want 0", len(bus.published))
	}
}
