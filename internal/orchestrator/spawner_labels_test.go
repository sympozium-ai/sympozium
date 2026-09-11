package orchestrator

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// k8sLabelValueRE mirrors the Kubernetes label value validation rule.
var k8sLabelValueRE = regexp.MustCompile(`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$`)

func spawnerFixture(t *testing.T) *Spawner {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return &Spawner{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Log:    logf.Log,
	}
}

// Regression guard: a batch ID containing characters invalid in a Kubernetes
// label (e.g. a slash), or longer than the 63-char label limit, must not
// break child creation. The label gets a sanitized, bounded token; the full
// original ID is preserved in an annotation.
func TestSpawn_BatchIDWithSlashDoesNotBreakLabel(t *testing.T) {
	s := spawnerFixture(t)
	batchID := "team/nightly-run/2026-09-09"

	result, err := s.Spawn(context.Background(), SpawnRequest{
		ParentRunName: "parent",
		InstanceName:  "team-lead",
		Namespace:     "default",
		ChildIndex:    1,
		BatchID:       batchID,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	var run sympoziumv1alpha1.AgentRun
	if err := s.Client.Get(context.Background(), types.NamespacedName{Name: result.RunName, Namespace: "default"}, &run); err != nil {
		t.Fatalf("get child: %v", err)
	}

	label := run.Labels["sympozium.ai/subagent-batch-id"]
	if label == "" {
		t.Fatal("expected a sanitized batch-id label")
	}
	if !k8sLabelValueRE.MatchString(label) {
		t.Errorf("label value %q is not a valid Kubernetes label", label)
	}
	if strings.Contains(label, "/") {
		t.Errorf("label value %q still contains a slash", label)
	}

	if got := run.Annotations["sympozium.ai/subagent-batch-id-full"]; got != batchID {
		t.Errorf("annotation = %q, want full batch id %q", got, batchID)
	}
}

func TestSpawn_LongBatchIDIsBoundedInLabel(t *testing.T) {
	s := spawnerFixture(t)
	batchID := strings.Repeat("a", 200)

	result, err := s.Spawn(context.Background(), SpawnRequest{
		ParentRunName: "parent",
		InstanceName:  "team-lead",
		Namespace:     "default",
		ChildIndex:    1,
		BatchID:       batchID,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	var run sympoziumv1alpha1.AgentRun
	if err := s.Client.Get(context.Background(), types.NamespacedName{Name: result.RunName, Namespace: "default"}, &run); err != nil {
		t.Fatalf("get child: %v", err)
	}

	label := run.Labels["sympozium.ai/subagent-batch-id"]
	if len(label) > 63 {
		t.Errorf("label length = %d, want <= 63", len(label))
	}
	if !k8sLabelValueRE.MatchString(label) {
		t.Errorf("label value %q is not a valid Kubernetes label", label)
	}

	if got := run.Annotations["sympozium.ai/subagent-batch-id-full"]; got != batchID {
		t.Errorf("annotation length = %d, want full %d-char batch id preserved", len(got), len(batchID))
	}
}

func TestSpawn_EmptyBatchIDSetsNoLabelOrAnnotation(t *testing.T) {
	s := spawnerFixture(t)

	result, err := s.Spawn(context.Background(), SpawnRequest{
		ParentRunName: "parent",
		InstanceName:  "team-lead",
		Namespace:     "default",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	var run sympoziumv1alpha1.AgentRun
	if err := s.Client.Get(context.Background(), types.NamespacedName{Name: result.RunName, Namespace: "default"}, &run); err != nil {
		t.Fatalf("get child: %v", err)
	}

	if _, ok := run.Labels["sympozium.ai/subagent-batch-id"]; ok {
		t.Error("expected no batch-id label for a non-batch spawn")
	}
	if _, ok := run.Annotations["sympozium.ai/subagent-batch-id-full"]; ok {
		t.Error("expected no batch-id annotation for a non-batch spawn")
	}
}
