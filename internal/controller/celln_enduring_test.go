package controller

import (
	"context"
	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"testing"
)

func TestEnduringIntentCannotFallThroughToOneShotExecution(t *testing.T) {
	run := newTestCellnRun(t, "enduring-refusal", "enduring-refusal-uid")
	run.Spec.Celln = nil
	run.Spec.CellnSelection = &api.CellnCatalogueSelection{}
	run.Spec.ExecutionLifecycle = "enduring"
	run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 3600, MaxTurns: 10, MaxModelRequests: 20, MaxOutputTokens: 10240}
	r := newAgentRunTestReconciler(t, run)
	if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
		t.Fatal(err)
	}
	var current api.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != api.AgentRunPhaseFailed || current.Status.CellnActionID != "" || current.Status.JobName != "" || current.Status.DeploymentName != "" {
		t.Fatalf("enduring intent was not refused before execution: %+v", current.Status)
	}
}
