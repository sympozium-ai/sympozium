package controller

import (
	"strings"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	channelpkg "github.com/sympozium-ai/sympozium/internal/channel"
)

// Default Slack reaction emojis used when SlackChannelOptions does not
// override them. The disable sentinel lives in the API package
// (sympoziumv1alpha1.SlackReactionDisabled) so spec docs and runtime
// agree on a single source of truth.
const (
	defaultSlackEmojiOnTrigger = "eyes"
	defaultSlackEmojiOnStop    = "mute"
	defaultSlackEmojiOnStart   = "loud_sound"
)

// reactionForDecision returns the OutboundMessage that the router should
// publish to add a per-channel reaction for the given trigger decision,
// or nil when the channel doesn't support reactions, lacks the required
// metadata (e.g. message timestamp), or has the slot disabled.
//
// New channel types add their own case here; the router stays
// channel-agnostic.
func reactionForDecision(
	inst *sympoziumv1alpha1.Agent,
	msg channelpkg.InboundMessage,
	decision triggerDecision,
) *channelpkg.OutboundMessage {
	switch msg.Channel {
	case "slack":
		return slackReaction(inst, msg, decision)
	default:
		return nil
	}
}

// slackReaction builds a reactions.add OutboundMessage for Slack, or
// returns nil when the message lacks a timestamp or no emoji is
// configured for the decision.
func slackReaction(
	inst *sympoziumv1alpha1.Agent,
	msg channelpkg.InboundMessage,
	decision triggerDecision,
) *channelpkg.OutboundMessage {
	ts := msg.Metadata["ts"]
	if ts == "" {
		return nil
	}
	emoji := slackEmojiFor(slackChannelOptions(inst), decision)
	if emoji == "" {
		return nil
	}
	return &channelpkg.OutboundMessage{
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		Reaction:        emoji,
		TargetMessageID: ts,
	}
}

// slackChannelOptions returns the SlackChannelOptions for the Slack channel
// on the given Agent, or nil when not configured.
func slackChannelOptions(inst *sympoziumv1alpha1.Agent) *sympoziumv1alpha1.SlackChannelOptions {
	if inst == nil {
		return nil
	}
	for i := range inst.Spec.Channels {
		if inst.Spec.Channels[i].Type == "slack" {
			return inst.Spec.Channels[i].Slack
		}
	}
	return nil
}

// slackEmojiFor returns the Slack emoji name for the given trigger
// decision. Unset slots fall back to defaults; the literal value
// "none" disables the slot and returns "". Decisions that don't emit
// reactions (e.g. triggerDrop) also return "".
func slackEmojiFor(opts *sympoziumv1alpha1.SlackChannelOptions, decision triggerDecision) string {
	var raw, def string
	switch decision {
	case triggerProcess:
		def = defaultSlackEmojiOnTrigger
		if opts != nil {
			raw = opts.EmojiOnTrigger
		}
	case triggerStop:
		def = defaultSlackEmojiOnStop
		if opts != nil {
			raw = opts.EmojiOnStop
		}
	case triggerResume:
		def = defaultSlackEmojiOnStart
		if opts != nil {
			raw = opts.EmojiOnStart
		}
	default:
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return def
	case sympoziumv1alpha1.SlackReactionDisabled:
		return ""
	default:
		return strings.Trim(raw, ":")
	}
}
