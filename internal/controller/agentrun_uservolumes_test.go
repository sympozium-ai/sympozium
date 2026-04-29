package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// User-supplied volumes/mounts on an AgentRun should be added to the pod
// spec and to the main agent container, while reserved names are skipped.
func TestBuildVolumes_UserVolumesAppended(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Volumes = []corev1.Volume{
		{
			Name: "vault-secrets",
			VolumeSource: corev1.VolumeSource{
				CSI: &corev1.CSIVolumeSource{
					Driver:   "secrets-store.csi.k8s.io",
					ReadOnly: boolPtr(true),
					VolumeAttributes: map[string]string{
						"secretProviderClass": "vault-agent-secrets",
					},
				},
			},
		},
		// Reserved name — must be skipped.
		{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	vols := r.buildVolumes(run, false, nil, nil)

	var found, reservedDup bool
	workspaceCount := 0
	for _, v := range vols {
		if v.Name == "vault-secrets" {
			if v.CSI == nil || v.CSI.Driver != "secrets-store.csi.k8s.io" {
				t.Errorf("vault-secrets volume mis-configured: %+v", v.VolumeSource)
			}
			found = true
		}
		if v.Name == "workspace" {
			workspaceCount++
		}
	}
	if !found {
		t.Error("vault-secrets volume not appended")
	}
	if workspaceCount != 1 {
		t.Errorf("reserved workspace volume duplicated: count=%d", workspaceCount)
		_ = reservedDup
	}
}

func TestBuildContainers_UserVolumeMountsAppended(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.VolumeMounts = []corev1.VolumeMount{
		{Name: "vault-secrets", MountPath: "/vault/secrets", ReadOnly: true},
		// Reserved — must be skipped.
		{Name: "workspace", MountPath: "/should-not-apply"},
	}

	containers, _ := r.buildContainers(run, false, nil, nil, nil)
	agent := containers[0]

	var hasVault bool
	for _, m := range agent.VolumeMounts {
		if m.Name == "vault-secrets" {
			if m.MountPath != "/vault/secrets" || !m.ReadOnly {
				t.Errorf("vault-secrets mount mis-configured: %+v", m)
			}
			hasVault = true
		}
		if m.Name == "workspace" && m.MountPath == "/should-not-apply" {
			t.Error("reserved workspace mount must be skipped")
		}
	}
	if !hasVault {
		t.Error("vault-secrets mount not applied to agent container")
	}
}

// SkillPack sidecars may also contribute pod-level volumes and per-container
// mounts (e.g. mounting Vault CSI secrets into a kubectl sidecar).
func TestBuildVolumes_SidecarVolumesAppendedAndDeduped(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	sidecars := []resolvedSidecar{
		{
			skillPackName: "k8s-ops",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "ghcr.io/example/skill-k8s-ops:latest",
				Volumes: []corev1.Volume{
					{
						Name: "vault-kubeconfig",
						VolumeSource: corev1.VolumeSource{
							CSI: &corev1.CSIVolumeSource{
								Driver: "secrets-store.csi.k8s.io",
							},
						},
					},
					// Reserved — must be skipped.
					{Name: "ipc"},
				},
			},
		},
		{
			skillPackName: "github-gitops",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "ghcr.io/example/skill-github:latest",
				// Duplicate of the previous sidecar's volume — should be deduped.
				Volumes: []corev1.Volume{
					{Name: "vault-kubeconfig"},
				},
			},
		},
	}

	vols := r.buildVolumes(run, false, sidecars, nil)

	count := 0
	ipcReserved := 0
	for _, v := range vols {
		if v.Name == "vault-kubeconfig" {
			count++
		}
		if v.Name == "ipc" {
			ipcReserved++
		}
	}
	if count != 1 {
		t.Errorf("vault-kubeconfig volume count = %d, want 1 (deduped)", count)
	}
	// Sympozium always adds an "ipc" emptyDir; we should still have exactly one.
	if ipcReserved != 1 {
		t.Errorf("ipc volume count = %d, want 1 (sidecar-supplied ipc must be dropped)", ipcReserved)
	}
}

func TestBuildContainers_SidecarVolumeMountsApplied(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	sidecars := []resolvedSidecar{
		{
			skillPackName: "k8s-ops",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "ghcr.io/example/skill-k8s-ops:latest",
				VolumeMounts: []corev1.VolumeMount{
					{Name: "vault-kubeconfig", MountPath: "/etc/kube", ReadOnly: true},
					// Reserved — must be skipped.
					{Name: "workspace", MountPath: "/should-not-apply"},
				},
			},
		},
	}

	containers, _ := r.buildContainers(run, false, nil, sidecars, nil)

	var skillContainer *corev1.Container
	for i := range containers {
		if containers[i].Name == "skill-k8s-ops" {
			skillContainer = &containers[i]
			break
		}
	}
	if skillContainer == nil {
		t.Fatal("skill-k8s-ops sidecar container not found")
	}

	var hasMount bool
	for _, m := range skillContainer.VolumeMounts {
		if m.Name == "vault-kubeconfig" && m.MountPath == "/etc/kube" {
			hasMount = true
		}
		if m.Name == "workspace" && m.MountPath == "/should-not-apply" {
			t.Error("reserved workspace mount must be skipped on sidecar")
		}
	}
	if !hasMount {
		t.Error("sidecar VolumeMount not applied")
	}
}
