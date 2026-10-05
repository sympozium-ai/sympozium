package controller

import (
	"context"
	"strings"
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnscoped"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A scoped run whose owning node is gone ends, instead of retrying as an
// uncertain outcome forever; other failures stay uncertain and are retried.
func TestScopedContextLossEndsTheRun(t *testing.T) {
	run := newTestRun()
	r := newAgentRunTestReconciler(t, parityAgent(), run)
	lost := &cellnscoped.HTTPError{Status: 409, Reason: "AUTH_CONTEXT_LOST"}
	if _, err := r.scopedUncertain(context.Background(), run, "ExecutionOutcomeUnconfirmed", lost); err != nil {
		t.Fatalf("context loss should end the run, not retry: %v", err)
	}
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed || !strings.Contains(stored.Status.Error, "AUTH_CONTEXT_LOST") {
		t.Fatalf("phase %q error %q", stored.Status.Phase, stored.Status.Error)
	}

	other := newTestRun()
	other.Name = "uncertain-run"
	r = newAgentRunTestReconciler(t, parityAgent(), other)
	if _, err := r.scopedUncertain(context.Background(), other, "ExecutionOutcomeUnconfirmed", &cellnscoped.HTTPError{Status: 502, Reason: "HOST_REQUEST_REFUSED"}); err == nil {
		t.Fatal("an uncertain outcome must be retried")
	}
}
