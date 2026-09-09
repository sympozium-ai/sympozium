package cellnauthority

import (
	"context"
	"encoding/json"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestEnduringPreviewShowsBrokerIntersectionWithoutEnablingOneShot(t *testing.T) {
	l, c, agent, selected := loaderFixture(t)
	ctx := context.Background()
	var tool api.CellnTool
	if err := c.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: selected[0].Name}, &tool); err != nil {
		t.Fatal(err)
	}
	tool.Spec.Limits.Artifacts = &api.CellnArtifactLimits{Operation: "read", MaxOperations: 4, MaxFiles: 8, MaxFileBytes: 1024, MaxTotalBytes: 4096}
	if err := c.Update(ctx, &tool); err != nil {
		t.Fatal(err)
	}
	id, err := Identify(tool)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []types.NamespacedName{l.OperatorSource, l.RuntimeSource, l.AgentSource} {
		var cm corev1.ConfigMap
		if err := c.Get(ctx, key, &cm); err != nil {
			t.Fatal(err)
		}
		var doc GrantDocument
		if err := json.Unmarshal([]byte(cm.Data["grants.json"]), &doc); err != nil {
			t.Fatal(err)
		}
		doc.Grants = []Grant{{Tool: id, Limits: *tool.Spec.Limits.DeepCopy()}}
		if key == l.AgentSource {
			doc.Grants[0].Limits.Artifacts.MaxOperations = 2
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		cm.Data["grants.json"] = string(raw)
		if err := c.Update(ctx, &cm); err != nil {
			t.Fatal(err)
		}
	}
	intent := api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{{Name: tool.Name, Revision: tool.Spec.Revision}}}
	if _, err := l.Preview(ctx, agent, intent); err == nil {
		t.Fatal("one-shot preview accepted parent broker capability")
	}
	got, err := l.PreviewLifecycle(ctx, agent, intent, "enduring")
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionAuthorized || got.Readiness != "not-established" || got.Tools[0].Limits.Artifacts.MaxOperations != 2 {
		t.Fatalf("incorrect enduring preview: %+v", got)
	}
	if _, err := l.PreviewLifecycle(ctx, agent, intent, "persistent-override"); err == nil {
		t.Fatal("unknown lifecycle accepted")
	}
	snapshot, err := l.Resolve(ctx, agent, selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(*snapshot, 33554432); err == nil {
		t.Fatal("preview broadened one-shot composition authority")
	}
}

func TestPermissionPreviewUsesCurrentIntersection(t *testing.T) {
	l, c, agent, selected := loaderFixture(t)
	ctx := context.Background()
	var cm corev1.ConfigMap
	if err := c.Get(ctx, l.AgentSource, &cm); err != nil {
		t.Fatal(err)
	}
	var doc GrantDocument
	if err := json.Unmarshal([]byte(cm.Data["grants.json"]), &doc); err != nil {
		t.Fatal(err)
	}
	doc.Grants[0].Limits.TimeoutMillis = 7
	doc.Grants[0].Limits.MemoryBytes = 1024
	raw, _ := json.Marshal(doc)
	cm.Data["grants.json"] = string(raw)
	if err := c.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	intent := api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{{Name: selected[0].Name, Revision: selected[0].Revision}}}
	got, err := l.Preview(ctx, agent, intent)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionAuthorized || got.Readiness != "not-established" || len(got.Tools) != 1 || got.Tools[0].Limits.TimeoutMillis != 7 || got.RuntimeLimits.MemoryBytes != 1024 {
		t.Fatalf("wrong effective preview: %+v", got)
	}
	if err := c.Delete(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Preview(ctx, agent, intent); err == nil {
		t.Fatal("withdrawn grants previewed")
	}
}

func TestPermissionPreviewRefusesAmbiguity(t *testing.T) {
	l, _, agent, selected := loaderFixture(t)
	ref := api.CellnCatalogueToolRef{Name: selected[0].Name, Revision: selected[0].Revision}
	for _, intent := range []api.CellnCatalogueSelection{
		{}, {ToolRefs: []api.CellnCatalogueToolRef{ref, ref}},
		{RuntimeRef: "other/runtime", ToolRefs: []api.CellnCatalogueToolRef{}},
		{ToolRefs: []api.CellnCatalogueToolRef{{Name: "other/tool", Revision: "v1"}}},
		{RuntimeRef: "unapproved", ToolRefs: []api.CellnCatalogueToolRef{}},
	} {
		if _, err := l.Preview(context.Background(), agent, intent); err == nil {
			t.Fatalf("accepted %+v", intent)
		}
	}
	got, err := l.Preview(context.Background(), agent, api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}})
	if err != nil || got.Tools == nil || len(got.Tools) != 0 {
		t.Fatalf("explicit empty preview: %+v %v", got, err)
	}
}
