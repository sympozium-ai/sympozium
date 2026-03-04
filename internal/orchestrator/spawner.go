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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
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

	// InstanceName is the SympoziumInstance this belongs to.
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
	ctx, span := spawnerTracer.Start(ctx, "sympozium.pod.create",
		trace.WithAttributes(
			attribute.String("parent.run", req.ParentRunName),
			attribute.String("instance.name", req.InstanceName),
			attribute.Int("spawn.depth", req.CurrentDepth+1),
		),
	)
	defer span.End()

	log := s.Log.WithValues(
		"parentRun", req.ParentRunName,
		"instance", req.InstanceName,
		"depth", req.CurrentDepth+1,
	)

	runName := fmt.Sprintf("sub-%s-%d", req.ParentRunName, req.CurrentDepth+1)
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
			InstanceRef: req.InstanceName,
			AgentID:     req.AgentID,
			SessionKey:  sessionKey,
			Parent: &sympoziumv1alpha1.ParentRunRef{
				RunName:    req.ParentRunName,
				SessionKey: req.ParentSessionKey,
				SpawnDepth: req.CurrentDepth + 1,
			},
			Task:         req.Task,
			SystemPrompt: req.SystemPrompt,
			Model:        req.Model,
			Skills:       req.Skills,
			Cleanup:      "delete",
		},
	}

	if err := s.Client.Create(ctx, agentRun); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "create agentrun failed")
		return &SpawnResult{Error: err.Error()}, err
	}

	return &SpawnResult{RunName: runName}, nil
}
