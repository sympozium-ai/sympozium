package taskmodes

import (
	"errors"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ── TaskModeHandler tests ─────────────────────────────────────────────────────
//
// These tests pin the per-mode contract independently of the controller's
// reconcile loop. They run against the package-private handler constructors
// (e.g. NewSidecarDrivenHandler) so the registry wiring is exercised by the
// controller-level tests in agentrun_task_mode_test.go.

func TestRegistry_RegisterAndGet(t *testing.T) {
	// SidecarDrivenHandler is registered by init() in register.go.
	h, ok := Get(SidecarDriven)
	if !ok {
		t.Fatalf("registry missing built-in mode %q; SupportedModes=%v", SidecarDriven, SupportedModes())
	}
	if h.Mode() != SidecarDriven {
		t.Errorf("Mode() = %q, want %q", h.Mode(), SidecarDriven)
	}
}

func TestRegistry_SupportedModesSorted(t *testing.T) {
	modes := SupportedModes()
	if len(modes) == 0 {
		t.Fatal("SupportedModes is empty — init() did not register any handlers")
	}
	// Must be sorted so log messages and error strings are stable.
	if !sort.StringsAreSorted(modes) {
		t.Errorf("SupportedModes not sorted: %v", modes)
	}
	// Must include the built-in.
	found := false
	for _, m := range modes {
		if m == SidecarDriven {
			found = true
		}
	}
	if !found {
		t.Errorf("SupportedModes = %v, missing %q", modes, SidecarDriven)
	}
}

func TestRegistry_GetUnknownReturnsFalse(t *testing.T) {
	h, ok := Get("not-a-real-mode")
	if ok {
		t.Errorf("Get(%q) returned ok=true; handler=%v", "not-a-real-mode", h)
	}
	if h != nil {
		t.Errorf("Get(%q) handler should be nil, got %T", "not-a-real-mode", h)
	}
}

func TestRegister_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Register(nil) did not panic")
		}
	}()
	Register(nil)
}

func TestRegister_PanicsOnDuplicate(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("Register(duplicate) did not panic")
		}
	}()
	// SidecarDrivenHandler is already registered.
	Register(NewSidecarDrivenHandler())
}

// ── SidecarDrivenHandler unit tests ──────────────────────────────────────────

func TestSidecarDrivenHandler_Mode(t *testing.T) {
	h := NewSidecarDrivenHandler()
	if h.Mode() != SidecarDriven {
		t.Errorf("Mode() = %q, want %q", h.Mode(), SidecarDriven)
	}
}

func TestSidecarDrivenHandler_Validate_RequiresTool(t *testing.T) {
	h := NewSidecarDrivenHandler()
	task := &sympoziumv1alpha1.TaskSpec{Mode: SidecarDriven} // no Tool
	err := h.Validate(task)
	if err == nil {
		t.Fatal("Validate(task without Tool) returned nil; expected error")
	}
}

func TestSidecarDrivenHandler_Validate_AcceptsTool(t *testing.T) {
	h := NewSidecarDrivenHandler()
	task := &sympoziumv1alpha1.TaskSpec{Mode: SidecarDriven, Tool: "primary"}
	if err := h.Validate(task); err != nil {
		t.Errorf("Validate(task with Tool) returned error: %v", err)
	}
}

func TestSidecarDrivenHandler_ConfigureAgentContainer_SetsPromptServerMode(t *testing.T) {
	h := NewSidecarDrivenHandler()
	task := &sympoziumv1alpha1.TaskSpec{Mode: SidecarDriven, Tool: "primary"}
	env := []corev1.EnvVar{{Name: "EXISTING", Value: "keep"}}

	if err := h.ConfigureAgentContainer(task, &env); err != nil {
		t.Fatalf("ConfigureAgentContainer: %v", err)
	}

	found := false
	for _, e := range env {
		if e.Name == "AGENT_MODE" && e.Value == "prompt-server" {
			found = true
		}
	}
	if !found {
		t.Errorf("AGENT_MODE=prompt-server not set in env: %v", env)
	}
}

func TestSidecarDrivenHandler_AdjustSidecars_FindsNamedTool(t *testing.T) {
	h := NewSidecarDrivenHandler()
	task := &sympoziumv1alpha1.TaskSpec{
		Mode: SidecarDriven,
		Tool: "primary",
		Parameters: map[string]string{
			"batchSize": "2",
		},
	}
	sidecars := []SidecarContext{
		{
			SkillPackName: "scout",
			Sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "scout:latest",
				Tools: []sympoziumv1alpha1.SidecarTool{
					{Name: "secondary", Exec: []string{"node", "/app/dist/cli.js"}, Subcommand: "secondary"},
					{Name: "primary", Exec: []string{"node", "/app/dist/cli.js"}, Subcommand: "primary"},
				},
			},
		},
		{
			SkillPackName: "other",
			Sidecar:       sympoziumv1alpha1.SkillSidecar{Image: "other:latest"},
		},
	}

	adjustments, err := h.AdjustSidecars(task, sidecars)
	if err != nil {
		t.Fatalf("AdjustSidecars: %v", err)
	}
	if len(adjustments) != 1 {
		t.Fatalf("len(adjustments) = %d, want 1; got %+v", len(adjustments), adjustments)
	}
	adj := adjustments[0]
	if adj.SkillPackName != "scout" {
		t.Errorf("SkillPackName = %q, want %q", adj.SkillPackName, "scout")
	}
	// OverrideCommand must be the tool's Exec + Subcommand.
	if got, want := adj.OverrideCommand, []string{"node", "/app/dist/cli.js", "primary"}; !equalSlice(got, want) {
		t.Errorf("OverrideCommand = %v, want %v", got, want)
	}
	// AddEnv must include SYMPOZIUM_RUN_CONFIG_JSON with the JSON-marshalled parameters.
	if len(adj.AddEnv) != 1 || adj.AddEnv[0].Name != "SYMPOZIUM_RUN_CONFIG_JSON" {
		t.Fatalf("AddEnv = %+v, want one entry named SYMPOZIUM_RUN_CONFIG_JSON", adj.AddEnv)
	}
	if adj.AddEnv[0].Value != `{"batchSize":"2"}` {
		t.Errorf("SYMPOZIUM_RUN_CONFIG_JSON = %q, want %q", adj.AddEnv[0].Value, `{"batchSize":"2"}`)
	}
}

func TestSidecarDrivenHandler_AdjustSidecars_NoToolMatch(t *testing.T) {
	h := NewSidecarDrivenHandler()
	task := &sympoziumv1alpha1.TaskSpec{
		Mode: SidecarDriven,
		Tool: "nonexistent-tool",
	}
	sidecars := []SidecarContext{
		{
			SkillPackName: "scout",
			Sidecar: sympoziumv1alpha1.SkillSidecar{
				Tools: []sympoziumv1alpha1.SidecarTool{
					{Name: "unrelated"},
				},
			},
		},
	}
	_, err := h.AdjustSidecars(task, sidecars)
	if err == nil {
		t.Fatal("AdjustSidecars returned nil; expected error for missing tool")
	}
}

func TestSidecarDrivenHandler_AdjustSidecars_EmptyParameters(t *testing.T) {
	h := NewSidecarDrivenHandler()
	task := &sympoziumv1alpha1.TaskSpec{
		Mode: SidecarDriven,
		Tool: "primary",
		// no Parameters
	}
	sidecars := []SidecarContext{
		{
			SkillPackName: "scout",
			Sidecar: sympoziumv1alpha1.SkillSidecar{
				Tools: []sympoziumv1alpha1.SidecarTool{
					{Name: "primary", Exec: []string{"node", "/app/dist/cli.js"}},
				},
			},
		},
	}
	adjustments, err := h.AdjustSidecars(task, sidecars)
	if err != nil {
		t.Fatalf("AdjustSidecars: %v", err)
	}
	if got, want := adjustments[0].AddEnv[0].Value, `{}`; got != want {
		t.Errorf("SYMPOZIUM_RUN_CONFIG_JSON = %q, want %q (empty object for nil params)", got, want)
	}
}

// ── Custom test handler (for registry semantics) ────────────────────────────

// stubHandler is a no-op TaskModeHandler for testing Register/Get round-trips.
type stubHandler struct{ mode string }

func (s *stubHandler) Mode() string                                 { return s.mode }
func (s *stubHandler) Validate(_ *sympoziumv1alpha1.TaskSpec) error { return nil }
func (s *stubHandler) ConfigureAgentContainer(_ *sympoziumv1alpha1.TaskSpec, _ *[]corev1.EnvVar) error {
	return nil
}
func (s *stubHandler) AdjustSidecars(_ *sympoziumv1alpha1.TaskSpec, _ []SidecarContext) ([]SidecarAdjustment, error) {
	return nil, nil
}

var errStubValidate = errors.New("stub validation error")

// failingStubHandler validates successfully but AdjustSidecars always fails —
// useful for asserting the controller surfaces handler errors to reconcile.
type failingStubHandler struct{ stubHandler }

func (f *failingStubHandler) AdjustSidecars(_ *sympoziumv1alpha1.TaskSpec, _ []SidecarContext) ([]SidecarAdjustment, error) {
	return nil, errStubValidate
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
