package controller

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/pkg/sidecartools"
)

// The canonical tool-executor script is mounted read-only into every skill
// sidecar at sidecartools.ExecutorScriptPath. Images that ship their own copy
// keep running it (the sidecar's command is never changed); the mount is what
// lets a SkillPack use a stock image by setting
// command: ["/sympozium/bin/tool-executor.sh"].
const toolExecutorVolumeName = "sympozium-tool-executor"

func toolExecutorConfigMapName(agentRun *sympoziumv1alpha1.AgentRun) string {
	return agentRun.Name + "-tool-executor"
}

// errToolExecutorConfigMapConflict marks a <run>-tool-executor ConfigMap the
// controller did not write for this run. Retrying cannot fix it.
var errToolExecutorConfigMapConflict = errors.New("tool-executor ConfigMap conflict")

func isToolExecutorConfigMapConflict(err error) bool {
	return errors.Is(err, errToolExecutorConfigMapConflict)
}

// ensureToolExecutorConfigMap creates the run's ConfigMap holding the
// tool-executor script. Like the sidecar-tools manifest it is scoped to the run,
// garbage-collected via the owner reference, and immutable: the script a
// sidecar executes is fixed by the controller for the life of the run.
//
// Every skill sidecar mounts this ConfigMap and may execute it, so one that
// already exists is used only if this run controls it and it holds this
// controller's script. Anything else — created by someone else under the run's
// name, or by an older controller — fails with errToolExecutorConfigMapConflict.
func (r *AgentRunReconciler) ensureToolExecutorConfigMap(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      toolExecutorConfigMapName(agentRun),
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"sympozium.ai/agent-run": agentRun.Name,
				"sympozium.ai/instance":  agentRun.Spec.AgentRef,
				"sympozium.ai/component": "tool-executor",
			},
		},
		Immutable: boolPtr(true),
		Data: map[string]string{
			sidecartools.ExecutorScriptName: sidecartools.ExecutorScript,
		},
	}
	if err := controllerutil.SetControllerReference(agentRun, cm, r.Scheme); err != nil {
		return err
	}
	err := r.Create(ctx, cm)
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating tool-executor ConfigMap: %w", err)
	}

	var existing corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKeyFromObject(cm), &existing); err != nil {
		return fmt.Errorf("reading existing tool-executor ConfigMap: %w", err)
	}
	if !metav1.IsControlledBy(&existing, agentRun) {
		return fmt.Errorf("%w: ConfigMap %s/%s already exists and is not owned by AgentRun %s; delete it or rename the run",
			errToolExecutorConfigMapConflict, existing.Namespace, existing.Name, agentRun.Name)
	}
	if existing.Data[sidecartools.ExecutorScriptName] != sidecartools.ExecutorScript {
		return fmt.Errorf("%w: ConfigMap %s/%s does not hold this controller's tool-executor script",
			errToolExecutorConfigMapConflict, existing.Namespace, existing.Name)
	}
	return nil
}

func toolExecutorVolume(agentRun *sympoziumv1alpha1.AgentRun) corev1.Volume {
	mode := int32(0o555)
	return corev1.Volume{
		Name: toolExecutorVolumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: toolExecutorConfigMapName(agentRun)},
				DefaultMode:          &mode,
				Optional:             boolPtr(true),
			},
		},
	}
}

func toolExecutorVolumeMount() corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      toolExecutorVolumeName,
		MountPath: sidecartools.ExecutorMountDir,
		ReadOnly:  true,
	}
}
