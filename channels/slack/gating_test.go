package main

import (
	"testing"
	"time"
)

func TestClassifyKind(t *testing.T) {
	cases := []struct {
		name        string
		channelType string
		text        string
		botID       string
		want        triggerKind
	}{
		{"dm via channel_type=im", "im", "hello", "UBOT", kindDM},
		{"mention with bot id", "channel", "hey <@UBOT> ping", "UBOT", kindMention},
		{"mention without bot id falls through", "channel", "hey <@UBOT> ping", "", kindChannel},
		{"plain channel message", "channel", "noise", "UBOT", kindChannel},
		{"group message no mention", "group", "noise", "UBOT", kindChannel},
		{"empty bot id never matches mention", "channel", "<@UBOT>", "", kindChannel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyKind(tc.channelType, tc.text, tc.botID); got != tc.want {
				t.Fatalf("classifyKind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAccessAllowed(t *testing.T) {
	cfg := &slackConfig{
		allowedChats:   csvToSet("C1,C2"),
		allowedSenders: csvToSet("U1,U2"),
		deniedSenders:  csvToSet("U3"),
	}
	cases := []struct {
		name           string
		sender, chat   string
		want           bool
	}{
		{"chat allowed, sender allowed", "U1", "C1", true},
		{"chat not in allowlist", "U1", "C9", false},
		{"sender not in allowlist", "U9", "C1", false},
		{"sender denylist overrides allowlist absence", "U3", "C1", false},
		{"sender denylist overrides allowlist", "U3", "C2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cfg.accessAllowed(tc.sender, tc.chat); got != tc.want {
				t.Fatalf("accessAllowed(%q,%q) = %v, want %v", tc.sender, tc.chat, got, tc.want)
			}
		})
	}

	t.Run("empty allowlists permit anyone not denied", func(t *testing.T) {
		empty := &slackConfig{deniedSenders: csvToSet("U9")}
		if !empty.accessAllowed("U1", "C1") {
			t.Fatal("expected allow when no allowlists configured")
		}
		if empty.accessAllowed("U9", "C1") {
			t.Fatal("expected deny for explicitly denied sender")
		}
	})
}

func TestTriggerAllowed(t *testing.T) {
	t.Run("empty list allows everything", func(t *testing.T) {
		cfg := &slackConfig{}
		for _, k := range []triggerKind{kindDM, kindMention, kindChannel} {
			if !cfg.triggerAllowed(k) {
				t.Fatalf("expected %q to be allowed under empty list", k)
			}
		}
	})
	t.Run("only listed kinds allowed", func(t *testing.T) {
		cfg := &slackConfig{allowedTriggers: csvToSet("mention,dm")}
		if !cfg.triggerAllowed(kindMention) || !cfg.triggerAllowed(kindDM) {
			t.Fatal("mention and dm should be allowed")
		}
		if cfg.triggerAllowed(kindChannel) {
			t.Fatal("channel should be denied")
		}
	})
}

// --- evaluateInbound: stateless paths -----------------------------------------------

func TestEvaluateInbound_Stateless(t *testing.T) {
	te := newThreadEngagement(time.Hour)

	t.Run("access denial drops without state mutation", func(t *testing.T) {
		cfg := &slackConfig{deniedSenders: csvToSet("U3")}
		got, _ := evaluateInbound(cfg, te, "UBOT", "U3", "C1", "", "TS001", "channel", "hey <@UBOT>")
		if got != gateDrop {
			t.Fatalf("decision = %v, want gateDrop", got)
		}
		if st := te.get("C1", ""); st != nil {
			t.Fatalf("expected no state for denied user, got %+v", st)
		}
	})

	t.Run("kind not in allowlist drops top-level message", func(t *testing.T) {
		cfg := &slackConfig{allowedTriggers: csvToSet("mention")}
		got, _ := evaluateInbound(cfg, te, "UBOT", "U1", "C1", "", "TS001", "channel", "no mention here")
		if got != gateDrop {
			t.Fatalf("decision = %v, want gateDrop", got)
		}
	})

	t.Run("kind in allowlist allows top-level message", func(t *testing.T) {
		cfg := &slackConfig{allowedTriggers: csvToSet("mention")}
		got, _ := evaluateInbound(cfg, te, "UBOT", "U1", "C1", "", "TS001", "channel", "hey <@UBOT>")
		if got != gateAllow {
			t.Fatalf("decision = %v, want gateAllow", got)
		}
	})

	t.Run("dm bypasses allowlist when dm listed", func(t *testing.T) {
		cfg := &slackConfig{allowedTriggers: csvToSet("dm")}
		got, _ := evaluateInbound(cfg, te, "UBOT", "U1", "D1", "", "TS001", "im", "hello")
		if got != gateAllow {
			t.Fatalf("decision = %v, want gateAllow", got)
		}
	})
}

// --- evaluateInbound: thread stickiness state machine -------------------------------

func TestEvaluateInbound_StickyThread_HappyPath(t *testing.T) {
	cfg := &slackConfig{
		threading:        true,
		threadStickiness: true,
		allowedTriggers:  csvToSet("mention"),
	}
	te := newThreadEngagement(time.Hour)
	const (
		botID  = "UBOT"
		alice  = "UALICE"
		chat   = "C1"
		thread = "1700000000.0001"
	)

	// Alice opens the thread by mentioning the bot.
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "hi <@UBOT>"); got != gateAllow {
		t.Fatalf("opening mention dropped: %v", got)
	}
	// Alice replies in-thread without mention — sticky should allow.
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "follow-up"); got != gateAllow {
		t.Fatalf("sticky reply dropped: %v", got)
	}
	st := te.get(chat, thread)
	if st == nil || st.owner != alice || st.interrupted {
		t.Fatalf("unexpected state: %+v", st)
	}
}

func TestEvaluateInbound_StickyThread_StrangerInterrupts(t *testing.T) {
	cfg := &slackConfig{
		threading:        true,
		threadStickiness: true,
		allowedTriggers:  csvToSet("mention"),
	}
	te := newThreadEngagement(time.Hour)
	const (
		botID  = "UBOT"
		alice  = "UALICE"
		bob    = "UBOB"
		chat   = "C1"
		thread = "1700000000.0001"
	)

	// Alice engages.
	_, _ = evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "hi <@UBOT>")

	// Bob (stranger) speaks in-thread without mention → drop, mark interrupted.
	if got, _ := evaluateInbound(cfg, te, botID, bob, chat, thread, "TS001", "channel", "lurking"); got != gateDrop {
		t.Fatalf("stranger plain text should be dropped, got %v", got)
	}
	st := te.get(chat, thread)
	if !st.interrupted {
		t.Fatal("expected interrupted=true after stranger spoke")
	}
	if st.owner == bob {
		t.Fatal("stranger must not become the owner")
	}

	// Alice replies without re-mentioning → must be dropped (interrupted).
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "still here"); got != gateDrop {
		t.Fatalf("alice plain reply after interruption should drop, got %v", got)
	}
	if !te.get(chat, thread).interrupted {
		t.Fatal("alice plain reply must not clear interrupted")
	}

	// Alice re-mentions → allowed, but interrupted stays true: sticky
	// free-flow never resumes once a stranger has touched the thread.
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "hey <@UBOT> still there?"); got != gateAllow {
		t.Fatalf("alice re-mention should allow, got %v", got)
	}
	if !te.get(chat, thread).interrupted {
		t.Fatal("interrupted must remain true for the rest of the thread's life")
	}
	// Alice's next plain reply is dropped — every message now needs a trigger.
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "follow-up"); got != gateDrop {
		t.Fatalf("alice plain reply after stranger interrupt must drop, got %v", got)
	}
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "<@UBOT> follow-up"); got != gateAllow {
		t.Fatalf("alice re-mention should always be allowed, got %v", got)
	}
}

func TestEvaluateInbound_StickyThread_StrangerWithMentionEngages(t *testing.T) {
	cfg := &slackConfig{
		threading:        true,
		threadStickiness: true,
		allowedTriggers:  csvToSet("mention"),
	}
	te := newThreadEngagement(time.Hour)
	const (
		botID  = "UBOT"
		alice  = "UALICE"
		bob    = "UBOB"
		chat   = "C1"
		thread = "1700000000.0001"
	)
	_, _ = evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "<@UBOT>")
	// Bob joins with a mention: allowed (trigger satisfied) and engaged,
	// but the thread is now permanently interrupted because a non-engaged
	// sender appeared.
	if got, _ := evaluateInbound(cfg, te, botID, bob, chat, thread, "TS001", "channel", "<@UBOT> me too"); got != gateAllow {
		t.Fatalf("stranger with mention should be allowed: %v", got)
	}
	st := te.get(chat, thread)
	if st.owner != alice {
		t.Fatalf("expected alice still to be owner, got %+v", st)
	}
	if !st.interrupted {
		t.Fatalf("expected thread to be interrupted after stranger joined, got %+v", st)
	}
	// Alice's plain reply now drops — interrupted never resumes.
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "thanks"); got != gateDrop {
		t.Fatalf("alice plain reply after stranger join must drop, got %v", got)
	}
}

func TestEvaluateInbound_StickyThread_DeniedStrangerInterrupts(t *testing.T) {
	cfg := &slackConfig{
		threading:        true,
		threadStickiness: true,
		allowedTriggers:  csvToSet("mention"),
		deniedSenders:    csvToSet("UEVIL"),
	}
	te := newThreadEngagement(time.Hour)
	const (
		botID  = "UBOT"
		alice  = "UALICE"
		evil   = "UEVIL"
		chat   = "C1"
		thread = "1700000000.0001"
	)
	_, _ = evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "<@UBOT>")

	// Denied user speaks: dropped at access gate, but the thread is
	// marked interrupted because a non-engaged sender appeared.
	if got, _ := evaluateInbound(cfg, te, botID, evil, chat, thread, "TS001", "channel", "<@UBOT> hi"); got != gateDrop {
		t.Fatalf("denied user must drop, got %v", got)
	}
	st := te.get(chat, thread)
	if !st.interrupted {
		t.Fatal("denied stranger must interrupt the thread")
	}
	if st.owner == evil {
		t.Fatal("access-denied user must never become owner")
	}
	// Alice's plain reply is now dropped because the thread is interrupted.
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "ok"); got != gateDrop {
		t.Fatalf("alice plain reply must drop after stranger interruption, got %v", got)
	}
	// Alice re-mentions: allowed (trigger satisfied), but interrupted
	// stays true \u2014 lax mode never resumes.
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "<@UBOT> still there?"); got != gateAllow {
		t.Fatalf("alice re-mention must re-engage, got %v", got)
	}
	if !te.get(chat, thread).interrupted {
		t.Fatal("interrupted must persist permanently after stranger interrupted")
	}
}

func TestEvaluateInbound_StickyDisabled_RequiresMentionEveryTime(t *testing.T) {
	cfg := &slackConfig{
		threading:        true,
		threadStickiness: false, // off
		allowedTriggers:  csvToSet("mention"),
	}
	te := newThreadEngagement(time.Hour)
	const (
		botID  = "UBOT"
		alice  = "UALICE"
		chat   = "C1"
		thread = "1700000000.0001"
	)
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "<@UBOT>"); got != gateAllow {
		t.Fatalf("opening mention dropped: %v", got)
	}
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, thread, "TS001", "channel", "follow-up"); got != gateDrop {
		t.Fatalf("without stickiness, plain follow-up must drop: %v", got)
	}
}

// TestEvaluateInbound_TopLevelMentionEngagesForThreadFollowUp covers the
// real-world flow: user @-mentions the bot in a top-level message; the
// bot replies in a new thread keyed on the inbound message's ts; the
// user then sends a plain follow-up inside that thread. With
// threadStickiness=true that follow-up MUST pass without re-mention.
func TestEvaluateInbound_TopLevelMentionEngagesForThreadFollowUp(t *testing.T) {
	cfg := &slackConfig{
		threading:        true,
		threadStickiness: true,
		allowedTriggers:  csvToSet("mention"),
	}
	te := newThreadEngagement(time.Hour)
	const (
		botID = "UBOT"
		alice = "UALICE"
		chat  = "C1"
		ts    = "1700000000.0001"
	)

	// Top-level message: threadTS empty, ts is the message's own ts.
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, "", ts, "channel", "<@UBOT> what is k8s?"); got != gateAllow {
		t.Fatalf("top-level mention dropped: %v", got)
	}
	// Bot replies in thread anchored on `ts`. Ownership must have been
	// recorded against that anchor.
	st := te.get(chat, ts)
	if st == nil || st.owner != alice {
		t.Fatalf("expected alice to be owner on ts=%q after opening mention, got %+v", ts, st)
	}

	// Plain follow-up arrives with threadTS == ts (Slack semantics).
	if got, _ := evaluateInbound(cfg, te, botID, alice, chat, ts, ts+".reply", "channel", "tell me more"); got != gateAllow {
		t.Fatalf("in-thread follow-up after top-level mention should allow, got %v", got)
	}
}

func TestThreadEngagement_TTLEvicts(t *testing.T) {
	te := newThreadEngagement(10 * time.Millisecond)
	te.update("C1", "T1", func(s *threadState) {
		s.owner = "U1"
	})
	if st := te.get("C1", "T1"); st == nil {
		t.Fatal("entry should exist immediately")
	}
	time.Sleep(20 * time.Millisecond)
	if st := te.get("C1", "T1"); st != nil {
		t.Fatalf("entry should have been evicted, got %+v", st)
	}
}
