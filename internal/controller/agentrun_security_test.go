package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ── Fix 7: Skill sidecars get restricted security context ────────────────────

func TestBuildContainers_SkillSidecar_DefaultSecurityContext(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()

	sidecars := []resolvedSidecar{
		{
			skillPackName: "test-skill",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "ghcr.io/sympozium-ai/sympozium/skill-test:latest",
			},
		},
	}

	containers, _ := r.buildContainers(run, false, nil, sidecars, nil)

	// Find the skill sidecar container
	var skillContainer *corev1.Container
	for i := range containers {
		if containers[i].Name == "skill-test-skill" {
			skillContainer = &containers[i]
			break
		}
	}
	if skillContainer == nil {
		t.Fatal("skill sidecar container not found")
	}

	sc := skillContainer.SecurityContext
	if sc == nil {
		t.Fatal("skill sidecar should have a security context")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("skill sidecar: AllowPrivilegeEscalation should be false")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
		t.Error("skill sidecar: should drop ALL capabilities")
	} else if sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("skill sidecar: should drop ALL, got %v", sc.Capabilities.Drop)
	}
}

func TestBuildContainers_SkillSidecar_HostAccess_OverridesDefault(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()

	sidecars := []resolvedSidecar{
		{
			skillPackName: "priv-skill",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "ghcr.io/sympozium-ai/sympozium/skill-priv:latest",
				HostAccess: &sympoziumv1alpha1.HostAccessSpec{
					Enabled:    true,
					Privileged: true,
				},
			},
		},
	}

	containers, _ := r.buildContainers(run, false, nil, sidecars, nil)

	var skillContainer *corev1.Container
	for i := range containers {
		if containers[i].Name == "skill-priv-skill" {
			skillContainer = &containers[i]
			break
		}
	}
	if skillContainer == nil {
		t.Fatal("privileged skill sidecar container not found")
	}

	sc := skillContainer.SecurityContext
	if sc == nil {
		t.Fatal("privileged skill sidecar should have a security context")
	}
	if sc.Privileged == nil || !*sc.Privileged {
		t.Error("privileged skill sidecar: Privileged should be true")
	}
}

// ── Fix 9: Auth secret keys are individually injected ────────────────────────

func TestBuildContainers_AuthSecret_IndividualKeys(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Model.AuthSecretRef = "my-auth-secret"

	containers, _ := r.buildContainers(run, false, nil, nil, nil)
	agentContainer := containers[0]

	// Should NOT have envFrom (wholesale injection)
	if len(agentContainer.EnvFrom) > 0 {
		t.Error("agent container should NOT use envFrom for auth secrets")
	}

	// Should have individual env vars with secretKeyRef
	secretKeyEnvCount := 0
	for _, env := range agentContainer.Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			if env.ValueFrom.SecretKeyRef.Name == "my-auth-secret" {
				secretKeyEnvCount++
				// Each should be optional
				if env.ValueFrom.SecretKeyRef.Optional == nil || !*env.ValueFrom.SecretKeyRef.Optional {
					t.Errorf("secret key ref for %q should be optional", env.Name)
				}
			}
		}
	}

	if secretKeyEnvCount == 0 {
		t.Error("no auth secret key refs found in agent container env")
	}
	if secretKeyEnvCount != len(allowedAuthSecretKeys) {
		t.Errorf("expected %d auth secret keys, got %d", len(allowedAuthSecretKeys), secretKeyEnvCount)
	}
}

func TestBuildContainers_AuthSecret_OnlyAllowedKeys(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Model.AuthSecretRef = "my-auth-secret"

	containers, _ := r.buildContainers(run, false, nil, nil, nil)
	agentContainer := containers[0]

	allowed := make(map[string]bool)
	for _, k := range allowedAuthSecretKeys {
		allowed[k] = true
	}

	for _, env := range agentContainer.Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == "my-auth-secret" {
			if !allowed[env.Name] {
				t.Errorf("unexpected auth secret key %q injected", env.Name)
			}
		}
	}
}

func TestBuildContainers_NoAuthSecret_NoSecretKeyRefs(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Model.AuthSecretRef = ""

	containers, _ := r.buildContainers(run, false, nil, nil, nil)
	agentContainer := containers[0]

	for _, env := range agentContainer.Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			t.Errorf("no secret key refs should be present when AuthSecretRef is empty, found %q", env.Name)
		}
	}
}

// ── Fix 10 (controller side): deniedEnvVarKeys constant ──────────────────────

func TestDeniedEnvVarKeys_ContainsDangerousVars(t *testing.T) {
	dangerous := []string{"PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "HOME"}
	for _, key := range dangerous {
		if !deniedEnvVarKeys[key] {
			t.Errorf("deniedEnvVarKeys should contain %q", key)
		}
	}
}
