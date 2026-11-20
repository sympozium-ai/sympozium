package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSkillTargets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   \t\n", nil},
		{"single", "github-gitops", []string{"github-gitops"}},
		{"multiple", "github-gitops,k8s-ops", []string{"github-gitops", "k8s-ops"}},
		{"mixed case and spaces", " Github-Gitops ,  K8S-OPS ", []string{"github-gitops", "k8s-ops"}},
		{"empty entries skipped", "github-gitops,,k8s-ops", []string{"github-gitops", "k8s-ops"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseSkillTargets(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("parseSkillTargets(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestResolveSkillTarget(t *testing.T) {
	inv := []string{"team-a-github-gitops", "team-a-k8s-ops"}

	cases := []struct {
		name      string
		input     string
		valid     []string
		want      string
		errSubstr string
	}{
		{name: "empty input lets any sidecar claim", input: "", valid: inv, want: ""},
		{name: "no inventory passes the input through", input: "Github-Gitops", valid: nil, want: "github-gitops"},
		{name: "exact match", input: "team-a-k8s-ops", valid: inv, want: "team-a-k8s-ops"},
		{name: "exact match ignores case", input: "Team-A-K8s-Ops", valid: inv, want: "team-a-k8s-ops"},
		{name: "unique short name", input: "github-gitops", valid: inv, want: "team-a-github-gitops"},
		{name: "unknown target lists the inventory", input: "atlassian", valid: inv, errSubstr: "Available targets: team-a-github-gitops, team-a-k8s-ops"},
		{name: "ambiguous short name", input: "ops", valid: []string{"alpha-ops", "beta-ops"}, errSubstr: "ambiguous"},
		{name: "suffix must follow a hyphen", input: "ops", valid: []string{"k8sops"}, errSubstr: "unknown target"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveSkillTarget(c.input, c.valid)
			if c.errSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), c.errSubstr) {
					t.Fatalf("resolveSkillTarget(%q) = %q, %v; want an error containing %q", c.input, got, err, c.errSubstr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("resolveSkillTarget(%q) = %q, %v; want %q", c.input, got, err, c.want)
			}
		})
	}
}

func executeCommandTargetSchema(t *testing.T) (ToolDef, map[string]any) {
	t.Helper()
	for _, td := range defaultTools() {
		if td.Name == ToolExecuteCommand {
			props, _ := td.Parameters["properties"].(map[string]any)
			target, _ := props["target"].(map[string]any)
			return td, target
		}
	}
	t.Fatal("execute_command not found in defaultTools()")
	return ToolDef{}, nil
}

// With sidecars attached, execute_command's target is constrained to them, and
// the description names them for providers that ignore enum.
func TestDefaultTools_AdvertisesAttachedTargets(t *testing.T) {
	prev := skillTargets
	t.Cleanup(func() { skillTargets = prev })
	skillTargets = []string{"github-gitops", "k8s-ops"}

	def, target := executeCommandTargetSchema(t)
	if enum, _ := target["enum"].([]string); !reflect.DeepEqual(enum, skillTargets) {
		t.Fatalf("target enum = %v, want %v", target["enum"], skillTargets)
	}
	for _, name := range skillTargets {
		if !strings.Contains(def.Description, name) {
			t.Errorf("execute_command description does not name target %q: %s", name, def.Description)
		}
	}
}

// Without sidecars, target stays a free-form string: an empty enum is an
// invalid schema for some providers.
func TestDefaultTools_NoEnumWithoutTargets(t *testing.T) {
	prev := skillTargets
	t.Cleanup(func() { skillTargets = prev })
	skillTargets = nil

	if _, target := executeCommandTargetSchema(t); target["enum"] != nil {
		t.Fatalf("target schema has an enum with no sidecars attached: %#v", target)
	}
}

// An unknown target fails immediately instead of writing a request no sidecar
// will ever claim.
func TestExecuteCommand_UnknownTargetFailsFast(t *testing.T) {
	prev := skillTargets
	t.Cleanup(func() { skillTargets = prev })
	skillTargets = []string{"k8s-ops"}

	got := executeToolCall(t.Context(), ToolExecuteCommand, `{"command":"kubectl get pods","target":"helm"}`)
	if !strings.HasPrefix(got, "Error: unknown target") || !strings.Contains(got, "k8s-ops") {
		t.Fatalf("execute_command with an unknown target = %q, want an immediate error listing k8s-ops", got)
	}
}
