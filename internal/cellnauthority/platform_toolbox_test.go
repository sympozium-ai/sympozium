package cellnauthority

import (
	"context"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"k8s.io/apimachinery/pkg/types"
)

// toolboxFixture makes the fixture's profile a toolbox lending two tools in
// that order, and selects them as given.
func toolboxFixture(t *testing.T, selected ...string) platformFixture {
	t.Helper()
	f := newPlatformFixture(t, "toolbox", true)
	ctx := context.Background()
	var write api.ClusterCellnTool
	if err := f.client.Get(ctx, types.NamespacedName{Name: "workspace-write-v1"}, &write); err != nil {
		t.Fatal(err)
	}
	read := &api.ClusterCellnTool{}
	read.Name, read.Spec = "workspace-read-v1", *write.Spec.DeepCopy()
	read.Spec.EntryPoint, read.Spec.Closure.Hash = "/bin/read", "blake3:"+strings.Repeat("8", 64)
	read.Generation, read.UID = 1, "read-tool-uid"
	if err := f.client.Create(ctx, read); err != nil {
		t.Fatal(err)
	}
	var policies api.CellnExecutionPolicyList
	if err := f.client.List(ctx, &policies); err != nil {
		t.Fatal(err)
	}
	for i := range policies.Items {
		policy := &policies.Items[i]
		policy.Spec.Tools = append(policy.Spec.Tools, api.CellnExecutionPolicyTool{Ref: api.ClusterCellnToolRef{Name: read.Name, Revision: "v1"}})
		if err := f.client.Update(ctx, policy); err != nil {
			t.Fatal(err)
		}
	}
	var profile api.CellnRuntimeProfile
	if err := f.client.Get(ctx, types.NamespacedName{Name: "json-agent-v1"}, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Labels = map[string]string{cellnplatform.ToolboxLabel: "true"}
	profile.Annotations = map[string]string{cellnplatform.ToolboxToolsAnnotation: cellnplatform.ToolboxAnnotation([]api.ClusterCellnToolRef{{Name: read.Name, Revision: "v1"}, {Name: "workspace-write-v1", Revision: "v1"}})}
	if err := f.client.Update(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	var run api.AgentRun
	if err := f.client.Get(ctx, f.runKey, &run); err != nil {
		t.Fatal(err)
	}
	run.Spec.CellnSelection.ClusterToolRefs = nil
	for _, name := range selected {
		run.Spec.CellnSelection.ClusterToolRefs = append(run.Spec.CellnSelection.ClusterToolRefs, api.ClusterCellnToolRef{Name: name, Revision: "v1"})
	}
	if err := f.client.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
	return f
}

// A run on a toolbox runtime lends exactly its recorded tools, in order:
// Celln runs the toolbox closure for no other selection, so anything else is
// refused here with the reason, not by a node after admission.
func TestPlatformResolverToolboxAdmitsExactlyItsCatalogueOrderedTools(t *testing.T) {
	f := toolboxFixture(t, "workspace-read-v1", "workspace-write-v1")
	resolution, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f))
	if err != nil {
		t.Fatalf("catalogue-ordered toolbox selection refused: %v", err)
	}
	if tools := resolution.Decision.Tools; len(tools) != 2 || tools[0].Name != "workspace-read-v1" || tools[1].Name != "workspace-write-v1" {
		t.Fatalf("decision does not keep the toolbox order: %+v", tools)
	}
	for name, selected := range map[string][]string{
		"reordered": {"workspace-write-v1", "workspace-read-v1"},
		"partial":   {"workspace-write-v1"},
		"none":      nil,
	} {
		f := toolboxFixture(t, selected...)
		_, err := f.resolver.Resolve(t.Context(), f.runKey, platformRequest(f))
		if PlatformReason(err) != ReasonToolOrder || !strings.Contains(err.Error(), "exactly its 2 tools") {
			t.Fatalf("%s selection on the toolbox: %v", name, err)
		}
	}
}
