package main

import (
	"testing"
	"time"
)

func TestResolveProbeInterval(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		want         time.Duration
		wantFellBack bool
	}{
		{"valid", "45s", 45 * time.Second, false},
		{"valid minutes", "2m", 2 * time.Minute, false},
		{"zero falls back", "0s", 30 * time.Second, true},
		{"negative falls back", "-5s", 30 * time.Second, true},
		{"garbage falls back", "not-a-duration", 30 * time.Second, true},
		{"empty falls back", "", 30 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := resolveProbeInterval(tc.raw)
			if got != tc.want {
				t.Errorf("resolveProbeInterval(%q) = %v, want %v", tc.raw, got, tc.want)
			}
			if (reason != "") != tc.wantFellBack {
				t.Errorf("resolveProbeInterval(%q) reason=%q, wantFellBack=%v", tc.raw, reason, tc.wantFellBack)
			}
			// The returned duration must always be positive so time.NewTicker
			// never panics.
			if got <= 0 {
				t.Errorf("resolveProbeInterval(%q) returned non-positive %v", tc.raw, got)
			}
		})
	}
}
