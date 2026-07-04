package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

type testEventBus struct {
	published []testPublishedEvent
}

type testPublishedEvent struct {
	topic string
	event *eventbus.Event
}

func (b *testEventBus) Publish(_ context.Context, topic string, event *eventbus.Event) error {
	b.published = append(b.published, testPublishedEvent{topic: topic, event: event})
	return nil
}

func (b *testEventBus) Subscribe(_ context.Context, _ string) (<-chan *eventbus.Event, error) {
	return nil, nil
}

func (b *testEventBus) Close() error { return nil }

func TestHandleOutboundMessage_StripsSpoofedAttribution(t *testing.T) {
	bus := &testEventBus{}
	bridge := NewBridge(t.TempDir(), "run-1", "agent-alpha", bus, logr.Discard(), "default")
	bridge.AgentDisplayName = "Agent Alpha"
	path := writeOutboundMessage(t, bridge.BasePath, `{
		"channel":"slack",
		"chatId":"C123",
		"text":"hi",
		"username":"Alex Jones (CEO)",
		"iconUrl":"https://example.com/ceo.png",
		"iconEmoji":":ceo:"
	}`)

	bridge.handleOutboundMessage(context.Background(), FileEvent{Path: path})

	payload := publishedPayload(t, bus)
	for _, key := range []string{"username", "iconUrl", "iconEmoji"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("%s present in sanitized payload: %v", key, payload[key])
		}
	}
	if payload["channel"] != "slack" || payload["chatId"] != "C123" || payload["text"] != "hi" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestHandleOutboundMessage_AllowsConfiguredUsernameButStripsIcons(t *testing.T) {
	bus := &testEventBus{}
	bridge := NewBridge(t.TempDir(), "run-1", "agent-alpha", bus, logr.Discard(), "default")
	bridge.AgentDisplayName = "Agent Alpha"
	path := writeOutboundMessage(t, bridge.BasePath, `{
		"channel":"slack",
		"chatId":"C123",
		"text":"hi",
		"username":"Agent Alpha",
		"iconEmoji":":robot_face:"
	}`)

	bridge.handleOutboundMessage(context.Background(), FileEvent{Path: path})

	payload := publishedPayload(t, bus)
	if payload["username"] != "Agent Alpha" {
		t.Fatalf("username = %v", payload["username"])
	}
	for _, key := range []string{"iconUrl", "iconEmoji"} {
		if _, ok := payload[key]; ok {
			t.Fatalf("%s present in sanitized payload: %v", key, payload[key])
		}
	}
}

func writeOutboundMessage(t *testing.T, basePath, body string) string {
	t.Helper()
	dir := filepath.Join(basePath, DirMessages)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir messages dir: %v", err)
	}
	path := filepath.Join(dir, "send-1.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write outbound message: %v", err)
	}
	return path
}

func publishedPayload(t *testing.T, bus *testEventBus) map[string]any {
	t.Helper()
	if len(bus.published) != 1 {
		t.Fatalf("published events = %d", len(bus.published))
	}
	if bus.published[0].topic != eventbus.TopicChannelMessageSend {
		t.Fatalf("topic = %s", bus.published[0].topic)
	}
	var payload map[string]any
	if err := json.Unmarshal(bus.published[0].event.Data, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}
