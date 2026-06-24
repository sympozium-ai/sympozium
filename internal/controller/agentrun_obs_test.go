package controller

import "testing"

// TestClassifyFailureReason verifies the free-form failure strings produced
// across the controller (failRun callers) bucket into the intended bounded
// reason codes for the sympozium.agent.runs{reason} label (ISI-1406 gap 1).
func TestClassifyFailureReason(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "unknown"},
		{"timeout", "timeout"},
		{"context deadline exceeded", "timeout"},
		{"agent container terminated with error (OOMKilled)", "oom"},
		{"out of memory", "oom"},
		{"policy validation failed: tool denied", "policy"},
		{"gate hook validation failed", "policy"},
		{"token budget exceeded: 1000 > 500", "token_budget"},
		{`model "qwen3.6" not found`, "model_unavailable"},
		{`model "qwen3.6" is not ready (phase: Pending)`, "model_unavailable"},
		{"delegate child run failed", "delegate_failed"},
		{"upstream LLM rate limit", "llm_error"},
		{"context length exceeded", "llm_error"},
		{"Job not found", "infra"},
		{"failed to create pod", "infra"},
		{"something totally unexpected", "other"},
	}
	for _, c := range cases {
		if got := classifyFailureReason(c.in); got != c.want {
			t.Errorf("classifyFailureReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
