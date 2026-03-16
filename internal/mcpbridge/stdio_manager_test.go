package mcpbridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestStdioManager_StartStop(t *testing.T) {
	m := NewStdioManager("cat", nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !m.IsAlive() {
		t.Fatal("expected alive after start")
	}
	m.Stop()
	if m.IsAlive() {
		t.Fatal("expected not alive after stop")
	}
}

func TestStdioManager_SendReceive(t *testing.T) {
	// cat echoes stdin to stdout line by line
	m := NewStdioManager("cat", nil, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer m.Stop()

	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "test",
	}
	reqBytes, _ := json.Marshal(req)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := m.Send(ctx, reqBytes)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// cat should echo back the same JSON
	var got map[string]interface{}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("response is not valid JSON: %v (got: %s)", err, string(resp))
	}

	if got["method"] != "test" {
		t.Errorf("expected method=test, got %v", got["method"])
	}
}

func TestStdioManager_SendWhenDead(t *testing.T) {
	m := NewStdioManager("cat", nil, nil)
	// Don't start — should fail
	ctx := context.Background()
	_, err := m.Send(ctx, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error when sending to dead process")
	}
}
