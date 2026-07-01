//go:build isi1497

// Tests for ISI-1497 Slack agent routing: designated receiver selection and
// @name → delegation resolution.  Build tag keeps these out of standard CI
// until the SlackListener CRD field (C1) and router functions (C2/C3) land.
// Remove the build tag in the same PR that ships C1+C2+C3.

package controller

import (
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// resolveSlackReceiver returns the AgentConfigSpec marked slackListener=true.
// If none is set it returns nil (caller falls back to first Slack-bound agent).
// If more than one is set, the first match is used.
//
// This function is implemented in C2 (internal/controller/channel_router.go).

func TestResolveSlackReceiver(t *testing.T) {
	tests := []struct {
		name        string
		agentCfgs   []sympoziumv1alpha1.AgentConfigSpec
		wantName    string // empty = expect nil
		wantNil     bool
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
