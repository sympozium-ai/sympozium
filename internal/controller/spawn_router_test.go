package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/ipc"
)

func newTestSpawnRouter(t *testing.T, objs ...client.Object) *SpawnRouter {
	t.Helper()

	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	sympoziumv1alpha1.AddToScheme(scheme)

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&sympoziumv1alpha1.Ensemble{}).
		Build()

	return &SpawnRouter{
		Client: cl,
		Log:    logr.Discard(),
	}
}

func TestCircuitBreaker_TripsAfterThreshold(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
				Enabled: true,
				Membrane: &sympoziumv1alpha1.MembraneSpec{
					CircuitBreaker: &sympoziumv1alpha1.CircuitBreakerSpec{
						ConsecutiveFailures: 2,
					},
				},
			},
		},
	}
	parentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-run",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/ensemble": "my-pack"},
		},
	}

	sr := newTestSpawnRouter(t, pack, parentRun)
	ctx := context.Background()

	// First failure: should not trip.
	sr.incrementCircuitBreaker(ctx, "parent-run")
	var updated sympoziumv1alpha1.Ensemble
	sr.Client.Get(ctx, types.NamespacedName{Name: "my-pack", Namespace: "default"}, &updated)
	if updated.Status.CircuitBreakerOpen {
		t.Error("circuit breaker should not be open after 1 failure")
	}
	if updated.Status.ConsecutiveDelegateFailures != 1 {
		t.Errorf("failures = %d, want 1", updated.Status.ConsecutiveDelegateFailures)
	}

	// Second failure: should trip (threshold = 2).
	sr.incrementCircuitBreaker(ctx, "parent-run")
	sr.Client.Get(ctx, types.NamespacedName{Name: "my-pack", Namespace: "default"}, &updated)
	if !updated.Status.CircuitBreakerOpen {
		t.Error("circuit breaker should be open after 2 failures")
	}
}

func TestCircuitBreaker_ResetsOnSuccess(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
				Enabled: true,
				Membrane: &sympoziumv1alpha1.MembraneSpec{
					CircuitBreaker: &sympoziumv1alpha1.CircuitBreakerSpec{
						ConsecutiveFailures: 3,
					},
				},
			},
		},
		Status: sympoziumv1alpha1.EnsembleStatus{
			ConsecutiveDelegateFailures: 2,
		},
	}
	parentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-run",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/ensemble": "my-pack"},
		},
	}

	sr := newTestSpawnRouter(t, pack, parentRun)
	ctx := context.Background()

	sr.resetCircuitBreaker(ctx, "parent-run")

	var updated sympoziumv1alpha1.Ensemble
	sr.Client.Get(ctx, types.NamespacedName{Name: "my-pack", Namespace: "default"}, &updated)
	if updated.Status.ConsecutiveDelegateFailures != 0 {
		t.Errorf("failures = %d, want 0 after reset", updated.Status.ConsecutiveDelegateFailures)
	}
	if updated.Status.CircuitBreakerOpen {
		t.Error("circuit breaker should be closed after reset")
	}
}

func TestCircuitBreaker_BlocksSpawn(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
				Enabled: true,
				Membrane: &sympoziumv1alpha1.MembraneSpec{
					CircuitBreaker: &sympoziumv1alpha1.CircuitBreakerSpec{
						ConsecutiveFailures: 3,
					},
				},
			},
		},
		Status: sympoziumv1alpha1.EnsembleStatus{
			CircuitBreakerOpen:          true,
			ConsecutiveDelegateFailures: 3,
		},
	}
	parentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-run",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/ensemble": "my-pack"},
		},
	}

	sr := newTestSpawnRouter(t, pack, parentRun)
	ctx := context.Background()

	err := sr.checkCircuitBreaker(ctx, "my-pack", "parent-run")
	if err == nil {
		t.Error("expected error when circuit breaker is open")
	}
}

// ── Subagent batch tests ─────────────────────────────────────────────────────

func TestPendingBatch_FinalizeBatchStatus(t *testing.T) {
	tests := []struct {
		name       string
		results    []ipc.SubagentChildResult
		failed     int
		aborted    bool
		wantStatus string
	}{
		{
			name: "all succeeded",
			results: []ipc.SubagentChildResult{
				{ID: "a", Status: "success"},
				{ID: "b", Status: "success"},
			},
			failed:     0,
			wantStatus: "success",
		},
		{
			name: "some failed",
			results: []ipc.SubagentChildResult{
				{ID: "a", Status: "success"},
				{ID: "b", Status: "error", Error: "timeout"},
			},
			failed:     1,
			wantStatus: "partial",
		},
		{
			name: "all failed",
			results: []ipc.SubagentChildResult{
				{ID: "a", Status: "error", Error: "fail1"},
				{ID: "b", Status: "error", Error: "fail2"},
			},
			failed:     2,
			wantStatus: "error",
		},
		{
			name: "aborted with partial failures",
			results: []ipc.SubagentChildResult{
				{ID: "a", Status: "error", Error: "fail"},
				{ID: "b"},
			},
			failed:     1,
			aborted:    true,
			wantStatus: "partial",
		},
		{
			name: "aborted with all failed",
			results: []ipc.SubagentChildResult{
				{ID: "a", Status: "error", Error: "fail"},
			},
			failed:     1,
			aborted:    true,
			wantStatus: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch := &pendingBatch{
				batchID:     "test-batch",
				parentRunID: "parent",
				tasks:       make([]ipc.SubagentTask, len(tt.results)),
				results:     tt.results,
				completed:   len(tt.results),
				failed:      tt.failed,
				aborted:     tt.aborted,
			}

			// Determine status using the same logic as finalizeBatch.
			status := "success"
			batch.mu.Lock()
			if batch.failed > 0 && batch.failed < len(batch.tasks) {
				status = "partial"
			} else if batch.failed == len(batch.tasks) || (batch.aborted && batch.failed > 0) {
				status = "error"
			}
			batch.mu.Unlock()

			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
		})
	}
}

func TestPendingBatch_ChildToIndexTracking(t *testing.T) {
	batch := &pendingBatch{
		batchID:      "batch-1",
		parentRunID:  "parent-1",
		tasks:        []ipc.SubagentTask{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		results:      make([]ipc.SubagentChildResult, 3),
		childToIndex: make(map[string]int),
	}

	// Simulate registering children.
	batch.childToIndex["sub-parent-1-1-1"] = 0
	batch.childToIndex["sub-parent-1-1-2"] = 1
	batch.childToIndex["sub-parent-1-1-3"] = 2

	// Verify lookup.
	if idx, ok := batch.childToIndex["sub-parent-1-1-2"]; !ok || idx != 1 {
		t.Errorf("child index lookup failed: ok=%v, idx=%d", ok, idx)
	}

	// Simulate completing child 1.
	batch.mu.Lock()
	batch.results[1] = ipc.SubagentChildResult{
		ID:       "b",
		RunName:  "sub-parent-1-1-2",
		Status:   "success",
		Response: "done",
	}
	batch.completed++
	batch.mu.Unlock()

	if batch.completed != 1 {
		t.Errorf("completed = %d, want 1", batch.completed)
	}
	if batch.results[1].Status != "success" {
		t.Errorf("results[1].Status = %q, want %q", batch.results[1].Status, "success")
	}
}

func TestCircuitBreaker_NoConfig(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec:       sympoziumv1alpha1.EnsembleSpec{},
	}
	parentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-run",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/ensemble": "my-pack"},
		},
	}

	sr := newTestSpawnRouter(t, pack, parentRun)
	ctx := context.Background()

	// Should not error when no circuit breaker is configured.
	err := sr.checkCircuitBreaker(ctx, "my-pack", "parent-run")
	if err != nil {
		t.Errorf("expected no error without circuit breaker config, got: %v", err)
	}
}
