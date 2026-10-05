package controller

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// A run whose owner keeps reporting its context unavailable ends after the
// grace period; a blip, another reason or another condition state does not.
func TestScopedUncertainEndsTheRunAfterGrace(t *testing.T) {
	now := time.Now()
	run := func(status metav1.ConditionStatus, reason string, since time.Duration) *api.AgentRun {
		return &api.AgentRun{Status: api.AgentRunStatus{Conditions: []metav1.Condition{{
			Type: "CellnScopedExecution", Status: status, Reason: reason, LastTransitionTime: metav1.NewTime(now.Add(-since)),
		}}}}
	}
	for name, tc := range map[string]struct {
		run  *api.AgentRun
		ends bool
	}{
		"sustained":           {run(metav1.ConditionUnknown, "NativeOwnerUncertain", 3*time.Minute), true},
		"blip":                {run(metav1.ConditionUnknown, "NativeOwnerUncertain", 30*time.Second), false},
		"other uncertainty":   {run(metav1.ConditionUnknown, "ExecutionOutcomeUnconfirmed", 10*time.Minute), false},
		"running":             {run(metav1.ConditionTrue, "EnduringParentReady", 10*time.Minute), false},
		"no condition at all": {&api.AgentRun{}, false},
	} {
		if got := scopedUncertainExpired(tc.run, now); got != tc.ends {
			t.Errorf("%s: ends=%v, want %v", name, got, tc.ends)
		}
	}
}

func TestScopedEndedSummaryExposesOnlyMappedCauses(t *testing.T) {
	if got := scopedEndedSummary("turn handler failed; parent context lost: aggregate parent budget exhausted"); !strings.Contains(got, "AUTH_BUDGET_EXHAUSTED") || strings.Contains(got, "turn handler") {
		t.Fatalf("budget: %s", got)
	}
	if got := scopedEndedSummary("/var/lib/secret/path exploded at 0xdeadbeef"); strings.Contains(got, "deadbeef") || strings.Contains(got, "/var/lib") {
		t.Fatalf("raw native diagnostics leaked: %s", got)
	}
}
