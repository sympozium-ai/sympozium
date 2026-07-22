package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// delegationFixtures returns a succeeded source run plus the Agent instances and
// Ensemble wiring needed for the controller-side delegation executor to spawn a
// child for persona "architect" -> "reviewer" over a delegation edge.
func delegationFixtures() (*sympoziumv1alpha1.AgentRun, []client.Object) {
	sourceInst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "team-architect",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/agent-config": "architect",
				"sympozium.ai/ensemble":     "team",
			},
		},
	}
	targetInst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "team-reviewer",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/agent-config": "reviewer",
				"sympozium.ai/ensemble":     "team",
			},
		},
	}
	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "architect", Target: "reviewer", Type: "delegation"},
			},
		},
	}
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "src-run", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentRunSpec{AgentRef: "team-architect", Task: sympoziumv1alpha1.NewStringTask("design it")},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:  sympoziumv1alpha1.AgentRunPhaseSucceeded,
			Result: "design complete",
		},
	}
	return run, []client.Object{sourceInst, targetInst, ensemble, run}
}

// listChildRuns returns AgentRuns spawned for the reviewer target.
func listChildRuns(t *testing.T, r *AgentRunReconciler) []sympoziumv1alpha1.AgentRun {
	t.Helper()
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(context.Background(), &runs, client.InNamespace("default")); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	var children []sympoziumv1alpha1.AgentRun
	for _, ar := range runs.Items {
		if ar.Labels["sympozium.ai/delegation-from"] != "" {
			children = append(children, ar)
		}
	}
	return children
}

// TestTriggerDelegationSuccessors_DefaultOff is the core default-off guarantee:
// with the flag unset, the executor is a no-op and spawns no child runs.
func TestTriggerDelegationSuccessors_DefaultOff(t *testing.T) {
	run, objs := delegationFixtures()
	r := newAgentRunTestReconciler(t, objs...)
	// DelegationControllerExecutor left at its zero value (false).

	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	if got := listChildRuns(t, r); len(got) != 0 {
		t.Fatalf("expected no child runs when flag off, got %d", len(got))
	}
	// Idempotency marker must not be set when nothing was triggered.
	if run.Labels["sympozium.ai/delegation-triggered"] == "true" {
		t.Fatal("delegation-triggered marker set despite flag off")
	}
}

// TestTriggerDelegationSuccessors_EnabledSpawnsChild verifies the executor
// spawns the target persona's run when enabled, and marks the parent idempotent.
func TestTriggerDelegationSuccessors_EnabledSpawnsChild(t *testing.T) {
	run, objs := delegationFixtures()
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	children := listChildRuns(t, r)
	if len(children) != 1 {
		t.Fatalf("expected 1 child run, got %d", len(children))
	}
	child := children[0]
	if child.Spec.AgentRef != "team-reviewer" {
		t.Fatalf("child AgentRef = %q, want team-reviewer", child.Spec.AgentRef)
	}
	if child.Spec.AgentID != "delegation-from-architect" {
		t.Fatalf("child AgentID = %q, want delegation-from-architect", child.Spec.AgentID)
	}
	if run.Labels["sympozium.ai/delegation-triggered"] != "true" {
		t.Fatal("parent not marked delegation-triggered after spawn")
	}
}

// TestTriggerDelegationSuccessors_Idempotent verifies a second call (e.g. on
// re-reconcile) does not double-spawn once the marker is set.
func TestTriggerDelegationSuccessors_Idempotent(t *testing.T) {
	run, objs := delegationFixtures()
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	for i := 0; i < 2; i++ {
		if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := listChildRuns(t, r); len(got) != 1 {
		t.Fatalf("expected exactly 1 child after 2 calls, got %d", len(got))
	}
}

// TestTriggerDelegationSuccessors_SkipsAlreadyDelegated verifies the executor
// stays a pure fallback: if the model already delegated to the target at
// runtime (recorded in Status.Delegates), no duplicate child is spawned.
func TestTriggerDelegationSuccessors_SkipsAlreadyDelegated(t *testing.T) {
	run, objs := delegationFixtures()
	run.Status.Delegates = []sympoziumv1alpha1.DelegateStatus{
		{ChildRunName: "tool-spawned-child", TargetPersona: "reviewer"},
	}
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	if _, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	if got := listChildRuns(t, r); len(got) != 0 {
		t.Fatalf("expected no controller-spawned child when model already delegated, got %d", len(got))
	}
}

// TestTriggerDelegationSuccessors_InflightCapRequeues covers the maintainer
// finding on PR #256: when the ensemble is at its in-flight cap the executor
// must NOT silently drop the successor. It returns a non-zero requeueAfter (so
// reconcileCompleted requeues with backoff) and does not spawn a child or set
// the idempotency marker, so a later retry can still fire once capacity frees.
func TestTriggerDelegationSuccessors_InflightCapRequeues(t *testing.T) {
	run, objs := delegationFixtures()
	// Saturate the ensemble: three running runs at the default cap of 3.
	for i := 0; i < 3; i++ {
		objs = append(objs, &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "busy-" + string(rune('a'+i)),
				Namespace: "default",
				Labels:    map[string]string{"sympozium.ai/ensemble": "team"},
			},
			Status: sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseRunning},
		})
	}
	r := newAgentRunTestReconciler(t, objs...)
	r.DelegationControllerExecutor = true

	requeueAfter, err := r.triggerDelegationSuccessors(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("triggerDelegationSuccessors: %v", err)
	}
	if requeueAfter <= 0 {
		t.Fatalf("expected a positive requeueAfter when at in-flight cap, got %v", requeueAfter)
	}
	if got := listChildRuns(t, r); len(got) != 0 {
		t.Fatalf("expected no child spawned at in-flight cap, got %d", len(got))
	}
	// Marker must stay unset so the requeued reconcile can re-evaluate and fire.
	if run.Labels["sympozium.ai/delegation-triggered"] == "true" {
		t.Fatal("marker set at in-flight cap — would prevent the retry from ever firing")
	}
}

func TestDelegationEdgeActive(t *testing.T) {
	cases := []struct {
		condition string
		want      bool
	}{
		{"", true},
		{"on explicit request", true},
		{"when source run succeeds", true},
		{"when source fails", false},
		{"On Error", false},
		{"if review unsuccessful", false},
		{"when rejected by reviewer", false},
	}
	for _, c := range cases {
		if got := delegationEdgeActive(c.condition); got != c.want {
			t.Errorf("delegationEdgeActive(%q) = %v, want %v", c.condition, got, c.want)
		}
	}
}
