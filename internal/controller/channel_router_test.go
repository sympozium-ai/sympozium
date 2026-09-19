package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	channel "github.com/sympozium-ai/sympozium/internal/channel"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
)

// TestHandleCompleted_Routing covers the skipped run staying silent (no channel
// reply) alongside the positive control that a normal result is routed back —
// the control guards the skipped assertion from passing vacuously.
func TestHandleCompleted_Routing(t *testing.T) {
	tests := []struct {
		name          string
		result        agentResult
		wantPublished int
	}{
		{
			name:          "skipped run stays silent",
			result:        agentResult{Status: ipc.ResultStatusSkipped, Response: "no new items in queue"},
			wantPublished: 0,
		},
		{
			name:          "success routes reply",
			result:        agentResult{Status: "success", Response: "here you go"},
			wantPublished: 1,
		},
		{
			// The runner's fatal() writes an error result and exits 1, so the
			// Job also fails and handleFailed replies with better advice.
			name:          "error result stays silent — handleFailed owns failure replies",
			result:        agentResult{Status: ipc.ResultStatusError, Error: "LLM completion failed: 429 rate limit"},
			wantPublished: 0,
		},
	}

	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := &sympoziumv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "chan-run",
					Namespace: "default",
					Labels:    map[string]string{"sympozium.ai/source": "channel"},
					Annotations: map[string]string{
						"sympozium.ai/reply-channel": "telegram",
						"sympozium.ai/reply-chat-id": "456",
					},
				},
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
			bus := &recordingEventBus{}
			cr := &ChannelRouter{Client: cl, EventBus: bus, Log: logr.Discard()}

			event, err := eventbus.NewEvent(eventbus.TopicAgentRunCompleted, map[string]string{
				"agentRunID":   "chan-run",
				"instanceName": "demo",
			}, tt.result)
			if err != nil {
				t.Fatalf("build event: %v", err)
			}

			cr.handleCompleted(context.Background(), event)

			if len(bus.published) != tt.wantPublished {
				t.Fatalf("published events = %d, want %d", len(bus.published), tt.wantPublished)
			}
			if tt.wantPublished == 1 && bus.published[0].Topic != eventbus.TopicChannelMessageSend {
				t.Fatalf("published topic = %q, want %q", bus.published[0].Topic, eventbus.TopicChannelMessageSend)
			}
		})
	}
}

// TestHandleFailed_Routing: a failed channel-sourced run gets a reply in the
// thread it came from — previously only successes were routed back, so a
// timed-out or crashed run produced total silence in Slack/Telegram. The
// classified reason bucket picks the advice; the raw error is quoted.
func TestHandleFailed_Routing(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	channelRun := func() *sympoziumv1alpha1.AgentRun {
		return &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "chan-run",
				Namespace: "default",
				Labels:    map[string]string{"sympozium.ai/source": "channel"},
				Annotations: map[string]string{
					"sympozium.ai/reply-channel":      "slack",
					"sympozium.ai/reply-chat-id":      "C123",
					"sympozium.ai/reply-thread-id":    "1700.1",
					"sympozium.ai/reply-message-ts":   "1700.1",
					"sympozium.ai/agent-display-name": "Support Bot",
				},
			},
		}
	}
	scheduleRun := func() *sympoziumv1alpha1.AgentRun {
		return &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Name: "chan-run", Namespace: "default"},
		}
	}

	tests := []struct {
		name          string
		run           *sympoziumv1alpha1.AgentRun
		bucket        string
		errText       string
		wantPublished int
		wantContains  string
	}{
		{
			name:          "timeout bucket gets runTimeout advice",
			run:           channelRun(),
			bucket:        "timeout",
			errText:       "agent run exceeded timeout of 10m0s",
			wantPublished: 1,
			wantContains:  "runTimeout",
		},
		{
			name:          "other bucket quotes the error",
			run:           channelRun(),
			bucket:        "llm_error",
			errText:       "LLM completion failed: 429 rate limit",
			wantPublished: 1,
			wantContains:  "LLM completion failed: 429 rate limit",
		},
		{
			name:          "non-channel run stays silent",
			run:           scheduleRun(),
			bucket:        "timeout",
			errText:       "agent run exceeded timeout of 10m0s",
			wantPublished: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.run).Build()
			bus := &recordingEventBus{}
			cr := &ChannelRouter{Client: cl, EventBus: bus, Log: logr.Discard()}

			event, err := eventbus.NewEvent(eventbus.TopicAgentRunFailed, map[string]string{
				"agentRunID":   "chan-run",
				"instanceName": "demo",
				"reason":       tt.bucket,
			}, map[string]string{"error": tt.errText})
			if err != nil {
				t.Fatalf("build event: %v", err)
			}

			cr.handleFailed(context.Background(), event)

			if len(bus.published) != tt.wantPublished {
				t.Fatalf("published events = %d, want %d", len(bus.published), tt.wantPublished)
			}
			if tt.wantPublished == 0 {
				return
			}
			got := bus.published[0]
			if got.Topic != eventbus.TopicChannelMessageSend {
				t.Fatalf("published topic = %q, want %q", got.Topic, eventbus.TopicChannelMessageSend)
			}
			if got.Event.Metadata["instanceName"] != "demo" || got.Event.Metadata["channel"] != "slack" {
				t.Fatalf("outbound metadata = %v, want instanceName=demo channel=slack", got.Event.Metadata)
			}
			var out channel.OutboundMessage
			if err := json.Unmarshal(got.Event.Data, &out); err != nil {
				t.Fatalf("decode outbound message: %v", err)
			}
			if out.Channel != "slack" || out.ChatID != "C123" || out.ThreadID != "1700.1" {
				t.Fatalf("reply addressed to %s/%s thread %q, want slack/C123 thread 1700.1", out.Channel, out.ChatID, out.ThreadID)
			}
			if out.Metadata["replyToTS"] != "1700.1" {
				t.Fatalf("replyToTS = %q, want 1700.1", out.Metadata["replyToTS"])
			}
			if out.Username != "Support Bot" {
				t.Fatalf("Username = %q, want the agent display name like a normal reply", out.Username)
			}
			if !strings.Contains(out.Text, tt.wantContains) {
				t.Fatalf("reply text %q does not mention %q", out.Text, tt.wantContains)
			}
		})
	}
}

// TestFailedRun_ExactlyOneReply pins the double-reply fix. When the runner
// fails internally it writes result.json with status "error" and exits 1: the
// IPC bridge publishes that result as agent.run.completed, then the Job fails
// and failRun publishes agent.run.failed. The channel must see one reply — the
// failure handler's, which carries the classified advice.
func TestFailedRun_ExactlyOneReply(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chan-run",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/source": "channel"},
			Annotations: map[string]string{
				"sympozium.ai/reply-channel": "slack",
				"sympozium.ai/reply-chat-id": "C123",
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	bus := &recordingEventBus{}
	cr := &ChannelRouter{Client: cl, EventBus: bus, Log: logr.Discard()}

	const errText = "LLM completion failed: 429 rate limit"
	completed, err := eventbus.NewEvent(eventbus.TopicAgentRunCompleted, map[string]string{
		"agentRunID": "chan-run", "instanceName": "demo",
	}, agentResult{Status: ipc.ResultStatusError, Error: errText})
	if err != nil {
		t.Fatalf("build completed event: %v", err)
	}
	failed, err := eventbus.NewEvent(eventbus.TopicAgentRunFailed, map[string]string{
		"agentRunID": "chan-run", "instanceName": "demo", "reason": "llm_error",
	}, map[string]string{"error": errText})
	if err != nil {
		t.Fatalf("build failed event: %v", err)
	}

	cr.handleCompleted(context.Background(), completed)
	cr.handleFailed(context.Background(), failed)

	if len(bus.published) != 1 {
		t.Fatalf("published %d replies for one failed run, want exactly 1 (from handleFailed)", len(bus.published))
	}
	var out channel.OutboundMessage
	if err := json.Unmarshal(bus.published[0].Event.Data, &out); err != nil {
		t.Fatalf("decode outbound message: %v", err)
	}
	if !strings.Contains(out.Text, "The agent run failed") || !strings.Contains(out.Text, errText) {
		t.Fatalf("the single reply should be handleFailed's, got %q", out.Text)
	}
}

func TestBuildFailureMessage(t *testing.T) {
	if got := buildFailureMessage("", ""); !strings.Contains(got, "unknown error") {
		t.Fatalf("empty bucket and detail: %q", got)
	}
	if got := buildFailureMessage("other", ""); !strings.Contains(got, "other") {
		t.Fatalf("bucket should stand in for a missing detail: %q", got)
	}
	if got := buildFailureMessage("policy", "tool kubectl denied by policy"); !strings.Contains(got, "denied by policy") {
		t.Fatalf("policy bucket should quote the detail: %q", got)
	}
}

func TestCheckChannelAccess(t *testing.T) {
	tests := []struct {
		name        string
		channels    []sympoziumv1alpha1.ChannelSpec
		msg         channel.InboundMessage
		wantAllowed bool
		wantDeny    string
	}{
		{
			name:        "no access control configured",
			channels:    []sympoziumv1alpha1.ChannelSpec{{Type: "telegram"}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name:        "channel type not in instance spec",
			channels:    []sympoziumv1alpha1.ChannelSpec{{Type: "slack"}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name: "allowed sender in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"123", "789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name: "allowed sender not in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"789", "012"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "denied sender in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					DeniedSenders: []string{"123"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "denied sender not in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					DeniedSenders: []string{"789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name: "sender in both allow and deny lists - deny wins",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"123"},
					DeniedSenders:  []string{"123"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "allowed chat in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats: []string{"456", "789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
		{
			name: "allowed chat not in list",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats: []string{"789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "allowed chat passes but denied sender blocks",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats:  []string{"456"},
					DeniedSenders: []string{"123"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
		},
		{
			name: "deny message returned when set",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"789"},
					DenyMessage:    "You are not authorized.",
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
			wantDeny:    "You are not authorized.",
		},
		{
			name: "deny message empty when not set",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: false,
			wantDeny:    "",
		},
		{
			name: "discord channel ID routing via AllowedChats - denied",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "discord",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats: []string{"1234567890123456789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "discord", SenderID: "user1", ChatID: "9999999999999999999"},
			wantAllowed: false,
		},
		{
			name: "discord channel ID routing via AllowedChats - allowed",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "discord",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedChats: []string{"1234567890123456789"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "discord", SenderID: "user1", ChatID: "1234567890123456789"},
			wantAllowed: true,
		},
		{
			name: "all checks pass",
			channels: []sympoziumv1alpha1.ChannelSpec{{
				Type: "telegram",
				AccessControl: &sympoziumv1alpha1.ChannelAccessControl{
					AllowedSenders: []string{"123"},
					AllowedChats:   []string{"456"},
					DeniedSenders:  []string{"999"},
				},
			}},
			msg:         channel.InboundMessage{Channel: "telegram", SenderID: "123", ChatID: "456"},
			wantAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := &sympoziumv1alpha1.Agent{
				Spec: sympoziumv1alpha1.AgentSpec{
					Channels: tt.channels,
				},
			}
			allowed, denyMsg := checkChannelAccess(inst, &tt.msg)
			if allowed != tt.wantAllowed {
				t.Errorf("checkChannelAccess() allowed = %v, want %v", allowed, tt.wantAllowed)
			}
			if denyMsg != tt.wantDeny {
				t.Errorf("checkChannelAccess() denyMsg = %q, want %q", denyMsg, tt.wantDeny)
			}
		})
	}
}
