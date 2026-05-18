package controller

import (
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	channel "github.com/sympozium-ai/sympozium/internal/channel"
)

func TestChannelRunAnnotations(t *testing.T) {
	tests := []struct {
		name string
		msg  channel.InboundMessage
		want map[string]string
	}{
		{
			name: "threaded message stamps reply-thread-id",
			msg: channel.InboundMessage{
				Channel:    "slack",
				ChatID:     "C123",
				ThreadID:   "1779146795.121549",
				SenderID:   "U456",
				SenderName: "alice",
			},
			want: map[string]string{
				"sympozium.ai/reply-channel":   "slack",
				"sympozium.ai/reply-chat-id":   "C123",
				"sympozium.ai/reply-thread-id": "1779146795.121549",
				"sympozium.ai/sender-id":       "U456",
				"sympozium.ai/sender-name":     "alice",
			},
		},
		{
			name: "non-threaded message omits reply-thread-id",
			msg: channel.InboundMessage{
				Channel:    "telegram",
				ChatID:     "789",
				SenderID:   "42",
				SenderName: "bob",
			},
			want: map[string]string{
				"sympozium.ai/reply-channel": "telegram",
				"sympozium.ai/reply-chat-id": "789",
				"sympozium.ai/sender-id":     "42",
				"sympozium.ai/sender-name":   "bob",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := channelRunAnnotations(&tt.msg)
			if len(got) != len(tt.want) {
				t.Errorf("got %d annotations, want %d:\n  got=%v\n  want=%v", len(got), len(tt.want), got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("annotation %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestOutboundFromRunAnnotations(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		text         string
		wantOK       bool
		wantChannel  string
		wantChatID   string
		wantThreadID string
		wantText     string
	}{
		{
			name: "threaded reply propagates ThreadID",
			annotations: map[string]string{
				"sympozium.ai/reply-channel":   "slack",
				"sympozium.ai/reply-chat-id":   "C123",
				"sympozium.ai/reply-thread-id": "1779146795.121549",
			},
			text:         "hello",
			wantOK:       true,
			wantChannel:  "slack",
			wantChatID:   "C123",
			wantThreadID: "1779146795.121549",
			wantText:     "hello",
		},
		{
			name: "non-threaded reply has empty ThreadID",
			annotations: map[string]string{
				"sympozium.ai/reply-channel": "telegram",
				"sympozium.ai/reply-chat-id": "789",
			},
			text:        "hi",
			wantOK:      true,
			wantChannel: "telegram",
			wantChatID:  "789",
			wantText:    "hi",
		},
		{
			name:        "missing reply-channel signals not-channel-sourced",
			annotations: map[string]string{},
			text:        "anything",
			wantOK:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, ok := outboundFromRunAnnotations(tt.annotations, tt.text)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if out.Channel != tt.wantChannel {
				t.Errorf("Channel = %q, want %q", out.Channel, tt.wantChannel)
			}
			if out.ChatID != tt.wantChatID {
				t.Errorf("ChatID = %q, want %q", out.ChatID, tt.wantChatID)
			}
			if out.ThreadID != tt.wantThreadID {
				t.Errorf("ThreadID = %q, want %q", out.ThreadID, tt.wantThreadID)
			}
			if out.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", out.Text, tt.wantText)
			}
		})
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
