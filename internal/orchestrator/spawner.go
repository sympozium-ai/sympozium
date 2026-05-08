// Package orchestrator handles building agent pods and spawning sub-agents.
package orchestrator

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

var spawnerTracer = otel.Tracer("sympozium.ai/spawner")

// Spawner handles sub-agent spawn requests by creating AgentRun CRs.
type Spawner struct {
	Client client.Client
	Log    logr.Logger
}

// SpawnRequest represents a request from a parent agent to spawn a sub-agent.
type SpawnRequest struct {
	// ParentRunName is the name of the parent AgentRun.
	ParentRunName string `json:"parentRunName"`

	// ParentSessionKey is the session key of the parent.
	ParentSessionKey string `json:"parentSessionKey"`

	// InstanceName is the Agent this belongs to.
	InstanceName string `json:"instanceName"`

	// Namespace is the Kubernetes namespace.
	Namespace string `json:"namespace"`

	// Task is the task for the sub-agent.
	Task string `json:"task"`

	// SystemPrompt is the system prompt for the sub-agent.
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// AgentID is the agent configuration to use.
	AgentID string `json:"agentId"`

	// CurrentDepth is the current spawn depth.
	CurrentDepth int `json:"currentDepth"`

	// Model configuration.
	Model sympoziumv1alpha1.ModelSpec `json:"model"`

	// Skills to mount.
	Skills []sympoziumv1alpha1.SkillRef `json:"skills,omitempty"`

	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Volumes and VolumeMounts are propagated from the parent AgentRun
	// so sub-agents inherit user-provided secret/PVC mounts (e.g. Vault CSI).
	Volumes      []corev1.Volume      `json:"volumes,omitempty"`
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// TargetPersona is the name of a persona within the same Ensemble.
	// When set (along with PackName), the spawner resolves it to the
	// correct Agent, overriding InstanceName.
	TargetPersona string `json:"targetPersona,omitempty"`

	// PackName is the Ensemble that owns both the parent and target personas.
	// Required when TargetPersona is set.
	PackName string `json:"packName,omitempty"`

	// ChildIndex disambiguates multiple children spawned at the same depth
	// (e.g. from a spawn_subagents batch). Zero means single spawn (legacy naming).
	ChildIndex int `json:"childIndex,omitempty"`
}

// SpawnResult is the result of a spawn operation.
type SpawnResult struct {
	// RunName is the name of the created AgentRun.
	RunName string `json:"runName"`

	// Error is set if the spawn failed.
	Error string `json:"error,omitempty"`
}

// Spawn creates a new AgentRun CR for a sub-agent.
func (s *Spawner) Spawn(ctx context.Context, req SpawnRequest) (*SpawnResult, error) {
	// Resolve persona-targeted delegation: look up the Ensemble to find
	// the target persona's installed instance name.
	if req.TargetPersona != "" && req.PackName != "" {
		resolved, err := s.resolvePersonaTarget(ctx, req)
		if err != nil {
			return &SpawnResult{Error: err.Error()}, err
		}
		req = resolved
	}

	ctx, span := spawnerTracer.Start(ctx, "sympozium.pod.create",
		trace.WithAttributes(
			attribute.String("parent.run", req.ParentRunName),
			attribute.String("instance.name", req.InstanceName),
			attribute.Int("spawn.depth", req.CurrentDepth+1),
		),
	)
	defer span.End()

	if req.TargetPersona != "" {
		span.SetAttributes(
			attribute.String("target.persona", req.TargetPersona),
			attribute.String("pack.name", req.PackName),
		)
	}

	log := s.Log.WithValues(
		"parentRun", req.ParentRunName,
		"instance", req.InstanceName,
		"depth", req.CurrentDepth+1,
	)

	runName := fmt.Sprintf("sub-%s-%d", req.ParentRunName, req.CurrentDepth+1)
	if req.ChildIndex > 0 {
		runName = fmt.Sprintf("sub-%s-%d-%d", req.ParentRunName, req.CurrentDepth+1, req.ChildIndex)
	}
	sessionKey := fmt.Sprintf("%s:sub:%s", req.ParentSessionKey, runName)

	span.SetAttributes(attribute.String("run.name", runName))
	log.Info("Spawning sub-agent", "runName", runName)

	agentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: req.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance":   req.InstanceName,
				"sympozium.ai/agent-id":   req.AgentID,
				"sympozium.ai/parent-run": req.ParentRunName,
				"sympozium.ai/component":  "agent-run",
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   req.InstanceName,
			AgentID:    req.AgentID,
			SessionKey: sessionKey,
			Parent: &sympoziumv1alpha1.ParentRunRef{
				RunName:    req.ParentRunName,
				SessionKey: req.ParentSessionKey,
				SpawnDepth: req.CurrentDepth + 1,
			},
			Task:             req.Task,
			SystemPrompt:     req.SystemPrompt,
			Model:            req.Model,
			Skills:           req.Skills,
			Cleanup:          "delete",
			ImagePullSecrets: req.ImagePullSecrets,
			Volumes:          req.Volumes,
			VolumeMounts:     req.VolumeMounts,
		},
	}

	// Look up the instance to propagate lifecycle hooks to sub-agents.
	var inst sympoziumv1alpha1.Agent
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.InstanceName}, &inst); err == nil {
		agentRun.Spec.Lifecycle = inst.Spec.Agents.Default.Lifecycle
	}

	if err := s.Client.Create(ctx, agentRun); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "create agentrun failed")
		return &SpawnResult{Error: err.Error()}, err
	}

	return &SpawnResult{RunName: runName}, nil
}

// resolvePersonaTarget looks up a Ensemble to find the Agent
// that corresponds to the requested target persona. It also inherits the
// target persona's system prompt, skills, and model if not already set.
func (s *Spawner) resolvePersonaTarget(ctx context.Context, req SpawnRequest) (SpawnRequest, error) {
	var pack sympoziumv1alpha1.Ensemble
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.PackName}, &pack); err != nil {
		return req, fmt.Errorf("Ensemble %q not found: %w", req.PackName, err)
	}

	// Find the target persona's installed instance.
	var targetAgentName string
	for _, ip := range pack.Status.InstalledAgentConfigs {
		if ip.Name == req.TargetPersona {
			targetAgentName = ip.InstanceName
			break
		}
	}
	if targetAgentName == "" {
		return req, fmt.Errorf("persona %q not found or not installed in Ensemble %q", req.TargetPersona, req.PackName)
	}

	// Validate that a relationship edge exists between the personas.
	if len(pack.Spec.Relationships) > 0 {
		// Determine the source persona from the parent's instance name.
		var sourcePersona string
		for _, ip := range pack.Status.InstalledAgentConfigs {
			if ip.InstanceName == req.InstanceName {
				sourcePersona = ip.Name
				break
			}
		}
		if sourcePersona != "" {
			edgeExists := false
			for _, rel := range pack.Spec.Relationships {
				if rel.Source == sourcePersona && rel.Target == req.TargetPersona &&
					(rel.Type == "delegation" || rel.Type == "sequential") {
					edgeExists = true
					break
				}
			}
			if !edgeExists {
				return req, fmt.Errorf("no delegation or sequential relationship from %q to %q in Ensemble %q",
					sourcePersona, req.TargetPersona, req.PackName)
			}
		}
	}

	req.InstanceName = targetAgentName

	// Inherit system prompt and skills from the target persona spec if not
	// explicitly provided in the spawn request.
	for _, p := range pack.Spec.AgentConfigs {
		if p.Name == req.TargetPersona {
			if req.SystemPrompt == "" {
				req.SystemPrompt = p.SystemPrompt
			}
			if len(req.Skills) == 0 && len(p.Skills) > 0 {
				skills := make([]sympoziumv1alpha1.SkillRef, len(p.Skills))
				for i, sk := range p.Skills {
					skills[i] = sympoziumv1alpha1.SkillRef{SkillPackRef: sk}
				}
				req.Skills = skills
			}
			break
		}
	}

	s.Log.Info("Resolved persona target",
		"pack", req.PackName,
		"targetPersona", req.TargetPersona,
		"resolvedInstance", targetAgentName,
	)

	return req, nil
}
