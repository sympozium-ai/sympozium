package toolpolicy

import (
	"reflect"
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/pkg/sidecartools"
)

func TestWithGating(t *testing.T) {
	rules := []sympoziumv1alpha1.ToolGatingRule{
		{Tool: "read_file", Action: "allow"},
		{Tool: "list_directory", Action: "allow"},
		{Tool: "fetch_url", Action: "deny"},
	}
	cases := []struct {
		name   string
		run    *sympoziumv1alpha1.ToolPolicySpec
		gating *sympoziumv1alpha1.ToolGatingSpec
		want   *sympoziumv1alpha1.ToolPolicySpec
	}{
		{"no gating keeps the run's policy", &sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"x"}}, nil,
			&sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"x"}}},
		{"default allow adds the policy's denies to a run without a policy", nil,
			&sympoziumv1alpha1.ToolGatingSpec{DefaultAction: "allow", Rules: rules},
			&sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"fetch_url"}}},
		{"default allow keeps the run's own lists", &sympoziumv1alpha1.ToolPolicySpec{Allow: []string{"read_file"}, Deny: []string{"write_file", "fetch_url"}},
			&sympoziumv1alpha1.ToolGatingSpec{DefaultAction: "allow", Rules: rules},
			&sympoziumv1alpha1.ToolPolicySpec{Allow: []string{"read_file"}, Deny: []string{"write_file", "fetch_url"}}},
		{"default deny allows only the policy's allowed tools", nil,
			&sympoziumv1alpha1.ToolGatingSpec{DefaultAction: "deny", Rules: rules},
			&sympoziumv1alpha1.ToolPolicySpec{Allow: []string{"read_file", "list_directory"}, Deny: []string{"fetch_url"}}},
		{"default deny narrows the run's allow list, never widens it", &sympoziumv1alpha1.ToolPolicySpec{Allow: []string{"read_file", "execute_command"}},
			&sympoziumv1alpha1.ToolGatingSpec{DefaultAction: "deny", Rules: rules},
			&sympoziumv1alpha1.ToolPolicySpec{Allow: []string{"read_file"}, Deny: []string{"fetch_url"}}},
		{"default deny with nothing allowed denies every tool", &sympoziumv1alpha1.ToolPolicySpec{Allow: []string{"execute_command"}},
			&sympoziumv1alpha1.ToolGatingSpec{DefaultAction: "deny", Rules: rules},
			&sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"fetch_url", sidecartools.DenyAllTools}}},
		{"an empty default-allow policy leaves no filter", nil,
			&sympoziumv1alpha1.ToolGatingSpec{DefaultAction: "allow"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithGating(c.run, c.gating); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("WithGating = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestWithGatingDoesNotMutateTheRun(t *testing.T) {
	run := &sympoziumv1alpha1.ToolPolicySpec{Deny: make([]string, 1, 4)}
	run.Deny[0] = "write_file"
	WithGating(run, &sympoziumv1alpha1.ToolGatingSpec{Rules: []sympoziumv1alpha1.ToolGatingRule{{Tool: "fetch_url", Action: "deny"}}})
	if len(run.Deny) != 1 || run.Deny[:2][1] != "" {
		t.Fatalf("run deny list was modified: %v", run.Deny[:2])
	}
}
