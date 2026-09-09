package agentexecution

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestResolveInheritsAgentDefaults(t *testing.T) {
	agent := &api.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "native"},
		Spec: api.AgentSpec{
			RuntimeRef: "json-harness",
			Execution: &api.AgentExecutionDefaults{
				Backend:            "celln",
				ExecutionLifecycle: "enduring",
				Provider:           "deepseek",
				Model:              "deepseek-chat",
				Enduring:           &api.EnduringRunSpec{LeaseSeconds: 600, MaxTurns: 8, MaxModelRequests: 24, MaxOutputTokens: 8192},
				CellnSelection: &api.CellnCatalogueSelection{
					ToolRefs: []api.CellnCatalogueToolRef{{Name: "workspace-write", Revision: "v1"}},
				},
			},
		},
	}
	got, err := Resolve(agent, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != "celln" || got.ExecutionLifecycle != "enduring" || got.Provider != "deepseek" || got.Model != "deepseek-chat" {
		t.Fatalf("inherited identity: %+v", got)
	}
	if got.CellnSelection == nil || len(got.CellnSelection.ToolRefs) != 1 || got.Enduring == nil || got.Enduring.LeaseSeconds != 600 {
		t.Fatalf("inherited selection/limits: %+v", got)
	}
	if len(got.Inherited) == 0 {
		t.Fatal("expected inherited field audit")
	}
}

func TestResolveExplicitEmptyToolsNotInherited(t *testing.T) {
	agent := &api.Agent{Spec: api.AgentSpec{Execution: &api.AgentExecutionDefaults{
		Backend: "celln",
		CellnSelection: &api.CellnCatalogueSelection{
			ToolRefs: []api.CellnCatalogueToolRef{{Name: "workspace-write", Revision: "v1"}},
		},
		Provider: "deepseek",
		Model:    "deepseek-chat",
	}}}
	got, err := Resolve(agent, Input{
		Backend:        "celln",
		Provider:       "deepseek",
		Model:          "deepseek-chat",
		CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.CellnSelection == nil || got.CellnSelection.ToolRefs == nil || len(got.CellnSelection.ToolRefs) != 0 {
		t.Fatalf("explicit empty tools must win: %+v", got.CellnSelection)
	}
	for _, field := range got.Inherited {
		if field == "cellnSelection" {
			t.Fatal("explicit selection must not be marked inherited")
		}
	}
}

func TestResolveRejectsSkillsWithCelln(t *testing.T) {
	agent := &api.Agent{Spec: api.AgentSpec{
		Skills:    []api.SkillRef{{SkillPackRef: "web-endpoint"}},
		Execution: &api.AgentExecutionDefaults{Backend: "celln", CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}}, Provider: "deepseek", Model: "deepseek-chat"},
	}}
	if _, err := Resolve(agent, Input{}); err == nil {
		t.Fatal("expected skills incompatibility")
	}
}

func TestResolveRejectsSilentJobFallback(t *testing.T) {
	agent := &api.Agent{Spec: api.AgentSpec{Execution: &api.AgentExecutionDefaults{
		Backend: "celln", CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}}, Provider: "deepseek", Model: "deepseek-chat",
	}}}
	if _, err := Resolve(agent, Input{Backend: "job", CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}}}); err == nil {
		t.Fatal("expected refusal to keep tools on job backend")
	}
}

func TestResolvePreservesAgentsWithoutDefaults(t *testing.T) {
	got, err := Resolve(&api.Agent{}, Input{Backend: "", Model: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != "" || got.CellnSelection != nil || got.ExecutionLifecycle != "" {
		t.Fatalf("legacy agents must stay unchanged: %+v", got)
	}
}

func TestAgentExecutionDefaultsValidate(t *testing.T) {
	bad := &api.AgentExecutionDefaults{Backend: "job", CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}}}
	if bad.Validate() == "" {
		t.Fatal("expected job+cellnSelection rejection")
	}
	good := &api.AgentExecutionDefaults{
		Backend: "celln", ExecutionLifecycle: "enduring",
		Enduring:       &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 2, MaxModelRequests: 4, MaxOutputTokens: 1024},
		CellnSelection: &api.CellnCatalogueSelection{ToolRefs: []api.CellnCatalogueToolRef{}},
	}
	if good.Validate() != "" {
		t.Fatal(good.Validate())
	}
}
