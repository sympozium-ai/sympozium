package controller

import (
	"strings"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/agentexecution"
)

// applyAgentExecutionDefaults copies Agent.spec.execution onto a newly built run
// when the builder left those fields empty. Unsupported combinations return an error
// instead of silently switching to Kubernetes or dropping tools. Already-live native
// parents are never mutated here; this only affects newly created AgentRun objects.
func applyAgentExecutionDefaults(agent *sympoziumv1alpha1.Agent, run *sympoziumv1alpha1.AgentRun) error {
	if agent == nil || run == nil || agent.Spec.Execution == nil {
		return nil
	}
	resolved, err := agentexecution.Resolve(agent, agentexecution.Input{
		Backend:            run.Spec.Backend,
		ExecutionLifecycle: run.Spec.ExecutionLifecycle,
		Enduring:           run.Spec.Enduring,
		CellnSelection:     run.Spec.CellnSelection,
	})
	if err != nil {
		return err
	}
	agentexecution.Apply(&run.Spec, resolved)
	if resolved.CellnSelection != nil {
		run.Spec.Model = sympoziumv1alpha1.ModelSpec{Provider: resolved.Provider, Model: resolved.Model}
	}
	if len(resolved.Inherited) > 0 {
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		run.Annotations["sympozium.ai/execution-inherited"] = strings.Join(resolved.Inherited, ",")
	}
	return nil
}
