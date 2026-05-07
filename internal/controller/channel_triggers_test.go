package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestEvaluateTrigger(t *testing.T) {
	spec := &sympoziumv1alpha1.ChannelTriggerSpec{
		StopKeywords:  []string{"bot stop", "shut up"},
		StartKeywords: []string{"bot resume"},
	}

	cases := []struct {
		name         string
		spec         *sympoziumv1alpha1.ChannelTriggerSpec
		text         string
		muted        bool
		wantDecision triggerDecision
	}{
		{"nil spec processes", nil, "anything", false, triggerProcess},
		{"nil spec ignores muted state", nil, "anything", true, triggerProcess},
		{"active no match -> process", spec, "hello there", false, triggerProcess},
		{"active stop keyword -> stop+mute", spec, "Bot Stop please", false, triggerStop},
		{"active stop substring -> stop", spec, "ok shut up now", false, triggerStop},
		{"active start keyword ignored", spec, "bot resume", false, triggerProcess},
		{"muted no match -> drop", spec, "hello there", true, triggerDrop},
		{"muted stop keyword ignored", spec, "bot stop", true, triggerDrop},
		{"muted start keyword -> resume", spec, "Bot Resume", true, triggerResume},
		{"empty text active", spec, "", false, triggerProcess},
		{"empty text muted", spec, "", true, triggerDrop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateTrigger(tc.spec, tc.text, tc.muted)
			if got != tc.wantDecision {
				t.Errorf("decision = %v, want %v", got, tc.wantDecision)
			}
		})
	}
}

func TestEvaluateTriggerEmptyKeywords(t *testing.T) {
	spec := &sympoziumv1alpha1.ChannelTriggerSpec{
		StopKeywords:  []string{"", "   "},
		StartKeywords: []string{"", "\t"},
	}
	if d := evaluateTrigger(spec, "anything", false); d != triggerProcess {
		t.Errorf("active: want process, got %v", d)
	}
	if d := evaluateTrigger(spec, "anything", true); d != triggerDrop {
		t.Errorf("muted: want drop, got %v", d)
	}
}

func newMuteStoreScheme(t *testing.T) *runtime.Scheme {
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

func TestMuteStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(newMuteStoreScheme(t)).Build()
	owner := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-x", Namespace: "ns", UID: "uid-1"},
	}
	store := newMuteStore(c, owner)

	// Initially not muted (ConfigMap doesn't exist).
	muted, err := store.IsMuted(ctx, "slack", "C123")
	if err != nil || muted {
		t.Fatalf("initial IsMuted: muted=%v err=%v", muted, err)
	}

	// Setting to false on missing ConfigMap is a no-op.
	if err := store.SetMuted(ctx, "slack", "C123", false); err != nil {
		t.Fatalf("clear missing: %v", err)
	}
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "agent-x-channel-state"}, cm); err == nil {
		t.Fatalf("expected ConfigMap not to exist after no-op clear")
	}

	// Mute creates the ConfigMap.
	if err := store.SetMuted(ctx, "slack", "C123", true); err != nil {
		t.Fatalf("mute: %v", err)
	}
	muted, err = store.IsMuted(ctx, "slack", "C123")
	if err != nil || !muted {
		t.Fatalf("after mute: muted=%v err=%v", muted, err)
	}

	// The created ConfigMap must carry an ownerReference back to the
	// Agent so that K8s GC reclaims it when the Agent is deleted.
	if err := c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "agent-x-channel-state"}, cm); err != nil {
		t.Fatalf("get state cm: %v", err)
	}
	var ownerOK bool
	for _, or := range cm.OwnerReferences {
		if or.Kind == "Agent" && or.Name == "agent-x" && or.UID == "uid-1" {
			ownerOK = true
			break
		}
	}
	if !ownerOK {
		t.Fatalf("expected ownerReference to Agent agent-x, got %+v", cm.OwnerReferences)
	}

	// A second mute is idempotent.
	if err := store.SetMuted(ctx, "slack", "C123", true); err != nil {
		t.Fatalf("re-mute: %v", err)
	}

	// Different chat is independent.
	muted, err = store.IsMuted(ctx, "slack", "C999")
	if err != nil || muted {
		t.Fatalf("other chat: muted=%v err=%v", muted, err)
	}

	// Unmute removes the entry.
	if err := store.SetMuted(ctx, "slack", "C123", false); err != nil {
		t.Fatalf("unmute: %v", err)
	}
	muted, err = store.IsMuted(ctx, "slack", "C123")
	if err != nil || muted {
		t.Fatalf("after unmute: muted=%v err=%v", muted, err)
	}
}

func TestChannelTriggerSpecLookup(t *testing.T) {
	inst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Channels: []sympoziumv1alpha1.ChannelSpec{
				{Type: "telegram"},
				{Type: "slack", Triggers: &sympoziumv1alpha1.ChannelTriggerSpec{StopKeywords: []string{"x"}}},
			},
		},
	}
	if got := channelTriggerSpec(inst, "slack"); got == nil || len(got.StopKeywords) != 1 {
		t.Errorf("slack: want non-nil with 1 stop keyword, got %+v", got)
	}
	if got := channelTriggerSpec(inst, "telegram"); got != nil {
		t.Errorf("telegram: want nil, got %+v", got)
	}
	if got := channelTriggerSpec(inst, "discord"); got != nil {
		t.Errorf("discord (missing): want nil, got %+v", got)
	}
	if got := channelTriggerSpec(nil, "slack"); got != nil {
		t.Errorf("nil instance: want nil, got %+v", got)
	}
}

func TestChannelMuteKey(t *testing.T) {
	// Stable hash format — guards against accidental changes that would
	// orphan all existing mute entries on upgrade.
	got := channelMuteKey("slack", "C123")
	want := "slack.abefcf257b5d2ff2" // sha256("C123")[:8] hex
	if got != want {
		t.Errorf("channelMuteKey(slack,C123) = %q, want %q", got, want)
	}

	// Different chat IDs produce different keys.
	if channelMuteKey("slack", "C123") == channelMuteKey("slack", "C999") {
		t.Errorf("expected distinct keys for distinct chat IDs")
	}

	// Different channel types produce different keys even with the
	// same chat ID.
	if channelMuteKey("slack", "X") == channelMuteKey("discord", "X") {
		t.Errorf("expected distinct keys across channel types")
	}

	// Chat IDs containing characters disallowed in ConfigMap data keys
	// (':', '@', '/') must still produce valid keys ([-._a-zA-Z0-9]+).
	for _, raw := range []string{"matrix:!abc:server", "user@whatsapp.net", "team/channel"} {
		k := channelMuteKey("x", raw)
		for _, r := range k {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '.' || r == '_'
			if !ok {
				t.Errorf("channelMuteKey(%q) = %q contains invalid rune %q", raw, k, r)
			}
		}
	}
}
