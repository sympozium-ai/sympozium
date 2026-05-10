package eventbus

import (
	"encoding/json"
	"testing"
)

func TestNewEvent_Success(t *testing.T) {
	metadata := map[string]string{"agent": "test-agent"}
	data := map[string]any{"task": "hello"}

	evt, err := NewEvent(TopicAgentRunRequested, metadata, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evt.Topic != TopicAgentRunRequested {
		t.Errorf("expected topic=%s, got %s", TopicAgentRunRequested, evt.Topic)
	}
	if evt.Data == nil {
		t.Error("expected Data to be non-empty")
	}
	if evt.Metadata["agent"] != "test-agent" {
		t.Errorf("expected metadata.agent=test-agent, got %q", evt.Metadata["agent"])
	}
	if evt.Ctx != nil {
		t.Error("expected Ctx to be nil (not set by NewEvent)")
	}
	if evt.Timestamp.IsZero() {
		t.Error("expected Timestamp to be set")
	}
}

func TestNewEvent_NilMetadata(t *testing.T) {
	evt, err := NewEvent(TopicAgentRunRequested, nil, "data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Metadata != nil {
		t.Errorf("expected nil metadata, got %v", evt.Metadata)
	}
}

func TestNewEvent_InvalidJSON(t *testing.T) {
	data := struct{ Ch chan int }{}
	_, err := NewEvent(TopicAgentRunRequested, nil, data)
	if err == nil {
		t.Error("expected error for unmarshalable data, got nil")
	}
}

func TestEvent_Encode(t *testing.T) {
	rawData := map[string]any{"task": "hello"}
	metadata := map[string]string{"key": "val"}

	evt, err := NewEvent(TopicAgentRunCompleted, metadata, rawData)
	if err != nil {
		t.Fatalf("unexpected NewEvent error: %v", err)
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}

	if len(data) == 0 {
		t.Error("expected non-empty JSON output")
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if parsed["topic"] != TopicAgentRunCompleted {
		t.Errorf("expected topic=%s, got %v", TopicAgentRunCompleted, parsed["topic"])
	}
	if parsed["metadata"] == nil {
		t.Error("expected metadata to be present")
	}

	parsedData, ok := parsed["data"].(map[string]any)
	if !ok {
		t.Error("expected data to be a JSON object")
	} else if parsedData["task"] != "hello" {
		t.Errorf("expected data.task=hello, got %v", parsedData["task"])
	}
}

func TestEvent_Decode(t *testing.T) {
	jsonData := `{"topic":"agent.run.requested","timestamp":"2025-01-01T00:00:00Z","metadata":{"agent":"test"},"data":{"task":"hello"}}`

	var evt Event
	if err := json.Unmarshal([]byte(jsonData), &evt); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if evt.Topic != TopicAgentRunRequested {
		t.Errorf("expected topic=%s, got %q", TopicAgentRunRequested, evt.Topic)
	}
	if evt.Metadata["agent"] != "test" {
		t.Errorf("expected metadata.agent=test, got %q", evt.Metadata["agent"])
	}
	if string(evt.Data) != `{"task":"hello"}` {
		t.Errorf("expected data to match, got %s", string(evt.Data))
	}
	if evt.Ctx != nil {
		t.Error("expected Ctx to be nil (json:\"-\" tag)")
	}
}

func TestEvent_RoundTrip(t *testing.T) {
	original, err := NewEvent(TopicAgentRunCompleted, map[string]string{"k": "v"}, map[string]any{"n": 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.Topic != original.Topic {
		t.Errorf("topic mismatch: %q vs %q", original.Topic, decoded.Topic)
	}
	if !decoded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("timestamp mismatch: %v vs %v", original.Timestamp, decoded.Timestamp)
	}
	if string(decoded.Data) != string(original.Data) {
		t.Errorf("data mismatch: %s vs %s", original.Data, decoded.Data)
	}
}

func TestEvent_CtxExcludedFromJSON(t *testing.T) {
	evt := Event{
		Topic: TopicAgentRunCompleted,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if _, ok := parsed["ctx"]; ok {
		t.Error("ctx field should not appear in JSON output")
	}
}
