package controller

import (
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
)

// resolveTaskModeAdjustments validates the AgentRun's spec.task field and
// computes the per-sidecar mutations requested by the resolved TaskModeHandler.
//
// Returns (nil, nil) for the trivial cases:
//   - agentRun.Spec.Task == nil (unset; string form by default)
//   - agentRun.Spec.Task is the string form (Path A — the LLM prompt,
//     no orchestration dispatch needed)
//
// For object-form tasks:
//   - Looks up the handler in the taskmodes registry by Mode().
//   - On unknown mode: returns an error naming the supported modes so the
//     reconcile loop can surface it on AgentRun.status.error.
//   - On handler validation failure: returns the handler's error.
//   - On success: returns the handler's per-sidecar adjustments.
//
// The function does not mutate any container/env state itself; the caller
// (buildJob → buildContainers) applies the adjustments during render.
//
// Note: ConfigureAgentContainer is intentionally NOT called here — that
// method mutates the agent container's env slice which is built inside
// buildContainers. buildContainers looks up the handler again (a cheap
// map access) and calls ConfigureAgentContainer directly. The two-step
// pattern keeps resolveTaskModeAdjustments pure (no side effects) and
// keeps buildContainers the single owner of container rendering.
func resolveTaskModeAdjustments(
	agentRun *sympoziumv1alpha1.AgentRun,
	sidecars []resolvedSidecar,
) ([]taskmodes.SidecarAdjustment, error) {
	task := agentRun.Spec.Task
	if task == nil || task.IsString() {
		return nil, nil
	}

	mode := task.GetMode()
	handler, ok := taskmodes.Get(mode)
	if !ok {
		return nil, fmt.Errorf("unknown task.mode %q; supported modes: %v", mode, taskmodes.SupportedModes())
	}

	if err := handler.Validate(task); err != nil {
		return nil, fmt.Errorf("task.mode %q validation failed: %w", mode, err)
	}

	contexts := make([]taskmodes.SidecarContext, 0, len(sidecars))
	for _, sc := range sidecars {
		contexts = append(contexts, taskmodes.SidecarContext{
			SkillPackName: sc.skillPackName,
			Sidecar:       sc.sidecar,
			Params:        sc.params,
		})
	}

	adjustments, err := handler.AdjustSidecars(task, contexts)
	if err != nil {
		return nil, fmt.Errorf("task.mode %q sidecar adjustment failed: %w", mode, err)
	}

	return adjustments, nil
}

// applyTaskModeToAgentContainer looks up the TaskModeHandler for the given
// object-form task and calls its ConfigureAgentContainer method to mutate
// the agent container's env in place. For string-form / nil task this is a
// no-op. Returns an error if the mode is unknown or the handler fails —
// buildContainers logs but continues, treating the failure as a soft error
// (the agent-runner will surface the misconfiguration at runtime).
//
// Validation is done by resolveTaskModeAdjustments earlier in the pipeline;
// by the time we get here the handler is known to be valid. The redundant
// lookup is intentional: agentEnv is local to buildContainers so this is
// the simplest place to apply the mutation.
func applyTaskModeToAgentContainer(task *sympoziumv1alpha1.TaskSpec, agentEnv *[]corev1.EnvVar) {
	if task == nil || task.IsString() {
		return
	}
	handler, ok := taskmodes.Get(task.GetMode())
	if !ok {
		// Should not happen — resolveTaskModeAdjustments already rejected
		// this. Log and continue for safety.
		slog.Warn("task-mode handler missing during agent container config",
			"mode", task.GetMode(),
			"supported", taskmodes.SupportedModes(),
		)
		return
	}
	if err := handler.ConfigureAgentContainer(task, agentEnv); err != nil {
		slog.Warn("task-mode ConfigureAgentContainer failed",
			"mode", task.GetMode(),
			"err", err,
		)
	}
}
