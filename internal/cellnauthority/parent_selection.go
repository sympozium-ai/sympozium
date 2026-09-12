package cellnauthority

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// ParentSelection pins intent and independently intersected borrowed-tool
// grants. It is not a launch profile, model permit, parent compatibility claim,
// or authority to dispatch. A parent issuer must independently bind its approved
// parent/worker architecture and host limits to this exact selection.
type ParentSelection struct {
	APIVersion string            `json:"apiVersion"`
	Run        Subject           `json:"run"`
	Snapshot   SelectionSnapshot `json:"snapshot"`
}

// FreezeParentRun deliberately does not call FreezeRun/Prepare: those produce
// a disposable one-shot composition and cannot authorize an enduring parent.
func (l Loader) FreezeParentRun(ctx context.Context, key types.NamespacedName) (*ParentSelection, error) {
	if l.Reader == nil || key.Namespace == "" || key.Name == "" {
		return nil, fmt.Errorf("reader and parent run key required")
	}
	read := func() (*api.AgentRun, Subject, error) {
		var run api.AgentRun
		if err := l.Reader.Get(ctx, key, &run); err != nil {
			return nil, Subject{}, err
		}
		if run.Spec.ExecutionLifecycle != "enduring" || run.Spec.ValidateLifecycle() != "" || !run.Spec.Task.IsString() || strings.TrimSpace(run.Spec.Task.GetPrompt()) == "" || len(run.Spec.Task.GetPrompt()) > 2048 || strings.ContainsRune(run.Spec.Task.GetPrompt(), 0) {
			return nil, Subject{}, fmt.Errorf("bounded native enduring run intent required")
		}
		if run.Status.CellnRequest != "" || run.Status.CellnActionID != "" || run.Status.CellnIssuance != nil || run.Status.JobName != "" || run.Status.DeploymentName != "" {
			return nil, Subject{}, fmt.Errorf("parent selection cannot replace another execution path")
		}
		id, err := IdentifySubject("AgentRun", run.ObjectMeta, run.Spec)
		return &run, id, err
	}
	run, id, err := read()
	if err != nil {
		return nil, err
	}
	if len(run.Spec.CellnSelection.ClusterToolRefs) != 0 {
		return nil, fmt.Errorf("AUTH_CAPABILITY_UNSUPPORTED: shared catalogue selection requires mediated parent admission; legacy parent issuance cannot drop clusterToolRefs")
	}
	refs := run.Spec.CellnSelection.ToolRefs
	if refs == nil || len(refs) > 16 {
		return nil, fmt.Errorf("explicit bounded borrowed-tool selection required")
	}
	selection := make([]Selection, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.Name == "" || ref.Revision == "" || seen[ref.Name] {
			return nil, fmt.Errorf("invalid or duplicate borrowed-tool reference")
		}
		seen[ref.Name] = true
		selection = append(selection, Selection{Name: ref.Name, Revision: ref.Revision})
	}
	snapshot, err := l.resolveRuntime(ctx, types.NamespacedName{Namespace: key.Namespace, Name: run.Spec.AgentRef}, selection, run.Spec.CellnSelection.RuntimeRef)
	if err != nil {
		return nil, err
	}
	_, fresh, err := read()
	if err != nil {
		return nil, err
	}
	if fresh != id {
		return nil, fmt.Errorf("parent intent changed during resolution")
	}
	result := &ParentSelection{APIVersion: "sympozium.ai/celln-parent-selection-v1", Run: id, Snapshot: *snapshot}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 262144 {
		return nil, fmt.Errorf("parent selection exceeds bound")
	}
	return result, nil
}

// RevalidateParentSelection observes live sources again; it never substitutes
// new tools, revises a parent identity or renews a host permit.
func (l Loader) RevalidateParentSelection(ctx context.Context, frozen ParentSelection) error {
	if frozen.APIVersion != "sympozium.ai/celln-parent-selection-v1" || frozen.Run.Kind != "AgentRun" {
		return fmt.Errorf("invalid parent selection version/subject")
	}
	fresh, err := l.FreezeParentRun(ctx, types.NamespacedName{Namespace: frozen.Run.Namespace, Name: frozen.Run.Name})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*fresh, frozen) {
		return fmt.Errorf("parent intent or current grants changed")
	}
	return nil
}
