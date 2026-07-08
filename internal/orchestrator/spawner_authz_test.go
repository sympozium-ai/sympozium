package orchestrator

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ensembleFixture builds an Ensemble with two installed personas (lead, worker)
// and the supplied relationships, plus a spawner backed by a fake client.
func ensembleFixture(t *testing.T, rels []sympoziumv1alpha1.AgentConfigRelationship) *Spawner {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: v1.ObjectMeta{Name: "team", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "lead", SystemPrompt: "lead"},
				{Name: "worker", SystemPrompt: "worker"},
			},
			Relationships: rels,
		},
		Status: sympoziumv1alpha1.EnsembleStatus{
			InstalledAgentConfigs: []sympoziumv1alpha1.InstalledAgentConfig{
				{Name: "lead", InstanceName: "team-lead"},
				{Name: "worker", InstanceName: "team-worker"},
			},
		},
	}
	return &Spawner{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(pack).Build(),
		Log:    logf.Log,
	}
}

func TestResolvePersonaTarget_AllowsConfiguredEdge(t *testing.T) {
	s := ensembleFixture(t, []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "lead", Target: "worker", Type: "delegation"},
	})
	_, err := s.resolvePersonaTarget(context.Background(), SpawnRequest{
		Namespace:     "default",
		InstanceName:  "team-lead",
		PackName:      "team",
		TargetPersona: "worker",
	})
	if err != nil {
		t.Fatalf("expected delegation with a configured edge to be allowed, got: %v", err)
	}
}

func TestResolvePersonaTarget_DeniesWithoutEdge(t *testing.T) {
	// lead → worker exists, but worker → lead does not.
	s := ensembleFixture(t, []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "lead", Target: "worker", Type: "delegation"},
	})
	_, err := s.resolvePersonaTarget(context.Background(), SpawnRequest{
		Namespace:     "default",
		InstanceName:  "team-worker",
		PackName:      "team",
		TargetPersona: "lead",
	})
	if err == nil {
		t.Fatal("expected denial when no edge connects source to target")
	}
}

func TestResolvePersonaTarget_DeniesZeroRelationships(t *testing.T) {
	// Regression: an Ensemble with no relationships previously permitted
	// any-to-any delegation. It must now deny.
	s := ensembleFixture(t, nil)
	_, err := s.resolvePersonaTarget(context.Background(), SpawnRequest{
		Namespace:     "default",
		InstanceName:  "team-lead",
		PackName:      "team",
		TargetPersona: "worker",
	})
	if err == nil {
		t.Fatal("expected denial when the ensemble declares no relationships")
	}
}

func TestResolvePersonaTarget_DeniesForeignEnsemble(t *testing.T) {
	// Regression: the parent instance ("intruder-x") is not a member of the
	// named ensemble, so its source persona resolves to "". Previously the edge
	// check was skipped entirely and the delegation was allowed.
	s := ensembleFixture(t, []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "lead", Target: "worker", Type: "delegation"},
	})
	_, err := s.resolvePersonaTarget(context.Background(), SpawnRequest{
		Namespace:     "default",
		InstanceName:  "intruder-x",
		PackName:      "team",
		TargetPersona: "worker",
	})
	if err == nil {
		t.Fatal("expected denial when the parent is not a member of the ensemble")
	}
	if !strings.Contains(err.Error(), "not a member") {
		t.Errorf("expected a membership-denial error, got: %v", err)
	}
}
