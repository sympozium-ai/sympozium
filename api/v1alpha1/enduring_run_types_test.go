package v1alpha1

import "testing"

func TestLifecyclePreservesLegacyAndRequiresExplicitEnduringLimits(t *testing.T) {
	for _, mode := range []string{"", "task", "server"} {
		s := AgentRunSpec{Mode: mode}
		if got := s.ValidateLifecycle(); got != "" {
			t.Fatal(got)
		}
	}
	valid := AgentRunSpec{Backend: "celln", ExecutionLifecycle: "enduring", CellnSelection: &CellnCatalogueSelection{}, Enduring: &EnduringRunSpec{LeaseSeconds: 3600, MaxTurns: 20, MaxModelRequests: 60, MaxOutputTokens: 30720}}
	if got := valid.ValidateLifecycle(); got != "" {
		t.Fatal(got)
	}
	for _, mutate := range []func(*AgentRunSpec){
		func(s *AgentRunSpec) { s.Mode = "server" },
		func(s *AgentRunSpec) { s.Backend = "job" },
		func(s *AgentRunSpec) { s.CellnSelection = nil },
		func(s *AgentRunSpec) { s.Enduring = nil },
		func(s *AgentRunSpec) { s.ExecutionLifecycle = "" },
		func(s *AgentRunSpec) { s.Enduring.MaxTurns = 1025 },
		func(s *AgentRunSpec) { s.Enduring.LeaseSeconds = 0 },
		func(s *AgentRunSpec) { s.Enduring.RequireToolCall = true },
	} {
		s := *valid.DeepCopy()
		mutate(&s)
		if s.ValidateLifecycle() == "" {
			t.Fatalf("accepted invalid lifecycle: %+v", s)
		}
	}
	shot := AgentRunSpec{ExecutionLifecycle: "one-shot", Mode: "server"}
	if shot.ValidateLifecycle() == "" {
		t.Fatal("accepted one-shot server")
	}
	valid.Enduring.RequireToolCall = true
	valid.CellnSelection.ToolRefs = []CellnCatalogueToolRef{{Name: "echo", Revision: "v1"}}
	if got := valid.ValidateLifecycle(); got != "" {
		t.Fatal(got)
	}
	valid.Enduring.MaxModelRequests = 1
	if valid.ValidateLifecycle() == "" {
		t.Fatal("required execution accepted no result-request budget")
	}
}
