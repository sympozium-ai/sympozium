package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	channelpkg "github.com/sympozium-ai/sympozium/internal/channel"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// recordingEventBus captures published events for assertions. Subscribe
// and Close are not exercised by these tests.
type recordingEventBus struct {
	published  []recordedEvent
	publishErr error
}

type recordedEvent struct {
	Topic string
	Event *eventbus.Event
}

func (r *recordingEventBus) Publish(_ context.Context, topic string, event *eventbus.Event) error {
	if r.publishErr != nil {
		return r.publishErr
	}
	r.published = append(r.published, recordedEvent{Topic: topic, Event: event})
	return nil
}
func (r *recordingEventBus) Subscribe(_ context.Context, _ string) (<-chan *eventbus.Event, error) {
	return nil, nil
}
func (r *recordingEventBus) Close() error { return nil }

func slackInstance(opts *sympoziumv1alpha1.SlackChannelOptions) *sympoziumv1alpha1.Agent {
	return &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Channels: []sympoziumv1alpha1.ChannelSpec{
				{Type: "telegram"},
				{Type: "slack", Slack: opts},
			},
		},
	}
}

func TestSlackChannelOptionsLookup(t *testing.T) {
	opts := &sympoziumv1alpha1.SlackChannelOptions{EmojiOnTrigger: "robot_face"}
	inst := slackInstance(opts)

	if got := slackChannelOptions(inst); got != opts {
		t.Errorf("present: want %p, got %p", opts, got)
	}
	if got := slackChannelOptions(nil); got != nil {
		t.Errorf("nil instance: want nil, got %+v", got)
	}
	missing := &sympoziumv1alpha1.Agent{
		Spec: sympoziumv1alpha1.AgentSpec{
			Channels: []sympoziumv1alpha1.ChannelSpec{{Type: "telegram"}},
		},
	}
	if got := slackChannelOptions(missing); got != nil {
		t.Errorf("no slack channel: want nil, got %+v", got)
	}
}

func TestSlackEmojiFor(t *testing.T) {
	tests := []struct {
		name     string
		opts     *sympoziumv1alpha1.SlackChannelOptions
		decision triggerDecision
		want     string
	}{
		{"nil opts -> trigger default", nil, triggerProcess, defaultSlackEmojiOnTrigger},
		{"nil opts -> stop default", nil, triggerStop, defaultSlackEmojiOnStop},
		{"nil opts -> resume default", nil, triggerResume, defaultSlackEmojiOnStart},
		{"nil opts -> drop has no emoji", nil, triggerDrop, ""},
		{"empty trigger uses default", &sympoziumv1alpha1.SlackChannelOptions{}, triggerProcess, defaultSlackEmojiOnTrigger},
		{
			"override trigger",
			&sympoziumv1alpha1.SlackChannelOptions{EmojiOnTrigger: "robot_face"},
			triggerProcess, "robot_face",
		},
		{
			"override stop",
			&sympoziumv1alpha1.SlackChannelOptions{EmojiOnStop: "no_entry"},
			triggerStop, "no_entry",
		},
		{
			"override resume",
			&sympoziumv1alpha1.SlackChannelOptions{EmojiOnStart: "white_check_mark"},
			triggerResume, "white_check_mark",
		},
		{
			"colons stripped from override",
			&sympoziumv1alpha1.SlackChannelOptions{EmojiOnTrigger: ":eyes:"},
			triggerProcess, "eyes",
		},
		{
			"whitespace-only treated as unset",
			&sympoziumv1alpha1.SlackChannelOptions{EmojiOnTrigger: "   "},
			triggerProcess, defaultSlackEmojiOnTrigger,
		},
		{
			"none disables trigger",
			&sympoziumv1alpha1.SlackChannelOptions{EmojiOnTrigger: "none"},
			triggerProcess, "",
		},
		{
			"NONE (case-insensitive) disables stop",
			&sympoziumv1alpha1.SlackChannelOptions{EmojiOnStop: "NONE"},
			triggerStop, "",
		},
		{
			"none disables resume",
			&sympoziumv1alpha1.SlackChannelOptions{EmojiOnStart: "  none  "},
			triggerResume, "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := slackEmojiFor(tc.opts, tc.decision)
			if got != tc.want {
				t.Errorf("slackEmojiFor(%v) = %q, want %q", tc.decision, got, tc.want)
			}
		})
	}
}

func TestSlackReaction(t *testing.T) {
	inst := slackInstance(&sympoziumv1alpha1.SlackChannelOptions{EmojiOnTrigger: "robot_face"})
	withTS := func(ts string) channelpkg.InboundMessage {
		return channelpkg.InboundMessage{
			Channel:  "slack",
			ChatID:   "C1",
			Metadata: map[string]string{"ts": ts},
		}
	}

	t.Run("returns OutboundMessage with ts and emoji", func(t *testing.T) {
		out := slackReaction(inst, withTS("123.456"), triggerProcess)
		if out == nil {
			t.Fatal("want non-nil")
		}
		if out.Channel != "slack" || out.ChatID != "C1" {
			t.Errorf("addressing: %+v", out)
		}
		if out.TargetMessageID != "123.456" {
			t.Errorf("TargetMessageID = %q", out.TargetMessageID)
		}
		if out.Reaction != "robot_face" {
			t.Errorf("Reaction = %q", out.Reaction)
		}
		if out.Text != "" {
			t.Errorf("Text should be empty for reactions, got %q", out.Text)
		}
	})

	t.Run("missing ts -> nil", func(t *testing.T) {
		msg := withTS("")
		if got := slackReaction(inst, msg, triggerProcess); got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})

	t.Run("nil metadata -> nil", func(t *testing.T) {
		msg := channelpkg.InboundMessage{Channel: "slack", ChatID: "C1"}
		if got := slackReaction(inst, msg, triggerProcess); got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})

	t.Run("disabled emoji -> nil", func(t *testing.T) {
		disabled := slackInstance(&sympoziumv1alpha1.SlackChannelOptions{EmojiOnTrigger: "none"})
		if got := slackReaction(disabled, withTS("1"), triggerProcess); got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})

	t.Run("drop decision -> nil", func(t *testing.T) {
		if got := slackReaction(inst, withTS("1"), triggerDrop); got != nil {
			t.Errorf("want nil, got %+v", got)
		}
	})
}

func TestReactionForDecision(t *testing.T) {
	inst := slackInstance(nil) // use defaults
	slackMsg := channelpkg.InboundMessage{
		Channel:  "slack",
		ChatID:   "C1",
		Metadata: map[string]string{"ts": "1.0"},
	}

	if got := reactionForDecision(inst, slackMsg, triggerProcess); got == nil || got.Reaction != defaultSlackEmojiOnTrigger {
		t.Errorf("slack process: want default emoji, got %+v", got)
	}

	telegramMsg := channelpkg.InboundMessage{Channel: "telegram", ChatID: "C1"}
	if got := reactionForDecision(inst, telegramMsg, triggerProcess); got != nil {
		t.Errorf("non-slack channel: want nil, got %+v", got)
	}

	discordMsg := channelpkg.InboundMessage{Channel: "discord", ChatID: "C1"}
	if got := reactionForDecision(inst, discordMsg, triggerStop); got != nil {
		t.Errorf("unsupported channel: want nil, got %+v", got)
	}
}

func TestEmitReaction_PublishesOutboundEvent(t *testing.T) {
	bus := &recordingEventBus{}
	cr := &ChannelRouter{EventBus: bus, Log: logr.Discard()}
	inst := slackInstance(&sympoziumv1alpha1.SlackChannelOptions{EmojiOnStop: "mute"})
	msg := channelpkg.InboundMessage{
		Channel:      "slack",
		InstanceName: "agent-x",
		ChatID:       "C42",
		Metadata:     map[string]string{"ts": "1700000000.000100"},
	}

	cr.emitReaction(context.Background(), inst, msg, triggerStop)

	if len(bus.published) != 1 {
		t.Fatalf("want 1 published event, got %d", len(bus.published))
	}
	pub := bus.published[0]
	if pub.Topic != eventbus.TopicChannelMessageSend {
		t.Errorf("topic = %q, want %q", pub.Topic, eventbus.TopicChannelMessageSend)
	}
	if pub.Event.Metadata["instanceName"] != "agent-x" || pub.Event.Metadata["channel"] != "slack" {
		t.Errorf("metadata = %+v", pub.Event.Metadata)
	}

	var out channelpkg.OutboundMessage
	if err := json.Unmarshal(pub.Event.Data, &out); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if out.Reaction != "mute" || out.TargetMessageID != "1700000000.000100" || out.ChatID != "C42" {
		t.Errorf("payload = %+v", out)
	}
}

func TestEmitReaction_NoOpWhenNoReaction(t *testing.T) {
	bus := &recordingEventBus{}
	cr := &ChannelRouter{EventBus: bus, Log: logr.Discard()}

	// Non-slack channel => no reaction => no publish.
	telegram := channelpkg.InboundMessage{Channel: "telegram", InstanceName: "a", ChatID: "C"}
	cr.emitReaction(context.Background(), slackInstance(nil), telegram, triggerProcess)

	// Slack but no ts metadata => no publish.
	noTS := channelpkg.InboundMessage{Channel: "slack", InstanceName: "a", ChatID: "C"}
	cr.emitReaction(context.Background(), slackInstance(nil), noTS, triggerProcess)

	// Slack with disabled emoji => no publish.
	disabled := slackInstance(&sympoziumv1alpha1.SlackChannelOptions{EmojiOnTrigger: "none"})
	withTS := channelpkg.InboundMessage{
		Channel: "slack", InstanceName: "a", ChatID: "C",
		Metadata: map[string]string{"ts": "1"},
	}
	cr.emitReaction(context.Background(), disabled, withTS, triggerProcess)

	if len(bus.published) != 0 {
		t.Errorf("want 0 published events, got %d: %+v", len(bus.published), bus.published)
	}
}
