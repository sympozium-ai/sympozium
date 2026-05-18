package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// slackConfig captures Slack-specific gating configuration injected by the
// controller. All fields are optional; defaults preserve the prior
// "everything triggers, no threading" behaviour.
type slackConfig struct {
	threading        bool
	threadStickiness bool
	allowedTriggers  map[string]bool // "mention" | "dm" | "channel" — empty means allow all
	allowedSenders   map[string]bool // empty means allow all
	deniedSenders    map[string]bool
	allowedChats     map[string]bool // empty means allow all
}

func loadSlackConfig() *slackConfig {
	return &slackConfig{
		threading:        os.Getenv("SLACK_THREADING") == "true",
		threadStickiness: os.Getenv("SLACK_THREAD_STICKINESS") == "true",
		allowedTriggers:  csvToSet(os.Getenv("SLACK_ALLOWED_TRIGGERS")),
		allowedSenders:   csvToSet(os.Getenv("SLACK_ALLOWED_SENDERS")),
		deniedSenders:    csvToSet(os.Getenv("SLACK_DENIED_SENDERS")),
		allowedChats:     csvToSet(os.Getenv("SLACK_ALLOWED_CHATS")),
	}
}

func csvToSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out[p] = true
		}
	}
	return out
}

// triggerKind classifies an inbound Slack message.
type triggerKind string

const (
	kindDM       triggerKind = "dm"
	kindMention  triggerKind = "mention"
	kindChannel  triggerKind = "channel"
	kindReaction triggerKind = "reaction" // not a message; never gated
)

// classifyKind decides whether the message is a DM, an @-mention of the
// bot, or a generic channel/group message.
func classifyKind(channelType, text, botID string) triggerKind {
	if channelType == "im" {
		return kindDM
	}
	if botID != "" && strings.Contains(text, "<@"+botID+">") {
		return kindMention
	}
	return kindChannel
}

// accessAllowed enforces the user/chat allow-deny lists. Returns false when
// the message must be dropped before any state mutation.
func (c *slackConfig) accessAllowed(senderID, chatID string) bool {
	if len(c.allowedChats) > 0 && !c.allowedChats[chatID] {
		return false
	}
	if len(c.allowedSenders) > 0 && !c.allowedSenders[senderID] {
		return false
	}
	if c.deniedSenders[senderID] {
		return false
	}
	return true
}

// triggerAllowed returns true when the kind satisfies AllowedTriggers.
// Empty allowlist means all kinds pass.
func (c *slackConfig) triggerAllowed(k triggerKind) bool {
	if len(c.allowedTriggers) == 0 {
		return true
	}
	return c.allowedTriggers[string(k)]
}

// threadState tracks per-thread state under thread-stickiness mode.
//
// Sticky-thread semantics:
//   - The first sender to address the bot in a thread becomes the
//     thread's `owner`.
//   - Any message from anyone other than the owner — regardless of
//     access, trigger, or content — permanently marks the thread
//     `interrupted`.
//   - Once interrupted, every message (including from the owner) must
//     satisfy the trigger rules (e.g. @-mention) to be processed.
type threadState struct {
	owner       string // userID that opened the thread
	interrupted bool
	lastSeen    time.Time
}

// threadEngagement is a TTL-bounded map of (chat,thread) → state. Lives in
// memory in the Slack pod; lost on restart, which is acceptable: worst
// case the bot asks for an @ once after a restart.
type threadEngagement struct {
	mu      sync.Mutex
	entries map[string]*threadState
	ttl     time.Duration
}

func newThreadEngagement(ttl time.Duration) *threadEngagement {
	return &threadEngagement{
		entries: map[string]*threadState{},
		ttl:     ttl,
	}
}

func threadKey(chatID, threadTS string) string {
	return chatID + "/" + threadTS
}

func (te *threadEngagement) get(chatID, threadTS string) *threadState {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.evictLocked()
	st, ok := te.entries[threadKey(chatID, threadTS)]
	if !ok {
		return nil
	}
	return st
}

func (te *threadEngagement) update(chatID, threadTS string, fn func(*threadState)) {
	te.mu.Lock()
	defer te.mu.Unlock()
	te.evictLocked()
	k := threadKey(chatID, threadTS)
	st, ok := te.entries[k]
	if !ok {
		st = &threadState{}
		te.entries[k] = st
	}
	fn(st)
	st.lastSeen = time.Now()
}

func (te *threadEngagement) evictLocked() {
	if te.ttl <= 0 {
		return
	}
	cutoff := time.Now().Add(-te.ttl)
	for k, st := range te.entries {
		if st.lastSeen.Before(cutoff) {
			delete(te.entries, k)
		}
	}
}

// gateDecision is the per-message outcome from gating logic.
type gateDecision int

const (
	gateAllow gateDecision = iota
	gateDrop
)

// evaluateInbound runs the full gating pipeline for one inbound message.
// ts is the Slack ts of the message itself; for a top-level message
// under threading mode this becomes the anchor of the thread the bot
// will reply in, so we use it to record initial ownership.
//
// The second return value is a short, human-readable reason explaining
// the decision (e.g. "access:chat-not-allowed", "trigger:kind=channel",
// "sticky:thread-interrupted", "allow:owner-freeflow"). It is intended
// purely for operator logging.
func evaluateInbound(
	cfg *slackConfig,
	te *threadEngagement,
	botID, senderID, chatID, threadTS, ts, channelType, text string,
) (gateDecision, string) {
	kind := classifyKind(channelType, text, botID)

	inThread := threadTS != ""
	sticky := cfg.threading && cfg.threadStickiness && inThread

	// Sticky-thread interruption: any sender other than the thread's
	// owner permanently marks the thread interrupted, regardless of
	// access control, trigger, or content. This runs before access
	// control so even a denied user's message latches the interrupt.
	if sticky {
		st := te.get(chatID, threadTS)
		if st != nil && st.owner != "" && st.owner != senderID {
			te.update(chatID, threadTS, func(s *threadState) {
				s.interrupted = true
			})
		}
	}

	if !cfg.accessAllowed(senderID, chatID) {
		return gateDrop, fmt.Sprintf("access denied: sender=%s chat=%s", senderID, chatID)
	}

	// Sticky-thread evaluation.
	if sticky {
		st := te.get(chatID, threadTS)
		switch {
		case st == nil || st.owner == "":
			// Unowned thread: claim ownership iff this sender passes
			// the trigger rules (e.g. opening @-mention).
			if !cfg.triggerAllowed(kind) {
				return gateDrop, fmt.Sprintf("trigger not allowed: kind=%s", kind)
			}
			te.update(chatID, threadTS, func(s *threadState) {
				if s.owner == "" {
					s.owner = senderID
				}
			})
			return gateAllow, "sticky: claimed thread ownership"
		case st.owner != senderID, st.interrupted:
			// Either a non-owner is speaking, or the owner is speaking
			// in a thread that's been interrupted. Both require a
			// trigger every time; lax free-flow never returns once
			// interrupted.
			if !cfg.triggerAllowed(kind) {
				return gateDrop, fmt.Sprintf("sticky interrupted/non-owner, trigger not allowed: kind=%s", kind)
			}
			return gateAllow, "sticky: trigger satisfied"
		default:
			// Owner free-flow.
			te.update(chatID, threadTS, func(s *threadState) {})
			return gateAllow, "sticky: owner free-flow"
		}
	}

	// Stateless evaluation.
	if !cfg.triggerAllowed(kind) {
		return gateDrop, fmt.Sprintf("trigger not allowed: kind=%s", kind)
	}

	// When threading+stickiness is on and this top-level message will
	// spawn a new thread, record this sender as the thread's owner so
	// their plain follow-ups inside the thread are recognised.
	if cfg.threading && cfg.threadStickiness {
		anchor := threadTS
		if anchor == "" {
			anchor = ts
		}
		if anchor != "" {
			te.update(chatID, anchor, func(s *threadState) {
				if s.owner == "" {
					s.owner = senderID
				}
			})
		}
	}
	return gateAllow, fmt.Sprintf("allow: kind=%s", kind)
}

// resolveBotUserID calls Slack auth.test to discover the bot's own user ID,
// which is needed for @-mention detection in messages.
func resolveBotUserID(ctx context.Context, client *http.Client, botToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/auth.test",
		strings.NewReader(url.Values{}.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var body struct {
		OK     bool   `json:"ok"`
		UserID string `json:"user_id"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if !body.OK {
		return "", fmt.Errorf("auth.test: %s", body.Error)
	}
	return body.UserID, nil
}
