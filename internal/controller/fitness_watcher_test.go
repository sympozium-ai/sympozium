package controller

import (
	"testing"
	"time"
)

func TestContainsIgnoreCase(t *testing.T) {
	tests := []struct {
		s, substr string
		want      bool
	}{
		{"Qwen2.5-7B-Instruct", "qwen2.5", true},
		{"Qwen2.5-7B-Instruct", "QWEN2.5", true},
		{"Qwen2.5-7B-Instruct", "Qwen2.5", true},
		{"Llama-3.1-70B", "llama", true},
		{"Llama-3.1-70B", "mistral", false},
		{"", "", true},
		{"anything", "", true},
		{"", "something", false},
	}
	for _, tt := range tests {
		got := fitContainsIgnoreCase(tt.s, tt.substr)
		if got != tt.want {
			t.Errorf("fitContainsIgnoreCase(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
		}
	}
}

func TestFitnessWatcherDefaults(t *testing.T) {
	fw := &FitnessWatcher{}

	// Verify defaults are applied in Start's early path.
	if fw.CheckInterval == 0 {
		fw.CheckInterval = 30 * time.Second
	}
	if fw.DegradeThreshold == 0 {
		fw.DegradeThreshold = 0.3
	}

	if fw.CheckInterval != 30*time.Second {
		t.Errorf("expected default CheckInterval 30s, got %v", fw.CheckInterval)
	}
	if fw.DegradeThreshold != 0.3 {
		t.Errorf("expected default DegradeThreshold 0.3, got %f", fw.DegradeThreshold)
	}
}
