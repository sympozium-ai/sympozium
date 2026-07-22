package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func stimulusTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}
	return scheme
}

func TestStimulusFiresOnReady(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
		want    bool
	}{
		// Ensembles authored before the field existed have no trigger set and
		// must keep firing on readiness.
		{"empty trigger keeps the pre-existing auto-fire behaviour", "", true},
		{"explicit onReady fires", sympoziumv1alpha1.StimulusTriggerOnReady, true},
		{"manual does not fire on readiness", sympoziumv1alpha1.StimulusTriggerManual, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &sympoziumv1alpha1.StimulusSpec{Name: "brief", Prompt: "go", Trigger: tt.trigger}
			if got := s.FiresOnReady(); got != tt.want {
				t.Errorf("FiresOnReady() with trigger %q = %v, want %v", tt.trigger, got, tt.want)
			}
		})
	}
}

func TestScheduleWaitsForFirstInterval(t *testing.T) {
	tests := []struct {
		name      string
		firstTick string
		want      bool
	}{
		{"empty firstTick keeps the immediate first run", "", false},
		{"explicit immediate fires at once", sympoziumv1alpha1.ScheduleFirstTickImmediate, false},
		{"afterInterval defers the first run", sympoziumv1alpha1.ScheduleFirstTickAfterInterval, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &sympoziumv1alpha1.SympoziumScheduleSpec{FirstTick: tt.firstTick}
			if got := s.WaitsForFirstInterval(); got != tt.want {
				t.Errorf("WaitsForFirstInterval() with firstTick %q = %v, want %v", tt.firstTick, got, tt.want)
			}
		})
	}
}

func TestResolveStimulusTarget(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "lead", Target: "researcher", Type: "delegation"},
				{Source: "brief", Target: "lead", Type: "stimulus"},
			},
		},
	}
	got, err := ResolveStimulusTarget(pack)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "lead" {
		t.Errorf("ResolveStimulusTarget() = %q, want %q", got, "lead")
	}

	none := &sympoziumv1alpha1.Ensemble{
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "lead", Target: "researcher", Type: "delegation"},
			},
		},
	}
	if _, err := ResolveStimulusTarget(none); err == nil {
		t.Error("expected an error when no stimulus edge exists")
	}
}

// BuildStimulusRun is the shared path for both the readiness edge and the
// manual trigger API. The API server used to build its own run and had drifted:
// it defaulted the provider to "openai" and dropped the ToolPolicy, so a
// manually triggered agent ran with tools its agent config had denied.
func TestBuildStimulusRunCarriesToolPolicyAndResolvedProvider(t *testing.T) {
	scheme := stimulusTestScheme(t)

	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "research", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Stimulus: &sympoziumv1alpha1.StimulusSpec{Name: "brief", Prompt: "research pharaohs"},
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "brief", Target: "lead", Type: "stimulus"},
			},
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{
					Name: "lead",
					ToolPolicy: &sympoziumv1alpha1.AgentConfigToolPolicy{
						Deny: []string{"write_file"},
					},
				},
			},
		},
	}

	inst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "research-lead",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/ensemble":     "research",
				"sympozium.ai/agent-config": "lead",
			},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "anthropic", Secret: "claude-key"},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pack, inst).Build()

	run := BuildStimulusRun(context.Background(), c, pack, inst, "lead",
		StimulusTriggerSourceManual, time.Now())

	if run.Spec.ToolPolicy == nil {
		t.Fatal("stimulus run dropped the agent config's ToolPolicy")
	}
	if len(run.Spec.ToolPolicy.Deny) != 1 || run.Spec.ToolPolicy.Deny[0] != "write_file" {
		t.Errorf("ToolPolicy.Deny = %v, want [write_file]", run.Spec.ToolPolicy.Deny)
	}
	if run.Spec.Model.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q (resolved from the agent, not defaulted)",
			run.Spec.Model.Provider, "anthropic")
	}
	if run.Spec.Model.AuthSecretRef != "claude-key" {
		t.Errorf("AuthSecretRef = %q, want %q", run.Spec.Model.AuthSecretRef, "claude-key")
	}
	if run.Spec.Task.GetPrompt() != "research pharaohs" {
		t.Errorf("Task = %q, want the stimulus prompt", run.Spec.Task.GetPrompt())
	}
	if run.Labels["sympozium.ai/trigger-source"] != StimulusTriggerSourceManual {
		t.Errorf("trigger-source = %q, want %q",
			run.Labels["sympozium.ai/trigger-source"], StimulusTriggerSourceManual)
	}
	if run.Labels["sympozium.ai/stimulus"] != "true" {
		t.Error("stimulus runs must be labelled so the schedule controller can defer to them")
	}
}
