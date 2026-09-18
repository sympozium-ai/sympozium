package v1alpha1

import "testing"

// TestAgentRunPhaseIsTerminal pins the terminal phase set. Callers across the
// controller, the CLI and the web proxy all route through IsTerminal, so a new
// phase constant that belongs here must be added to IsTerminal, not to a
// caller's own comparison.
func TestAgentRunPhaseIsTerminal(t *testing.T) {
	cases := map[AgentRunPhase]bool{
		"":                            false,
		AgentRunPhasePending:          false,
		AgentRunPhaseRunning:          false,
		AgentRunPhaseServing:          false,
		AgentRunPhasePostRunning:      false,
		AgentRunPhaseAwaitingDelegate: false,
		AgentRunPhaseSucceeded:        true,
		AgentRunPhaseFailed:           true,
		AgentRunPhaseSkipped:          true,
	}
	for phase, want := range cases {
		if got := phase.IsTerminal(); got != want {
			t.Errorf("phase %q: IsTerminal()=%v, want %v", phase, got, want)
		}
	}
}
