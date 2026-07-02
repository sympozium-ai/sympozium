// Tests for ISI-1497 Slack agent routing: designated receiver selection and
// @name → delegation resolution.

package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	channelpkg "github.com/sympozium-ai/sympozium/internal/channel"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

func TestExtractNameMention(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		wantName      string
		wantRemainder string
		wantMention   bool // true = @word form; false = word: form or no match
	}{
		// @word form — isMention=true
		{
			name:          "@persona with body",
			text:          "@billing please check my invoice",
			wantName:      "billing",
			wantRemainder: "please check my invoice",
			wantMention:   true,
		},
		{
			name:          "@persona only (no body)",
			text:          "@engineering",
			wantName:      "engineering",
			wantRemainder: "",
			wantMention:   true,
		},
		{
			name:          "@unknown persona",
			text:          "@finance help",
			wantName:      "finance",
			wantRemainder: "help",
			wantMention:   true,
		},
		// ISI-1497 C7 — @mention honoured anywhere, not only leading token
		{
			name:          "@persona mid-message (numbered list)",
			text:          "1. @architect please review",
			wantName:      "architect",
			wantRemainder: "1. please review",
			wantMention:   true,
		},
		{
			name:          "@persona mid-message (trailing token)",
			text:          "please review this @architect",
			wantName:      "architect",
			wantRemainder: "please review this",
			wantMention:   true,
		},
		{
			name:          "multiple @mentions — first wins",
			text:          "@architect @winston review",
			wantName:      "architect",
			wantRemainder: "@winston review",
			wantMention:   true,
		},
		// Email/URL negative controls — '@' not on a word boundary, no match
		{
			name:          "email address — @ not a mention",
			text:          "email me at henrik@perfbytes.com please",
			wantName:      "",
			wantRemainder: "email me at henrik@perfbytes.com please",
			wantMention:   false,
		},
		{
			name:          "URL with userinfo @ — not a mention",
			text:          "see https://user@host/path for details",
			wantName:      "",
			wantRemainder: "see https://user@host/path for details",
			wantMention:   false,
		},
		// Slack keywords — must return no match (fall through, not deny)
		{
			name:          "@here — Slack keyword, no match",
			text:          "@here anyone around?",
			wantName:      "",
			wantRemainder: "@here anyone around?",
			wantMention:   false,
		},
		{
			name:          "@channel — Slack keyword, no match",
			text:          "@channel important update",
			wantName:      "",
			wantRemainder: "@channel important update",
			wantMention:   false,
		},
		{
			name:          "@everyone — Slack keyword, no match",
			text:          "@everyone heads up",
			wantName:      "",
			wantRemainder: "@everyone heads up",
			wantMention:   false,
		},
		{
			name:          "@HERE case-insensitive Slack keyword",
			text:          "@HERE please read",
			wantName:      "",
			wantRemainder: "@HERE please read",
			wantMention:   false,
		},
		// word: prefix form — isMention=false (fall through on no persona match)
		{
			name:          "word: prefix",
			text:          "billing: check my account",
			wantName:      "billing",
			wantRemainder: "check my account",
			wantMention:   false,
		},
		{
			name:          "Note: — should not deny",
			text:          "Note: this is important",
			wantName:      "Note",
			wantRemainder: "this is important",
			wantMention:   false,
		},
		{
			name:          "TODO: — should not deny",
			text:          "TODO: fix the bug",
			wantName:      "TODO",
			wantRemainder: "fix the bug",
			wantMention:   false,
		},
		// URL — word: prefix but word is "https" (no slash in candidate)
		{
			name:          "https:// URL — no match (slash after colon, candidate ok but remainder starts with //)",
			text:          "https://example.com",
			wantName:      "https",
			wantRemainder: "//example.com",
			wantMention:   false,
		},
		// Plain text — no prefix at all
		{
			name:          "plain message, no prefix",
			text:          "hello world",
			wantName:      "",
			wantRemainder: "hello world",
			wantMention:   false,
		},
		{
			name:          "empty string",
			text:          "",
			wantName:      "",
			wantRemainder: "",
			wantMention:   false,
		},
		{
			name:          "text with colons mid-sentence",
			text:          "hello: world: foo",
			wantName:      "hello",
			wantRemainder: "world: foo",
			wantMention:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotRemainder, gotMention := extractNameMention(tc.text)
			if gotName != tc.wantName {
				t.Errorf("name = %q, want %q", gotName, tc.wantName)
			}
			if gotRemainder != tc.wantRemainder {
				t.Errorf("remainder = %q, want %q", gotRemainder, tc.wantRemainder)
			}
			if gotMention != tc.wantMention {
				t.Errorf("isMention = %v, want %v", gotMention, tc.wantMention)
			}
		})
	}
}

// resolveSlackReceiver returns the AgentConfigSpec marked slackListener=true.
// If none is set it returns nil (caller falls back to first Slack-bound agent).
// If more than one is set, the first match is used.
//
// This function is implemented in C2 (internal/controller/channel_router.go).

func TestResolveSlackReceiver(t *testing.T) {
	tests := []struct {
		name      string
		agentCfgs []sympoziumv1alpha1.AgentConfigSpec
		wantName  string // empty = expect nil
		wantNil   bool
	}{
		{
			name:    "no agents — nil",
			wantNil: true,
		},
		{
			name: "no slackListener flag set — nil (fallback to first)",
			agentCfgs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "alpha"},
				{Name: "beta"},
			},
			wantNil: true,
		},
		{
			name: "single slackListener=true — returns that agent",
			agentCfgs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "triage", SlackListener: true},
				{Name: "billing"},
			},
			wantName: "triage",
		},
		{
			name: "slackListener on non-first agent — still found",
			agentCfgs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "billing"},
				{Name: "triage", SlackListener: true},
				{Name: "engineering"},
			},
			wantName: "triage",
		},
		{
			name: "multiple slackListener=true — first match wins",
			agentCfgs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "a", SlackListener: true},
				{Name: "b", SlackListener: true},
			},
			wantName: "a",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveSlackReceiver(tc.agentCfgs)
			if tc.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %q", got.Name)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %q, got nil", tc.wantName)
			}
			if got.Name != tc.wantName {
				t.Errorf("expected %q, got %q", tc.wantName, got.Name)
			}
		})
	}
}

// resolveNamedDelegate matches a bare @name token against AgentConfigSpec.Name
// and AgentConfigSpec.DisplayName (case-insensitive).  Returns nil when no
// match is found (unknown-name fallback: stay on receiver).
//
// This function is implemented in C3 (internal/controller/channel_router.go).

func TestResolveNamedDelegate(t *testing.T) {
	configs := []sympoziumv1alpha1.AgentConfigSpec{
		{Name: "triage", DisplayName: "Support Triage", SlackListener: true},
		{Name: "billing", DisplayName: "Billing Support"},
		{Name: "engineering", DisplayName: "Engineering Support"},
		{Name: "docs", DisplayName: "Documentation"},
	}

	tests := []struct {
		name     string
		mention  string // raw token after stripping leading @
		wantName string // expected AgentConfigSpec.Name; empty = nil
	}{
		{
			name:     "exact Name match",
			mention:  "billing",
			wantName: "billing",
		},
		{
			name:     "Name match case-insensitive",
			mention:  "BILLING",
			wantName: "billing",
		},
		{
			name:     "DisplayName match",
			mention:  "Billing Support",
			wantName: "billing",
		},
		{
			name:     "DisplayName match case-insensitive",
			mention:  "engineering support",
			wantName: "engineering",
		},
		{
			name:     "partial Name — no match (must be exact)",
			mention:  "bill",
			wantName: "",
		},
		{
			name:     "unknown name — nil (stay on receiver)",
			mention:  "finance",
			wantName: "",
		},
		{
			name:     "empty mention — nil",
			mention:  "",
			wantName: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveNamedDelegate(configs, tc.mention)
			if tc.wantName == "" {
				if got != nil {
					t.Errorf("expected nil, got %q", got.Name)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %q, got nil", tc.wantName)
			}
			if got.Name != tc.wantName {
				t.Errorf("expected %q, got %q", tc.wantName, got.Name)
			}
		})
	}
}

// TestNoListenerFallback verifies that when slackListener is absent the router
// behaves exactly as before ISI-1497: the first agent is used.
func TestNoListenerFallback(t *testing.T) {
	configs := []sympoziumv1alpha1.AgentConfigSpec{
		{Name: "alpha"},
		{Name: "beta"},
	}
	got := resolveSlackReceiver(configs)
	if got != nil {
		t.Errorf("expected nil for no-listener ensemble, got %q", got.Name)
	}
	// Caller interprets nil as "use first agent" — no extra assertion needed here.
}

// handleInboundScheme builds the runtime.Scheme required by the fake client
// used in handleInbound integration tests.
func handleInboundScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add sympoziumv1alpha1: %v", err)
	}
	return s
}

// outboundTexts extracts the Text fields from all outbound channel.message.send
// events captured by a recordingEventBus.
func outboundTexts(t *testing.T, bus *recordingEventBus) []string {
	t.Helper()
	var texts []string
	for _, rec := range bus.published {
		if rec.Topic != eventbus.TopicChannelMessageSend {
			continue
		}
		var out channelpkg.OutboundMessage
		if err := json.Unmarshal(rec.Event.Data, &out); err != nil {
			t.Fatalf("decode outbound: %v", err)
		}
		texts = append(texts, out.Text)
	}
	return texts
}

// TestHandleInbound_NameRouting exercises the three key paths in handleInbound's
// @name / name: routing block:
//
//   - known @persona  → no denial, redirect routes to the delegate Agent
//   - unknown @persona → denial published on the event bus
//   - word: prefix    → no denial (falls through; word: is not persona-directed)
func TestHandleInbound_NameRouting(t *testing.T) {
	const (
		ns           = "default"
		ensembleName = "myteam"
		receiverName = "myteam-triage"
		delegateName = "myteam-billing"
	)

	// Ensemble with two personas: "triage" (receiver) and "billing" (delegate).
	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: ensembleName, Namespace: ns},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "triage", SlackListener: true},
				{Name: "billing"},
			},
		},
	}

	agentLabels := map[string]string{
		"sympozium.ai/ensemble":     ensembleName,
		"sympozium.ai/agent-config": "triage",
	}
	receiver := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: receiverName, Namespace: ns, Labels: agentLabels},
	}
	delegate := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      delegateName,
			Namespace: ns,
			Labels: map[string]string{
				"sympozium.ai/ensemble":     ensembleName,
				"sympozium.ai/agent-config": "billing",
			},
		},
	}

	newRouter := func(t *testing.T) (*ChannelRouter, *recordingEventBus) {
		t.Helper()
		bus := &recordingEventBus{}
		c := fake.NewClientBuilder().
			WithScheme(handleInboundScheme(t)).
			WithObjects(ensemble, receiver, delegate).
			Build()
		return &ChannelRouter{
			Client:   c,
			EventBus: bus,
			Log:      logr.Discard(),
		}, bus
	}

	inboundEvent := func(text string) *eventbus.Event {
		msg := channelpkg.InboundMessage{
			InstanceName: receiverName,
			Channel:      "slack",
			ChatID:       "C1",
			Text:         text,
		}
		ev, _ := eventbus.NewEvent(eventbus.TopicChannelMessageRecv, nil, msg)
		return ev
	}

	t.Run("known @persona — no denial, AgentRun created for delegate", func(t *testing.T) {
		cr, bus := newRouter(t)
		cr.handleInbound(context.Background(), inboundEvent("@billing please invoice me"))
		texts := outboundTexts(t, bus)
		for _, text := range texts {
			if len(text) > 0 {
				t.Errorf("expected no outbound denial, got %q", text)
			}
		}
		// Verify an AgentRun was created targeting the delegate.
		var runs sympoziumv1alpha1.AgentRunList
		if err := cr.Client.List(context.Background(), &runs); err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs.Items) == 0 {
			t.Fatal("expected AgentRun to be created, got none")
		}
		got := runs.Items[0].Spec.AgentRef
		if got != delegateName {
			t.Errorf("AgentRun.Spec.AgentRef = %q, want %q", got, delegateName)
		}
	})

	t.Run("unknown @persona — denial sent, no AgentRun", func(t *testing.T) {
		cr, bus := newRouter(t)
		cr.handleInbound(context.Background(), inboundEvent("@finance help me"))
		texts := outboundTexts(t, bus)
		if len(texts) == 0 {
			t.Fatal("expected denial to be published, got none")
		}
		// denial must mention the unknown persona
		found := false
		for _, text := range texts {
			if len(text) > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("denial text was empty; got %v", texts)
		}
		var runs sympoziumv1alpha1.AgentRunList
		if err := cr.Client.List(context.Background(), &runs); err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs.Items) != 0 {
			t.Errorf("expected no AgentRun after denial, got %d", len(runs.Items))
		}
	})

	t.Run("Note: prefix — no denial, falls through to receiver AgentRun", func(t *testing.T) {
		cr, bus := newRouter(t)
		cr.handleInbound(context.Background(), inboundEvent("Note: this is not a persona"))
		// No denial should be published.
		for _, text := range outboundTexts(t, bus) {
			if len(text) > 0 {
				t.Errorf("expected no denial for word: prefix, got %q", text)
			}
		}
		// A run should be created for the original receiver (not routed away).
		var runs sympoziumv1alpha1.AgentRunList
		if err := cr.Client.List(context.Background(), &runs); err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs.Items) == 0 {
			t.Fatal("expected AgentRun to be created for receiver, got none")
		}
		got := runs.Items[0].Spec.AgentRef
		if got != receiverName {
			t.Errorf("AgentRun.Spec.AgentRef = %q, want %q (receiver)", got, receiverName)
		}
	})

	// ISI-1499 M1: the load-bearing SlackListener swap branch — an unaddressed
	// inbound message arrives at a NON-listener instance (myteam-billing) and
	// must be redirected to the slackListener=true persona (myteam-triage),
	// carrying the listener's persona name in AgentRef and run labels.
	t.Run("unaddressed inbound at non-listener — swaps to SlackListener persona", func(t *testing.T) {
		cr, bus := newRouter(t)
		msg := channelpkg.InboundMessage{
			InstanceName: delegateName, // arrives at the non-listener billing inst
			Channel:      "slack",
			ChatID:       "C1",
			Text:         "hello, I have a general question",
		}
		ev, _ := eventbus.NewEvent(eventbus.TopicChannelMessageRecv, nil, msg)
		cr.handleInbound(context.Background(), ev)

		// No denial should be published on a plain, unaddressed message.
		for _, text := range outboundTexts(t, bus) {
			if len(text) > 0 {
				t.Errorf("expected no denial for unaddressed message, got %q", text)
			}
		}

		var runs sympoziumv1alpha1.AgentRunList
		if err := cr.Client.List(context.Background(), &runs); err != nil {
			t.Fatalf("list runs: %v", err)
		}
		if len(runs.Items) == 0 {
			t.Fatal("expected AgentRun to be created for the SlackListener persona, got none")
		}
		run := runs.Items[0]
		if got := run.Spec.AgentRef; got != receiverName {
			t.Errorf("AgentRun.Spec.AgentRef = %q, want %q (SlackListener persona)", got, receiverName)
		}
		if got := run.Labels["sympozium.ai/agent-config"]; got != "triage" {
			t.Errorf("run label agent-config = %q, want %q (listener persona)", got, "triage")
		}
		if got := run.Labels["sympozium.ai/ensemble"]; got != ensembleName {
			t.Errorf("run label ensemble = %q, want %q", got, ensembleName)
		}
		if got := run.Labels["sympozium.ai/instance"]; got != receiverName {
			t.Errorf("run label instance = %q, want %q (swapped listener inst)", got, receiverName)
		}
	})
}

// TestHandleInbound_UnknownMentionMutedChannel verifies ISI-1524: an unknown
// @persona mention on a muted channel must NOT emit a denial response.
// The deferred denial only fires after access/mute checks pass.
func TestHandleInbound_UnknownMentionMutedChannel(t *testing.T) {
	const (
		ns           = "default"
		ensembleName = "myteam"
		receiverName = "myteam-triage"
	)

	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: ensembleName, Namespace: ns},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "triage", SlackListener: true},
				{Name: "billing"},
			},
		},
	}

	// Receiver agent with a stop keyword trigger configured.
	receiver := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      receiverName,
			Namespace: ns,
			Labels: map[string]string{
				"sympozium.ai/ensemble":     ensembleName,
				"sympozium.ai/agent-config": "triage",
			},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Channels: []sympoziumv1alpha1.ChannelSpec{
				{
					Type: "slack",
					Triggers: &sympoziumv1alpha1.ChannelTriggerSpec{
						StopKeywords:  []string{"stop"},
						StartKeywords: []string{"start"},
					},
				},
			},
		},
	}

	// Pre-create the channel-state ConfigMap with the chat muted.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      receiverName + "-channel-state",
			Namespace: ns,
		},
		Data: map[string]string{
			channelMuteKey("slack", "C1"): channelMuteValue,
		},
	}

	bus := &recordingEventBus{}
	c := fake.NewClientBuilder().
		WithScheme(handleInboundScheme(t)).
		WithObjects(ensemble, receiver, cm).
		Build()

	cr := &ChannelRouter{
		Client:   c,
		EventBus: bus,
		Log:      logr.Discard(),
	}

	msg := channelpkg.InboundMessage{
		InstanceName: receiverName,
		Channel:      "slack",
		ChatID:       "C1",
		Text:         "@finance help me",
	}
	ev, _ := eventbus.NewEvent(eventbus.TopicChannelMessageRecv, nil, msg)
	cr.handleInbound(context.Background(), ev)

	// No denial should be emitted because the channel is muted.
	for _, text := range outboundTexts(t, bus) {
		if len(text) > 0 {
			t.Errorf("expected no outbound denial on muted channel, got %q", text)
		}
	}

	// No AgentRun should be created either (chat is muted).
	var runs sympoziumv1alpha1.AgentRunList
	if err := cr.Client.List(context.Background(), &runs); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 0 {
		t.Errorf("expected no AgentRun on muted channel, got %d", len(runs.Items))
	}
}
