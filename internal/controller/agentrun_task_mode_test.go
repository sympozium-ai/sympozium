package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
)

// envValue returns (value, true) when envs has an entry with the given name.
// Shared with the legacy agentrun_init_test.go helpers — re-declared here so
// this file compiles independently.
func envValueLocal(envs []corev1.EnvVar, name string) (string, bool) {
	for _, e := range envs {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// ── polymorphic-task dispatch tests ──────────────────────────────────────────
//
// These tests pin the controller's resolveTaskModeAdjustments + buildContainers
// wiring for the three dispatch cases:
//
//   1. String-form task → Path A (legacy LLM prompt): no adjustments,
//      AGENT_MODE unset on the agent container.
//   2. Object-form task with mode=sidecar-driven → Path B:
//      AGENT_MODE=prompt-server on the agent container; named sidecar's
//      command overridden; SYMPOZIUM_RUN_CONFIG_JSON set on that sidecar.
//   3. Object-form task with an unknown mode → resolveTaskModeAdjustments
//      returns an error naming the supported modes.

// stringModeRun returns an AgentRun whose task is a Path-A string prompt.
func stringModeRun(prompt string) *sympoziumv1alpha1.AgentRun {
	r := newTestRun()
	r.Spec.Task = sympoziumv1alpha1.NewStringTask(prompt)
	return r
}

// objectModeRun returns an AgentRun whose task is a Path-B object with the
// given mode/tool/parameters.
func objectModeRun(mode, tool string, params map[string]string) *sympoziumv1alpha1.AgentRun {
	r := newTestRun()
	r.Spec.Task = &sympoziumv1alpha1.TaskSpec{
		Mode:       mode,
		Tool:       tool,
		Parameters: params,
	}
	return r
}

// resolveTaskModeAdjustments short-circuits to nil/nil for nil or string-form
// tasks — Path A is unchanged.
func TestResolveTaskModeAdjustments_StringFormReturnsNil(t *testing.T) {
	run := stringModeRun("do the thing")

	adjustments, err := resolveTaskModeAdjustments(run, nil)
	if err != nil {
		t.Fatalf("resolveTaskModeAdjustments: %v", err)
	}
	if adjustments != nil {
		t.Errorf("adjustments = %+v, want nil for string-form task", adjustments)
	}
}

func TestResolveTaskModeAdjustments_NilTaskReturnsNil(t *testing.T) {
	run := newTestRun() // Task stays nil

	adjustments, err := resolveTaskModeAdjustments(run, nil)
	if err != nil {
		t.Fatalf("resolveTaskModeAdjustments: %v", err)
	}
	if adjustments != nil {
		t.Errorf("adjustments = %+v, want nil for unset task", adjustments)
	}
}

func TestResolveTaskModeAdjustments_SidecarDrivenMode(t *testing.T) {
	run := objectModeRun(taskmodes.SidecarDriven, "primary", map[string]string{
		"batchSize": "3",
	})

	sidecars := []resolvedSidecar{
		{
			skillPackName: "skill-scout",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Tools: []sympoziumv1alpha1.SidecarTool{
					{Name: "primary", Exec: []string{"node", "/app/dist/cli.js"}, Subcommand: "primary"},
				},
			},
		},
	}

	adjustments, err := resolveTaskModeAdjustments(run, sidecars)
	if err != nil {
		t.Fatalf("resolveTaskModeAdjustments: %v", err)
	}
	if len(adjustments) != 1 {
		t.Fatalf("len(adjustments) = %d, want 1; got %+v", len(adjustments), adjustments)
	}
	if adjustments[0].SkillPackName != "skill-scout" {
		t.Errorf("SkillPackName = %q, want %q", adjustments[0].SkillPackName, "skill-scout")
	}
}

// resolveTaskModeAdjustments returns an error listing supported modes when
// the requested mode is not registered.
func TestResolveTaskModeAdjustments_UnknownModeRejected(t *testing.T) {
	run := objectModeRun("bogus-mode", "primary", nil)

	_, err := resolveTaskModeAdjustments(run, nil)
	if err == nil {
		t.Fatal("resolveTaskModeAdjustments: expected error for unknown mode, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, `unknown task.mode "bogus-mode"`) {
		t.Errorf("error message %q does not name the unknown mode", msg)
	}
	if !strings.Contains(msg, taskmodes.SidecarDriven) {
		t.Errorf("error message %q does not list supported modes", msg)
	}
}

func TestResolveTaskModeAdjustments_HandlerValidationError(t *testing.T) {
	// Sidecar-driven mode requires Tool. Pass an empty Tool and expect the
	// handler's Validate() to reject the task.
	run := objectModeRun(taskmodes.SidecarDriven, "", nil)

	_, err := resolveTaskModeAdjustments(run, nil)
	if err == nil {
		t.Fatal("resolveTaskModeAdjustments: expected error for missing Tool, got nil")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("error message %q does not indicate validation failure", err.Error())
	}
}

// buildContainers applies the handler's ConfigureAgentContainer (AGENT_MODE)
// when the task is object form. String form leaves AGENT_MODE unset.
func TestBuildContainers_StringTaskNoAgentMode(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, _ := r.buildContainers(stringModeRun("do the thing"), false, nil, nil, nil, nil)

	if v, ok := envValueLocal(cs[0].Env, "AGENT_MODE"); ok {
		t.Errorf("AGENT_MODE set on string-form task: %q (want unset)", v)
	}
}

func TestBuildContainers_ObjectTaskSetsAgentModePromptServer(t *testing.T) {
	r := &AgentRunReconciler{}
	run := objectModeRun(taskmodes.SidecarDriven, "primary", nil)
	sidecars := []resolvedSidecar{
		{
			skillPackName: "skill-scout",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Tools: []sympoziumv1alpha1.SidecarTool{
					{Name: "primary", Exec: []string{"node", "/app/dist/cli.js"}},
				},
			},
		},
	}
	cs, _, err := r.buildContainers(run, false, nil, sidecars, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}

	if v, ok := envValueLocal(cs[0].Env, "AGENT_MODE"); !ok || v != "prompt-server" {
		t.Errorf("AGENT_MODE = (%q, %v), want (prompt-server, true)", v, ok)
	}
}

// buildContainers applies the handler's AdjustSidecars — the named sidecar
// gets its command overridden from the SkillPack tool's exec + subcommand.
func TestBuildContainers_ObjectTaskOverridesSidecarCommand(t *testing.T) {
	r := &AgentRunReconciler{}
	run := objectModeRun(taskmodes.SidecarDriven, "primary", map[string]string{
		"batchSize": "1",
	})
	sidecars := []resolvedSidecar{
		{
			skillPackName: "skill-scout",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Tools: []sympoziumv1alpha1.SidecarTool{
					{Name: "primary", Exec: []string{"node", "/app/dist/cli.js"}, Subcommand: "primary"},
				},
			},
		},
	}

	cs, _, _ := r.buildContainers(run, false, nil, sidecars, nil, nil)

	var found *corev1.Container
	for i := range cs {
		if cs[i].Name == "skill-skill-scout" {
			found = &cs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("named sidecar not found in containers")
	}
	if got, want := found.Command, []string{"node", "/app/dist/cli.js", "primary"}; !equalCmd(got, want) {
		t.Errorf("Command = %v, want %v", got, want)
	}
	if v, ok := envValueLocal(found.Env, "SYMPOZIUM_RUN_CONFIG_JSON"); !ok {
		t.Errorf("SYMPOZIUM_RUN_CONFIG_JSON not set on the named sidecar")
	} else if v != `{"batchSize":"1"}` {
		t.Errorf("SYMPOZIUM_RUN_CONFIG_JSON = %q, want %q", v, `{"batchSize":"1"}`)
	}
}

func equalCmd(a, b []string) bool {
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

// TaskSpec round-trip: string form → Marshal → Unmarshal preserves shape.
func TestTaskSpec_StringRoundTrip(t *testing.T) {
	in := &sympoziumv1alpha1.TaskSpec{}
	if err := in.UnmarshalJSON([]byte(`"do the thing"`)); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !in.IsString() {
		t.Errorf("IsString() = false after string unmarshal")
	}
	if in.GetPrompt() != "do the thing" {
		t.Errorf("GetPrompt() = %q, want %q", in.GetPrompt(), "do the thing")
	}

	out, err := in.MarshalJSON()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(out) != `"do the thing"` {
		t.Errorf("Marshal round-trip = %q, want %q", string(out), `"do the thing"`)
	}
}

func TestTaskSpec_ObjectRoundTrip(t *testing.T) {
	in := &sympoziumv1alpha1.TaskSpec{}
	raw := `{"mode":"sidecar-driven","tool":"primary","parameters":{"batchSize":"2"}}`
	if err := in.UnmarshalJSON([]byte(raw)); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !in.IsObject() {
		t.Errorf("IsObject() = false after object unmarshal")
	}
	if in.GetMode() != "sidecar-driven" {
		t.Errorf("GetMode() = %q, want %q", in.GetMode(), "sidecar-driven")
	}
	if in.Tool != "primary" {
		t.Errorf("Tool = %q, want %q", in.Tool, "primary")
	}
	if in.Parameters["batchSize"] != "2" {
		t.Errorf("Parameters[batchSize] = %q, want %q", in.Parameters["batchSize"], "2")
	}

	out, err := in.MarshalJSON()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(out)
	// Prompt must not appear in object form output.
	if strings.Contains(s, "prompt") {
		t.Errorf("Marshal object form includes 'prompt': %q", s)
	}
	if !strings.Contains(s, `"mode":"sidecar-driven"`) {
		t.Errorf("Marshal object form missing mode: %q", s)
	}
	if !strings.Contains(s, `"tool":"primary"`) {
		t.Errorf("Marshal object form missing tool: %q", s)
	}
	if !strings.Contains(s, `"parameters"`) {
		t.Errorf("Marshal object form missing parameters: %q", s)
	}
}

func TestTaskSpec_UnmarshalRejectsNonStringNonObject(t *testing.T) {
	in := &sympoziumv1alpha1.TaskSpec{}
	for _, raw := range []string{`123`, `true`, `null`, `[]`} {
		if err := in.UnmarshalJSON([]byte(raw)); err == nil {
			t.Errorf("Unmarshal(%q) returned nil error; expected rejection", raw)
		}
	}
}
