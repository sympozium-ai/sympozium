package channel

import (
	"context"
	"testing"
	"time"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// stubBus hands Subscribe a caller-controlled channel so the test can drive
// exactly which outbound events arrive and when the upstream closes.
type stubBus struct {
	events chan *eventbus.Event
}

func (s *stubBus) Publish(context.Context, string, *eventbus.Event) error { return nil }
func (s *stubBus) Subscribe(context.Context, string) (<-chan *eventbus.Event, error) {
	return s.events, nil
}
func (s *stubBus) Close() error { return nil }

func outboundFor(instance string) *eventbus.Event {
	return &eventbus.Event{
		Topic:     eventbus.TopicChannelMessageSend,
		Timestamp: time.Now(),
		Metadata:  map[string]string{"instanceName": instance, "channel": "slack"},
	}
}

// Every channel pod subscribes to the single channel.message.send topic, so
// without filtering each pod receives — and delivers — every agent's replies.
func TestSubscribeOutboundDeliversOnlyOwnInstance(t *testing.T) {
	source := make(chan *eventbus.Event, 8)
	bc := &BaseChannel{ChannelType: "slack", InstanceName: "support-bot", EventBus: &stubBus{events: source}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	filtered, err := bc.SubscribeOutbound(ctx)
	if err != nil {
		t.Fatal(err)
	}

	source <- outboundFor("other-bot")
	source <- outboundFor("support-bot")
	source <- outboundFor("")
	source <- outboundFor("support-bot")
	close(source)

	var got []string
	for event := range filtered {
		got = append(got, event.Metadata["instanceName"])
	}
	if len(got) != 2 {
		t.Fatalf("delivered %d events %v, want exactly the 2 addressed to support-bot", len(got), got)
	}
	for _, instance := range got {
		if instance != "support-bot" {
			t.Fatalf("delivered event for %q to support-bot", instance)
		}
	}
}

func TestSubscribeOutboundClosesWhenContextEnds(t *testing.T) {
	source := make(chan *eventbus.Event) // never written, never closed
	bc := &BaseChannel{ChannelType: "slack", InstanceName: "support-bot", EventBus: &stubBus{events: source}}

	ctx, cancel := context.WithCancel(context.Background())
	filtered, err := bc.SubscribeOutbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	select {
	case _, ok := <-filtered:
		if ok {
			t.Fatal("received an event after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("filtered channel did not close after context cancellation")
	}
}
