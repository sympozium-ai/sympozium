package v1alpha1

import (
	"testing"
	"time"
)

func TestAgentConfig_ParseRunTimeout(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantNil   bool
		wantValue time.Duration
	}{
		{name: "empty returns nil", value: "", wantNil: true},
		{name: "invalid returns nil", value: "not-a-duration", wantNil: true},
		{name: "zero returns nil", value: "0s", wantNil: true},
		{name: "negative returns nil", value: "-5m", wantNil: true},
		{name: "minutes", value: "30m", wantValue: 30 * time.Minute},
		{name: "hours", value: "1h", wantValue: time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AgentConfig{RunTimeout: tt.value}.ParseRunTimeout()
			if tt.wantNil {
				if got != nil {
					t.Fatalf("ParseRunTimeout(%q) = %v, want nil", tt.value, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ParseRunTimeout(%q) = nil, want %s", tt.value, tt.wantValue)
			}
			if got.Duration != tt.wantValue {
				t.Fatalf("ParseRunTimeout(%q) = %s, want %s", tt.value, got.Duration, tt.wantValue)
			}
		})
	}
}
