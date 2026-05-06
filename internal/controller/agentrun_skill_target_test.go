package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// findEnv returns the value of the env var with the given name, or "" if not
// present. Used by the sidecar-target-routing tests.
func findEnv(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// When a SkillPack with a sidecar is attached, each generated sidecar
// container MUST receive a SYMPOZIUM_SKILL_PACK env var whose value matches
// the SkillPack's name. The tool-executor.sh in the sidecar uses this value
// to filter requests by their optional `target` field — without it, the
// race-by-mkdir behaviour silently routes commands to the wrong sidecar.
func TestBuildContainers_SidecarReceivesSkillPackEnv(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	sidecars := []resolvedSidecar{
		{
			skillPackName: "github-gitops",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "ghcr.io/example/skill-github-gitops:latest",
			},
		},
		{
			skillPackName: "k8s-ops",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "ghcr.io/example/skill-k8s-ops:latest",
			},
		},
	}

	containers, _ := r.buildContainers(run, false, nil, sidecars, nil)

	want := map[string]string{
		"skill-github-gitops": "github-gitops",
		"skill-k8s-ops":       "k8s-ops",
	}
	seen := map[string]string{}
	for _, c := range containers {
		if !strings.HasPrefix(c.Name, "skill-") {
			continue
		}
		val, ok := findEnv(c.Env, "SYMPOZIUM_SKILL_PACK")
		if !ok {
			t.Errorf("container %s missing SYMPOZIUM_SKILL_PACK env", c.Name)
			continue
		}
		seen[c.Name] = val
	}
	for name, val := range want {
		got, ok := seen[name]
		if !ok {
			t.Errorf("expected sidecar container %s not found", name)
			continue
		}
		if got != val {
			t.Errorf("%s SYMPOZIUM_SKILL_PACK = %q, want %q", name, got, val)
		}
	}
}

// The agent container MUST receive a SYMPOZIUM_SKILL_TARGETS env var listing
// the names of every attached skill sidecar, in spec order, comma-separated.
// The agent-runner uses this to advise the LLM (and validate) which `target`
// values are legal for the execute_command tool.
func TestBuildContainers_AgentReceivesSkillTargetsEnv(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	sidecars := []resolvedSidecar{
		{
			skillPackName: "github-gitops",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "ghcr.io/example/skill-github-gitops:latest",
			},
		},
		{
			skillPackName: "k8s-ops",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "ghcr.io/example/skill-k8s-ops:latest",
			},
		},
	}

	containers, _ := r.buildContainers(run, false, nil, sidecars, nil)

	if len(containers) == 0 || containers[0].Name != "agent" {
		t.Fatalf("expected first container to be 'agent', got: %+v", containers)
	}
	got, ok := findEnv(containers[0].Env, "SYMPOZIUM_SKILL_TARGETS")
	if !ok {
		t.Fatalf("agent container missing SYMPOZIUM_SKILL_TARGETS env")
	}
	if got != "github-gitops,k8s-ops" {
		t.Errorf("SYMPOZIUM_SKILL_TARGETS = %q, want %q", got, "github-gitops,k8s-ops")
	}
}

// When no skill sidecars are attached, SYMPOZIUM_SKILL_TARGETS MUST NOT be
// injected into the agent container. The agent-runner uses presence of the
// env to decide whether to advertise the optional `target` parameter; an
// empty value would clutter the prompt for single-skill or no-skill agents.
func TestBuildContainers_NoSkillTargetsEnvWithoutSidecars(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()

	containers, _ := r.buildContainers(run, false, nil, nil, nil)

	if len(containers) == 0 || containers[0].Name != "agent" {
		t.Fatalf("expected first container to be 'agent', got: %+v", containers)
	}
	if val, ok := findEnv(containers[0].Env, "SYMPOZIUM_SKILL_TARGETS"); ok {
		t.Errorf("SYMPOZIUM_SKILL_TARGETS should not be set when no sidecars; got %q", val)
	}
}
