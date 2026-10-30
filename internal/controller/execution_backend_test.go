package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-logr/logr"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	refused := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		Phase: sympoziumv1alpha1.AgentRunPhaseFailed,
		Error: "AUTH_CAPACITY: native cell, memory or broker capacity is currently reserved.",
	}}
	if p := b.Phase(refused); p != ExecutionRefused {
		t.Fatalf("refusal phase = %s", p)
	}
	if res := b.Result(refused); res == nil || res.PublicReason != "AUTH_CAPACITY" {
		t.Fatalf("refusal result = %+v", res)
	}
	// The same code after a start attempt is a failure, not a refusal.
	refused.Status.CellnScoped = &sympoziumv1alpha1.CellnScopedStatus{StartAttempted: true}
	if p := b.Phase(refused); p != ExecutionFailed {
		t.Fatalf("post-start AUTH failure phase = %s, want Failed", p)
	}

	uncertain := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		Phase:      sympoziumv1alpha1.AgentRunPhaseRunning,
		Conditions: []metav1.Condition{{Type: "CellnScopedExecution", Status: metav1.ConditionUnknown, Reason: "ExecutionOutcomeUnconfirmed"}},
	}}
	if p := b.Phase(uncertain); p != ExecutionUncertain {
		t.Fatalf("uncertain phase = %s", p)
	}
	uncertain.Status.Conditions[0].Reason = "NativeOwnerUncertain"
	if p := b.Phase(uncertain); p != ExecutionContextLost {
		t.Fatalf("context-lost phase = %s", p)
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
