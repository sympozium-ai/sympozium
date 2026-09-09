package cellnauthority

import (
	"context"
	"fmt"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

// PermissionPreview is an observation of the current tool grant intersection,
// not model authorization, admission, readiness, a lease or a dispatch ticket.
type PermissionPreview struct {
	Agent               Subject                     `json:"agent"`
	Runtime             Subject                     `json:"runtime"`
	Tools               []Grant                     `json:"tools"`
	RuntimeLimits       api.AgentRuntimeCellnLimits `json:"runtimeLimits"`
	ExecutionAuthorized bool                        `json:"executionAuthorized"`
	Readiness           string                      `json:"readiness"`
}

func (l Loader) Preview(ctx context.Context, agent types.NamespacedName, intent api.CellnCatalogueSelection) (*PermissionPreview, error) {
	return l.PreviewLifecycle(ctx, agent, intent, "")
}

func (l Loader) PreviewLifecycle(ctx context.Context, agent types.NamespacedName, intent api.CellnCatalogueSelection, lifecycle string) (*PermissionPreview, error) {
	if lifecycle != "" && lifecycle != "enduring" {
		return nil, fmt.Errorf("unsupported preview lifecycle")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if intent.ToolRefs == nil || len(intent.ToolRefs) > 16 || (intent.RuntimeRef != "" && len(validation.IsDNS1123Subdomain(intent.RuntimeRef)) != 0) {
		return nil, fmt.Errorf("explicit bounded toolRefs and same-namespace runtime name required")
	}
	selected := make([]Selection, 0, len(intent.ToolRefs))
	seen := map[string]bool{}
	for _, ref := range intent.ToolRefs {
		if len(validation.IsDNS1123Subdomain(ref.Name)) != 0 || ref.Revision == "" || len(ref.Revision) > 64 || seen[ref.Name] {
			return nil, fmt.Errorf("unique names and exact bounded revisions required")
		}
		seen[ref.Name] = true
		selected = append(selected, Selection{Name: ref.Name, Revision: ref.Revision})
	}
	snapshot, err := l.resolveRuntime(ctx, agent, selected, intent.RuntimeRef)
	if err != nil {
		return nil, err
	}
	prepared, err := prepare(*snapshot, 33554432, lifecycle == "enduring")
	if err != nil {
		return nil, err
	}
	result := &PermissionPreview{Agent: snapshot.Agent, Runtime: snapshot.Runtime, Tools: []Grant{}, RuntimeLimits: prepared.Limits, Readiness: "not-established"}
	for _, tool := range snapshot.Tools {
		result.Tools = append(result.Tools, Grant{Tool: tool.Identity, Limits: *tool.Limits.DeepCopy()})
	}
	return result, nil
}
