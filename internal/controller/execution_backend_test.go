package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestJobBackendOwnsOnlyPlainRuns(t *testing.T) {
	b := jobBackend{}
	cases := []struct {
		name string
		run  sympoziumv1alpha1.AgentRun
		want bool
	}{
		{"plain", sympoziumv1alpha1.AgentRun{}, true},
		{"celln backend", sympoziumv1alpha1.AgentRun{Spec: sympoziumv1alpha1.AgentRunSpec{Backend: "celln"}}, false},
		{"scoped binding", sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{CellnScoped: &sympoziumv1alpha1.CellnScopedStatus{}}}, false},
		{"sandbox", sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{SandboxName: "s"}}, false},
	}
	for _, c := range cases {
		got, err := b.Owns(context.Background(), &c.run)
		if err != nil || got != c.want {
			t.Fatalf("%s: Owns = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestJobBackendViews(t *testing.T) {
	b := jobBackend{}
	run := &sympoziumv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns", UID: "u"}}
	if p := b.Phase(run); p != ExecutionPending {
		t.Fatalf("empty phase = %s", p)
	}
	if b.Result(run) != nil {
		t.Fatal("non-terminal run has a result")
	}
	run.Status.Phase = sympoziumv1alpha1.AgentRunPhaseAwaitingGate
	if p := b.Phase(run); p != ExecutionRunning {
		t.Fatalf("gate phase = %s, want Running (gates sit above the seam)", p)
	}
	run.Status.Phase = sympoziumv1alpha1.AgentRunPhaseSucceeded
	run.Status.Result = "ok"
	run.Status.JobName = "r-job"
	if res := b.Result(run); res == nil || res.Outcome != ExecutionSucceeded || res.Output != "ok" || res.Receipt.Digest != "" {
		t.Fatalf("result = %+v", res)
	}
	id := b.Identity(run)
	if id.Backend != ExecutionBackendJob || id.Workload != "Job/r-job" || id.UID != "u" || id.Namespace != "ns" {
		t.Fatalf("identity = %+v", id)
	}
	if caps, _ := b.Capabilities(context.Background()); caps.State != BackendReady || caps.Turns {
		t.Fatalf("capabilities = %+v", caps)
	}
}

func TestCellnBackendTypedOutcomes(t *testing.T) {
	b := cellnBackend{}
	receipt := "sha256:" + strings.Repeat("ab", 32)
	capacity := "AUTH_CAPACITY: native cell, memory or broker capacity is currently reserved."
	refused := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		Phase:       sympoziumv1alpha1.AgentRunPhaseFailed,
		Error:       capacity,
		CellnScoped: &sympoziumv1alpha1.CellnScopedStatus{NativePhase: "Refused"},
	}}
	if p := b.Phase(refused); p != ExecutionRefused {
		t.Fatalf("refusal phase = %s", p)
	}
	if res := b.Result(refused); res == nil || res.Outcome != ExecutionRefused || res.PublicReason != "AUTH_CAPACITY" {
		t.Fatalf("refusal result = %+v", res)
	}
	// A receiver may refuse after it was asked to start: still Refused.
	refused.Status.CellnScoped.StartAttempted = true
	if p := b.Phase(refused); p != ExecutionRefused {
		t.Fatalf("post-start receiver refusal phase = %s, want Refused", p)
	}
	// The classification is the native phase, not the error text: an AUTH_
	// summary on a natively Failed run is a failure.
	failed := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		Phase:       sympoziumv1alpha1.AgentRunPhaseFailed,
		Error:       capacity,
		CellnScoped: &sympoziumv1alpha1.CellnScopedStatus{NativePhase: "Failed", StartAttempted: true},
	}}
	if p := b.Phase(failed); p != ExecutionFailed {
		t.Fatalf("native failure phase = %s, want Failed", p)
	}
	if res := b.Result(failed); res == nil || res.PublicReason != "" {
		t.Fatalf("native failure result = %+v", res)
	}

	// Shared admission: a model key refusal is typed, on Celln and Job alike.
	failedAt := metav1.Now()
	keyRefused := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		Phase:       sympoziumv1alpha1.AgentRunPhaseFailed,
		CompletedAt: &failedAt,
		Error:       `model key Secret "k" is not declared in Agent "a" spec.authRefs`,
		Conditions:  []metav1.Condition{{Type: conditionModelKeyAuthorized, Status: metav1.ConditionFalse, Reason: string(RefusalModelKeyNotGranted), LastTransitionTime: failedAt}},
	}}
	for _, backend := range []ExecutionBackend{b, jobBackend{}} {
		if p := backend.Phase(keyRefused); p != ExecutionRefused {
			t.Fatalf("%s key refusal phase = %s, want Refused", backend.Name(), p)
		}
		if res := backend.Result(keyRefused); res == nil || res.Outcome != ExecutionRefused || res.PublicReason != string(RefusalModelKeyNotGranted) {
			t.Fatalf("%s key refusal result = %+v", backend.Name(), res)
		}
	}

	uncertain := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		Phase:      sympoziumv1alpha1.AgentRunPhaseRunning,
		Conditions: []metav1.Condition{{Type: "CellnScopedExecution", Status: metav1.ConditionUnknown, Reason: "ExecutionOutcomeUnconfirmed"}},
	}}
	if p := b.Phase(uncertain); p != ExecutionUncertain {
		t.Fatalf("uncertain phase = %s", p)
	}
	// NativeOwnerUncertain is the grace period; it can still end as a
	// LeaseElapsed success, so it is Uncertain, never ContextLost.
	uncertain.Status.Conditions[0].Reason = "NativeOwnerUncertain"
	if p := b.Phase(uncertain); p != ExecutionUncertain {
		t.Fatalf("grace-period phase = %s, want Uncertain", p)
	}

	// A receiver-reported AUTH_CONTEXT_LOST ends the run through failRun and
	// marks it; cleanup rewriting CellnScopedExecution does not hide it.
	lost := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		Phase:       sympoziumv1alpha1.AgentRunPhaseFailed,
		CompletedAt: &failedAt,
		Error:       "Celln node holding this run is gone (AUTH_CONTEXT_LOST); no replacement execution is permitted. Start a new run.",
		Conditions: []metav1.Condition{
			{Type: conditionCellnContextLost, Status: metav1.ConditionTrue, Reason: "AUTH_CONTEXT_LOST", LastTransitionTime: failedAt},
			{Type: "CellnScopedExecution", Status: metav1.ConditionTrue, Reason: "CleanupConfirmed"},
		},
	}}
	if p := b.Phase(lost); p != ExecutionContextLost {
		t.Fatalf("context-lost phase = %s", p)
	}
	if res := b.Result(lost); res == nil || res.Outcome != ExecutionContextLost || res.PublicReason != "AUTH_CONTEXT_LOST" {
		t.Fatalf("context-lost result = %+v", res)
	}

	done := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		Phase:       sympoziumv1alpha1.AgentRunPhaseSucceeded,
		Result:      "answer",
		CellnScoped: &sympoziumv1alpha1.CellnScopedStatus{ReceiptDigest: receipt, PreparationName: "prep"},
	}}
	res := b.Result(done)
	if res == nil || res.Outcome != ExecutionSucceeded || res.Receipt.Digest != receipt {
		t.Fatalf("success result = %+v", res)
	}
	if id := b.Identity(done); id.Workload != "CellnPreparation/prep" {
		t.Fatalf("identity = %+v", id)
	}
	if _, err := b.Logs(context.Background(), logr.Discard(), done); !errors.Is(err, ErrLogsUnsupported) {
		t.Fatalf("logs err = %v", err)
	}
}

// A typed marker counts only for the Failed transition failRun wrote it
// with. One left from another transition (an older write, a hand edit, a
// crash in an earlier version that wrote it separately) never relabels a
// later, unrelated failure as Refused or ContextLost.
func TestStaleOutcomeMarkersAreIgnored(t *testing.T) {
	earlier := metav1.NewTime(metav1.Now().Add(-time.Minute))
	failedAt := metav1.Now()
	stale := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		Phase:       sympoziumv1alpha1.AgentRunPhaseFailed,
		CompletedAt: &failedAt,
		Error:       "Job failed",
		Conditions: []metav1.Condition{
			{Type: conditionModelKeyAuthorized, Status: metav1.ConditionFalse, Reason: string(RefusalModelKeyNotGranted), LastTransitionTime: earlier},
			{Type: conditionCellnContextLost, Status: metav1.ConditionTrue, Reason: "AUTH_CONTEXT_LOST", LastTransitionTime: earlier},
		},
	}}
	for _, backend := range []ExecutionBackend{jobBackend{}, cellnBackend{}} {
		if p := backend.Phase(stale); p != ExecutionFailed {
			t.Fatalf("%s: stale markers read as %s, want Failed", backend.Name(), p)
		}
		if res := backend.Result(stale); res == nil || res.PublicReason != "" {
			t.Fatalf("%s: stale markers leaked into the result: %+v", backend.Name(), res)
		}
	}
	// A marker on a run that is not Failed is never read either.
	stale.Status.Phase = sympoziumv1alpha1.AgentRunPhaseSucceeded
	stale.Status.Conditions[0].LastTransitionTime = failedAt
	if p := (jobBackend{}).Phase(stale); p != ExecutionSucceeded {
		t.Fatalf("marker on a succeeded run read as %s", p)
	}
}

// failRun writes a mark and the Failed phase in one status update, so there
// is no window in which one exists without the other.
func TestFailRunWritesMarkWithTheTransition(t *testing.T) {
	run := newTestRun()
	r := newAgentRunTestReconciler(t, parityAgent(), run)
	if err := r.refuseRun(context.Background(), run, &Refusal{Code: RefusalModelKeyConflict, Message: "refused"}); err != nil {
		t.Fatal(err)
	}
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatal(err)
	}
	c := meta.FindStatusCondition(stored.Status.Conditions, conditionModelKeyAuthorized)
	if stored.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || c == nil || stored.Status.CompletedAt == nil || !c.LastTransitionTime.Equal(stored.Status.CompletedAt) {
		t.Fatalf("phase %s completedAt %v condition %+v", stored.Status.Phase, stored.Status.CompletedAt, c)
	}
	if res := (jobBackend{}).Result(&stored); res == nil || res.Outcome != ExecutionRefused || res.PublicReason != string(RefusalModelKeyConflict) {
		t.Fatalf("result = %+v", res)
	}
}
