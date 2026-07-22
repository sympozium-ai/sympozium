package main

import (
	"strings"
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func convRun(instance, task string, phase sympoziumv1alpha1.AgentRunPhase, result, errMsg string) sympoziumv1alpha1.AgentRun {
	return sympoziumv1alpha1.AgentRun{
		Spec: sympoziumv1alpha1.AgentRunSpec{AgentRef: instance, Task: sympoziumv1alpha1.NewStringTask(task)},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:  phase,
			Result: result,
			Error:  errMsg,
		},
	}
}

func TestBuildConversationContext_RendersPhases(t *testing.T) {
	const instance = "demo"
	// m.runs is newest-first; buildConversationContext walks it oldest-first.
	m := tuiModel{runs: []sympoziumv1alpha1.AgentRun{
		convRun(instance, "skip with reason", sympoziumv1alpha1.AgentRunPhaseSkipped, "no new items in queue", ""),
		convRun(instance, "skip no reason", sympoziumv1alpha1.AgentRunPhaseSkipped, "", ""),
		convRun(instance, "failed task", sympoziumv1alpha1.AgentRunPhaseFailed, "", "boom"),
		convRun(instance, "ok task", sympoziumv1alpha1.AgentRunPhaseSucceeded, "all done", ""),
		// A run for a different instance must be excluded.
		convRun("other", "ignored", sympoziumv1alpha1.AgentRunPhaseSucceeded, "nope", ""),
	}}

	got := m.buildConversationContext(instance)

	for _, want := range []string{
		"Assistant: all done",
		"Assistant: [error: boom]",
		"Assistant: [skipped: no new items in queue]",
		"Assistant: [skipped: no work to do]", // default reason when Result empty
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected transcript to contain %q\n--- got ---\n%s", want, got)
		}
	}

	// A skipped run must never be rendered as pending.
	if strings.Contains(got, "[pending]") {
		t.Errorf("did not expect any [pending] entry\n--- got ---\n%s", got)
	}
	// The other-instance run must not leak in.
	if strings.Contains(got, "ignored") || strings.Contains(got, "nope") {
		t.Errorf("transcript leaked a run from another instance\n--- got ---\n%s", got)
	}
}
