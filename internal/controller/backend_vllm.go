package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// vllmBackend serves HuggingFace models via vLLM.
// vLLM pulls models from HuggingFace at startup (no download Job needed),
// but uses a PVC as an HF cache to avoid re-downloading on pod restarts.
type vllmBackend struct{}

func (b *vllmBackend) NeedsDownload() bool   { return false }
func (b *vllmBackend) DefaultImage() string  { return "vllm/vllm-openai:latest" }
func (b *vllmBackend) DefaultPort() int32    { return 8000 }
func (b *vllmBackend) ContainerName() string { return "vllm" }
func (b *vllmBackend) HealthPath() string    { return "/health" }

// ReadinessFailureThreshold is higher than llama-cpp because vLLM downloads
// model weights from HuggingFace at container startup. A 7B model takes
// several minutes on typical bandwidth.
func (b *vllmBackend) ReadinessFailureThreshold() int32 { return 120 } // ~10 min

func (b *vllmBackend) BuildArgs(model *sympoziumv1alpha1.Model, port int32) []string {
	modelID := model.Spec.Source.ModelID

	ctxSize := model.Spec.Inference.ContextSize
	if ctxSize <= 0 {
		ctxSize = 4096
	}

	args := []string{
		"--model", modelID,
		"--port", fmt.Sprintf("%d", port),
		"--host", "0.0.0.0",
		"--max-model-len", fmt.Sprintf("%d", ctxSize),
	}

	// Enable tensor parallelism for multi-GPU setups.
	if model.Spec.Resources.GPU > 1 {
		args = append(args, "--tensor-parallel-size", fmt.Sprintf("%d", model.Spec.Resources.GPU))
	}

	args = append(args, model.Spec.Inference.Args...)
	return args
}

func (b *vllmBackend) BuildEnv(model *sympoziumv1alpha1.Model) []corev1.EnvVar {
	env := []corev1.EnvVar{
		// Point HuggingFace cache to the PVC so downloads persist across restarts.
		{Name: "HF_HOME", Value: modelMountPath + "/hf-cache"},
	}

	// Mount HF token from secret if configured.
	if model.Spec.Inference.HuggingFaceTokenSecret != "" {
		env = append(env, corev1.EnvVar{
			Name: "HF_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: model.Spec.Inference.HuggingFaceTokenSecret,
					},
					Key:      "token",
					Optional: boolPtr(true),
				},
			},
		})
	}

	env = append(env, model.Spec.Inference.Env...)
	return env
}

func (b *vllmBackend) VolumeMounts(_ *sympoziumv1alpha1.Model) []corev1.VolumeMount {
	// Read-write: vLLM writes to the HF cache during model download.
	return []corev1.VolumeMount{
		{Name: "model-storage", MountPath: modelMountPath},
	}
}

func (b *vllmBackend) Volumes(_ *sympoziumv1alpha1.Model, pvcName string) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "model-storage",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
				},
			},
		},
	}
}

func (b *vllmBackend) ModelQuery(model *sympoziumv1alpha1.Model) string {
	return model.Spec.Source.ModelID
}
