package ipc

import (
	"os"
	"testing"
)

func TestOutboundChannelName(t *testing.T) {
	if got := outboundChannelName([]byte(`{"channel":"slack","text":"hi"}`)); got != "slack" {
		t.Errorf("got %q, want slack", got)
	}
	if got := outboundChannelName([]byte(`{"text":"no channel"}`)); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := outboundChannelName([]byte(`not json`)); got != "" {
		t.Errorf("got %q, want empty for invalid json", got)
	}
}

func TestOutboundChannelAllowed(t *testing.T) {
	msg := func(ch string) []byte { return []byte(`{"channel":"` + ch + `","text":"x"}`) }

	tests := []struct {
		name    string
		enforce bool
		allowed map[string]bool
		data    []byte
		want    bool
	}{
		{"enforcement off allows anything", false, nil, msg("slack"), true},
		{"allowed channel passes", true, map[string]bool{"slack": true}, msg("slack"), true},
		{"case-insensitive match", true, map[string]bool{"slack": true}, msg("Slack"), true},
		{"disallowed channel dropped", true, map[string]bool{"telegram": true}, msg("slack"), false},
		{"empty allowlist drops all", true, map[string]bool{}, msg("slack"), false},
		{"missing channel field dropped", true, map[string]bool{"slack": true}, []byte(`{"text":"x"}`), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Bridge{
				enforceOutboundChannels: tt.enforce,
				allowedOutboundChannels: tt.allowed,
			}
			if got := b.outboundChannelAllowed(tt.data); got != tt.want {
				t.Errorf("outboundChannelAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseAllowedOutboundChannels(t *testing.T) {
	t.Run("unset means no enforcement", func(t *testing.T) {
		// Register restoration via t.Setenv, then unset for the assertion.
		t.Setenv("ALLOWED_OUTBOUND_CHANNELS", "")
		os.Unsetenv("ALLOWED_OUTBOUND_CHANNELS")
		set, enforce := parseAllowedOutboundChannels()
		if enforce {
			t.Errorf("expected enforce=false when unset, got true (set=%v)", set)
		}
	})

	t.Run("present but empty enforces with empty set", func(t *testing.T) {
		t.Setenv("ALLOWED_OUTBOUND_CHANNELS", "")
		set, enforce := parseAllowedOutboundChannels()
		if !enforce {
			t.Error("expected enforce=true when present (even empty)")
		}
		if len(set) != 0 {
			t.Errorf("expected empty set, got %v", set)
		}
	})

	t.Run("parses and normalizes list", func(t *testing.T) {
		t.Setenv("ALLOWED_OUTBOUND_CHANNELS", " Slack , telegram ,")
		set, enforce := parseAllowedOutboundChannels()
		if !enforce {
			t.Fatal("expected enforce=true")
		}
		if !set["slack"] || !set["telegram"] {
			t.Errorf("expected slack+telegram, got %v", set)
		}
		if len(set) != 2 {
			t.Errorf("expected 2 entries (blank trimmed), got %v", set)
		}
	})
}
