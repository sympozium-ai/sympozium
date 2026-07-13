// Package toolpolicy resolves ToolPolicy from Ensemble AgentConfig specs.
package toolpolicy

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
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
