package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func tunedAgent() *sympoziumv1alpha1.Agent {
	return &sympoziumv1alpha1.Agent{
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Thinking:    "high",
					MaxTokens:   ptr.To[int32](12000),
					Temperature: "0.2",
				},
			},
		},
	}
}

// A run on the built-in agent-runner inherits the Agent's tuning regardless
// of how it was created — the reason this happens at reconcile time.
func TestInheritAgentModelTuning_AgentRunnerRunInherits(t *testing.T) {
	run := newTestRun()
	inheritAgentModelTuning(run, tunedAgent())

	m := run.Spec.Model
	if m.Thinking != "high" || m.MaxTokens == nil || *m.MaxTokens != 12000 || m.Temperature != "0.2" {
		t.Fatalf("model after inheritance = thinking %q maxTokens %v temperature %q, want high/12000/0.2",
			m.Thinking, m.MaxTokens, m.Temperature)
	}
}

func TestInheritAgentModelTuning_ExplicitRunValuesWin(t *testing.T) {
	run := newTestRun()
	run.Spec.Model.Thinking = "low"
	run.Spec.Model.MaxTokens = ptr.To[int32](500)
	run.Spec.Model.Temperature = "0.9"
	inheritAgentModelTuning(run, tunedAgent())

	m := run.Spec.Model
	if m.Thinking != "low" || *m.MaxTokens != 500 || m.Temperature != "0.9" {
		t.Fatalf("explicit run values were overwritten: thinking %q maxTokens %d temperature %q",
			m.Thinking, *m.MaxTokens, m.Temperature)
	}
}

// Harness adapters reject model.thinking/maxTokens/temperature
// (ValidateRunCompatibility), so a harness run must not pick them up
// implicitly from its Agent — that would break harness runs that work today.
func TestInheritAgentModelTuning_HarnessRunDoesNotInherit(t *testing.T) {
	run := harnessModeRun(nil)
	inheritAgentModelTuning(run, tunedAgent())

	m := run.Spec.Model
	if m.Thinking != "" || m.MaxTokens != nil || m.Temperature != "" {
		t.Fatalf("harness run inherited tuning: thinking %q maxTokens %v temperature %q", m.Thinking, m.MaxTokens, m.Temperature)
	}
}

func TestInheritAgentModelTuning_CellnRunDoesNotInherit(t *testing.T) {
	run := newTestRun()
	run.Spec.Backend = "celln"
	inheritAgentModelTuning(run, tunedAgent())

	m := run.Spec.Model
	if m.Thinking != "" || m.MaxTokens != nil || m.Temperature != "" {
		t.Fatalf("celln run inherited tuning: thinking %q maxTokens %v temperature %q", m.Thinking, m.MaxTokens, m.Temperature)
	}
}

// Inheritance must copy the pointer's value, not alias the Agent's field:
// the run and the Agent are separate objects.
func TestInheritAgentModelTuning_MaxTokensIsCopied(t *testing.T) {
	agent := tunedAgent()
	run := newTestRun()
	inheritAgentModelTuning(run, agent)
	*run.Spec.Model.MaxTokens = 1

	if *agent.Spec.Agents.Default.MaxTokens != 12000 {
		t.Fatalf("mutating the run's MaxTokens changed the Agent's to %d", *agent.Spec.Agents.Default.MaxTokens)
	}
}

func agentEnvValue(t *testing.T, cs []corev1.Container, name string) (string, bool) {
	t.Helper()
	agent := containerByName(cs, "agent")
	if agent == nil {
		t.Fatal("no agent container")
	}
	for _, e := range agent.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestBuildContainers_SamplingEnv(t *testing.T) {
	r := &AgentRunReconciler{}

	run := newTestRun()
	run.Spec.Model.Thinking = "medium"
	run.Spec.Model.MaxTokens = ptr.To[int32](12000)
	run.Spec.Model.Temperature = "0.2"
	containers, _, err := r.buildContainers(run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	for name, want := range map[string]string{"THINKING_MODE": "medium", "MAX_TOKENS": "12000", "TEMPERATURE": "0.2"} {
		if got, ok := agentEnvValue(t, containers, name); !ok || got != want {
			t.Errorf("%s = %q (present=%v), want %q", name, got, ok, want)
		}
	}

	// Unset fields must be absent, so the runner applies provider defaults
	// rather than parsing an empty or zero value.
	bare, _, err := r.buildContainers(newTestRun(), false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	for _, name := range []string{"MAX_TOKENS", "TEMPERATURE"} {
		if _, ok := agentEnvValue(t, bare, name); ok {
			t.Errorf("%s emitted for a run that does not set it", name)
		}
	}
}
