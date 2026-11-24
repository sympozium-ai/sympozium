package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
	"github.com/sympozium-ai/sympozium/internal/substrate"
	"github.com/sympozium-ai/sympozium/internal/toolpolicy"
	"github.com/sympozium-ai/sympozium/pkg/sidecartools"
)

// Tools for Substrate actors.
//
// In a pod, a tool that acts outside the agent container goes through a
// trusted sidecar: the runner writes a request into /ipc and the ipc-bridge
// forwards it. An actor has no sidecar, and anything inside its sandbox may be
// driven by the model. So a tool that acts outside the sandbox is a request
// to this endpoint, and everything that decides whether it happens is here:
// which run is asking comes from the actor's verified identity, never from
// the request; the run's tool policy is read from the cluster on each call;
// and what is published carries the run's identity as this controller knows
// it. The runner inside the sandbox is told which tools exist, but nothing
// depends on it honouring that.
//
// Served so far are the tools whose in-pod path is one event on the bus.
// Tools that wait for an answer (delegation, sub-agents), skills, MCP servers
// and memory are not served yet; a run that needs them runs as a Job.

const (
	toolSendChannelMessage = "send_channel_message"
	toolScheduleTask       = "schedule_task"
)

// substrateServedTools are the tools an actor can call, in a stable order.
var substrateServedTools = []string{toolSendChannelMessage, toolScheduleTask}

// toolPermitted applies a resolved tool policy to one tool, the same way the
// runner filters its tool list: a deny wins, and a non-empty allow list
// admits only what it names.
func toolPermitted(policy *sympoziumv1alpha1.ToolPolicySpec, tool string) bool {
	if policy == nil {
		return true
	}
	if slices.Contains(policy.Deny, tool) || slices.Contains(policy.Deny, sidecartools.DenyAllTools) {
		return false
	}
	return len(policy.Allow) == 0 || slices.Contains(policy.Allow, tool)
}

// substrateToolPolicy is the policy a run's tool calls are held to: its own
// spec.toolPolicy with its Agent's SympoziumPolicy gating applied.
func (r *AgentRunReconciler) substrateToolPolicy(ctx context.Context, reader client.Reader, run *sympoziumv1alpha1.AgentRun) (*sympoziumv1alpha1.ToolPolicySpec, *sympoziumv1alpha1.Agent, error) {
	agent := &sympoziumv1alpha1.Agent{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.AgentRef}, agent); err != nil {
		return nil, nil, fmt.Errorf("resolving tool policy: agent %q: %w", run.Spec.AgentRef, err)
	}
	if agent.Spec.PolicyRef == "" {
		return run.Spec.ToolPolicy, agent, nil
	}
	policy := &sympoziumv1alpha1.SympoziumPolicy{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: agent.Spec.PolicyRef}, policy); err != nil {
		// A policy that cannot be read permits nothing.
		return nil, nil, fmt.Errorf("resolving tool policy: policy %q: %w", agent.Spec.PolicyRef, err)
	}
	return toolpolicy.WithGating(run.Spec.ToolPolicy, policy.Spec.ToolGating), agent, nil
}

// substrateOfferedTools is what the runner in the actor is told it has: the
// served tools the run's policy permits. It is advice to the runner; CallTool
// checks again.
func (r *AgentRunReconciler) substrateOfferedTools(ctx context.Context, run *sympoziumv1alpha1.AgentRun) ([]string, error) {
	if r.EventBus == nil {
		return nil, nil
	}
	policy, agent, err := r.substrateToolPolicy(ctx, r.Client, run)
	if err != nil {
		return nil, err
	}
	var offered []string
	for _, tool := range substrateServedTools {
		if !toolPermitted(policy, tool) {
			continue
		}
		if tool == toolSendChannelMessage && len(agentChannelTypes(agent)) == 0 {
			continue // nowhere it may send
		}
		offered = append(offered, tool)
	}
	return offered, nil
}

func agentChannelTypes(agent *sympoziumv1alpha1.Agent) []string {
	var types []string
	for _, ch := range agent.Spec.Channels {
		if t := strings.ToLower(strings.TrimSpace(ch.Type)); t != "" {
			types = append(types, t)
		}
	}
	return types
}

// CallTool makes one tool call for the run the actor is bound to.
func (s *SubstrateTurns) CallTool(ctx context.Context, actor substrate.ActorRef, tool string, args json.RawMessage) (string, error) {
	r := s.r
	// The API reader, not the cache: a run that has just ended, or a policy
	// that has just changed, must be seen here.
	run, ok := s.runFor(ctx, r.uncachedReader(), actor)
	if !ok || run.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning || run.Status.Substrate.Outcome != nil {
		return "", substrate.ErrNoSuchTurn
	}
	if !slices.Contains(substrateServedTools, tool) || r.EventBus == nil {
		return "", &substrate.ToolRefusal{Reason: fmt.Sprintf("tool %q is not available to this run", tool)}
	}
	policy, agent, err := r.substrateToolPolicy(ctx, r.uncachedReader(), run)
	if err != nil {
		return "", err
	}
	if !toolPermitted(policy, tool) {
		r.Log.Info("Refused actor tool call: denied by policy", "run", run.Name, "namespace", run.Namespace, "tool", tool)
		return "", &substrate.ToolRefusal{Reason: fmt.Sprintf("tool %q is denied by policy", tool)}
	}
	metadata := map[string]string{"agentRunID": run.Name, "instanceName": run.Spec.AgentRef, "namespace": run.Namespace}

	switch tool {
	case toolSendChannelMessage:
		var msg struct {
			Channel string `json:"channel"`
			Text    string `json:"text"`
		}
		if json.Unmarshal(args, &msg) != nil || msg.Channel == "" || msg.Text == "" {
			return "", &substrate.ToolRefusal{Reason: "send_channel_message needs a channel and text"}
		}
		// An agent may send only on a channel its Agent is configured with.
		if !slices.Contains(agentChannelTypes(agent), strings.ToLower(strings.TrimSpace(msg.Channel))) {
			r.Log.Info("Refused actor tool call: channel not configured on the Agent", "run", run.Name, "namespace", run.Namespace, "channel", msg.Channel)
			return "", &substrate.ToolRefusal{Reason: fmt.Sprintf("channel %q is not configured for this agent", msg.Channel)}
		}
		username := strings.TrimSpace(run.Annotations["sympozium.ai/agent-display-name"])
		if username == "" {
			username = strings.TrimSpace(run.Spec.AgentRef)
		}
		sanitized, _, err := ipc.SanitizeOutboundMessage(args, username)
		if err != nil {
			return "", &substrate.ToolRefusal{Reason: "send_channel_message arguments are not an object"}
		}
		if err := s.publish(ctx, eventbus.TopicChannelMessageSend, metadata, sanitized); err != nil {
			return "", err
		}
		return fmt.Sprintf("Message sent to %s channel", msg.Channel), nil

	case toolScheduleTask:
		var req struct {
			Name   string `json:"name"`
			Action string `json:"action"`
		}
		if json.Unmarshal(args, &req) != nil || req.Name == "" || req.Action == "" {
			return "", &substrate.ToolRefusal{Reason: "schedule_task needs a name and an action"}
		}
		if err := s.publish(ctx, eventbus.TopicScheduleUpsert, metadata, args); err != nil {
			return "", err
		}
		return fmt.Sprintf("Schedule %q: %s requested", req.Name, req.Action), nil
	}
	return "", &substrate.ToolRefusal{Reason: fmt.Sprintf("tool %q is not available to this run", tool)}
}

func (s *SubstrateTurns) publish(ctx context.Context, topic string, metadata map[string]string, data json.RawMessage) error {
	event, err := eventbus.NewEvent(topic, metadata, data)
	if err != nil {
		return err
	}
	return s.r.EventBus.Publish(ctx, topic, event)
}
