package v1alpha1

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestCellnCatalogueSelectionKeepsScopesExplicit(t *testing.T) {
	legacy := CellnCatalogueSelection{RuntimeRef: "legacy-runtime", ToolRefs: []CellnCatalogueToolRef{{Name: "workspace-read", Revision: "v1"}}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip CellnCatalogueSelection
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.RuntimeRef != legacy.RuntimeRef || len(roundTrip.ToolRefs) != 1 || len(roundTrip.ClusterToolRefs) != 0 {
		t.Fatalf("legacy namespaced selection changed scope on round trip: %#v", roundTrip)
	}

	shared := CellnCatalogueSelection{ClusterToolRefs: []ClusterCellnToolRef{{Name: "workspace-read", Revision: "v2"}}}
	data, err = json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip = CellnCatalogueSelection{}
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if len(roundTrip.ClusterToolRefs) != 1 || roundTrip.ClusterToolRefs[0].Revision != "v2" {
		t.Fatalf("cluster catalogue selection lost explicit revision: %#v", roundTrip)
	}
}

func TestAgentRuntimeSharedProfileIsAdditive(t *testing.T) {
	runtimeSpec := AgentRuntimeSpec{CellnProfileRef: &CellnRuntimeProfileRef{Name: "json-agent", Revision: "v3"}, CellnLimits: &AgentRuntimeCellnLimits{TimeoutMillis: 30_000, MemoryBytes: 64 << 20, TaskBytes: 2048, OutputBytes: 65536, Workspace: "none"}}
	data, err := json.Marshal(runtimeSpec)
	if err != nil {
		t.Fatal(err)
	}
	var got AgentRuntimeSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.CellnProfileRef == nil || got.CellnProfileRef.Name != "json-agent" || got.Celln != nil {
		t.Fatalf("shared profile did not remain explicit: %#v", got)
	}
}

func TestCellnPlatformKindsRegisterWithScheme(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"CellnRuntimeProfile", "ClusterCellnTool", "CellnExecutionPolicy"} {
		gvk := GroupVersion.WithKind(kind)
		if _, err := scheme.New(gvk); err != nil {
			t.Fatalf("%s not registered: %v", kind, err)
		}
	}
}
