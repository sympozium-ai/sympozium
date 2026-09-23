// Package toolpolicy resolves the tool policy a run is held to: from Ensemble
// AgentConfig specs and from the Agent's SympoziumPolicy tool gating.
package toolpolicy

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/pkg/sidecartools"
)

// ForAgent resolves the ToolPolicy for an Agent instance by looking up
// the Ensemble it belongs to and finding the matching AgentConfig.
// Returns nil if the Agent has no ensemble/config labels or no policy is set.
func ForAgent(ctx context.Context, c client.Reader, inst *sympoziumv1alpha1.Agent) *sympoziumv1alpha1.ToolPolicySpec {
	ensembleName := inst.Labels["sympozium.ai/ensemble"]
	configName := inst.Labels["sympozium.ai/agent-config"]
	if ensembleName == "" || configName == "" {
		return nil
	}
	var ensemble sympoziumv1alpha1.Ensemble
	if err := c.Get(ctx, types.NamespacedName{Name: ensembleName, Namespace: inst.Namespace}, &ensemble); err != nil {
		return nil
	}
	for i := range ensemble.Spec.AgentConfigs {
		if ensemble.Spec.AgentConfigs[i].Name == configName {
			tp := ensemble.Spec.AgentConfigs[i].ToolPolicy
			if tp == nil {
				return nil
			}
			return &sympoziumv1alpha1.ToolPolicySpec{
				Allow: tp.Allow,
				Deny:  tp.Deny,
			}
		}
	}
	return nil
}

// WithGating returns the tool policy a run is held to once its Agent's
// SympoziumPolicy tool gating is applied on top of the run's own
// spec.toolPolicy. Policy deny rules are added to the run's deny list. With
// defaultAction deny, only tools a policy rule allows remain (narrowed further
// by the run's own allow list), and when none remain every tool is denied.
// A nil result means no filtering, as for a nil spec.toolPolicy.
func WithGating(run *sympoziumv1alpha1.ToolPolicySpec, gating *sympoziumv1alpha1.ToolGatingSpec) *sympoziumv1alpha1.ToolPolicySpec {
	if gating == nil {
		return run
	}
	var runAllow, runDeny []string
	if run != nil {
		runAllow, runDeny = run.Allow, run.Deny
	}
	var ruleAllow, ruleDeny []string
	for _, rule := range gating.Rules {
		switch rule.Action {
		case "allow":
			ruleAllow = append(ruleAllow, rule.Tool)
		case "deny":
			ruleDeny = append(ruleDeny, rule.Tool)
		}
	}
	out := &sympoziumv1alpha1.ToolPolicySpec{
		Allow: runAllow,
		Deny:  appendUnique(append([]string(nil), runDeny...), ruleDeny...),
	}
	if gating.DefaultAction == "deny" {
		permitted := ruleAllow
		if len(runAllow) > 0 {
			permitted = intersect(runAllow, ruleAllow)
		}
		out.Allow = permitted
		if len(permitted) == 0 {
			out.Deny = appendUnique(out.Deny, sidecartools.DenyAllTools)
		}
	}
	if len(out.Allow) == 0 && len(out.Deny) == 0 {
		return run
	}
	return out
}

func appendUnique(list []string, items ...string) []string {
	for _, item := range items {
		if !contains(list, item) {
			list = append(list, item)
		}
	}
	return list
}

func intersect(a, b []string) []string {
	var out []string
	for _, item := range a {
		if contains(b, item) && !contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}
