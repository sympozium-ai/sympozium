package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// tgiBackend serves HuggingFace models via Text Generation Inference (TGI).
// Like vLLM, TGI pulls from HuggingFace at startup and uses PVC as HF cache.
type tgiBackend struct{}

func (b *tgiBackend) NeedsDownload() bool { return false }
func (b *tgiBackend) DefaultImage() string {
	return "ghcr.io/huggingface/text-generation-inference:latest"
}
func (b *tgiBackend) DefaultPort() int32               { return 8080 }
func (b *tgiBackend) ContainerName() string            { return "tgi" }
func (b *tgiBackend) HealthPath() string               { return "/health" }
func (b *tgiBackend) ReadinessFailureThreshold() int32 { return 120 } // ~10 min

func (b *tgiBackend) BuildArgs(model *sympoziumv1alpha1.Model, port int32) []string {
	modelID := model.Spec.Source.ModelID

	ctxSize := model.Spec.Inference.ContextSize
	if ctxSize <= 0 {
		ctxSize = 4096
	}

	args := []string{
		"--model-id", modelID,
		"--port", fmt.Sprintf("%d", port),
		"--hostname", "0.0.0.0",
		"--max-input-length", fmt.Sprintf("%d", ctxSize),
	}

	// Shard across GPUs for multi-GPU setups.
	if model.Spec.Resources.GPU > 1 {
		args = append(args, "--num-shard", fmt.Sprintf("%d", model.Spec.Resources.GPU))
	}

	args = append(args, model.Spec.Inference.Args...)
	return args
}

func (b *tgiBackend) BuildEnv(model *sympoziumv1alpha1.Model) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "HF_HOME", Value: modelMountPath + "/hf-cache"},
	}

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

func (b *tgiBackend) VolumeMounts(_ *sympoziumv1alpha1.Model) []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: "model-storage", MountPath: modelMountPath},
	}
}

func (b *tgiBackend) Volumes(_ *sympoziumv1alpha1.Model, pvcName string) []corev1.Volume {
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

func (b *tgiBackend) ModelQuery(model *sympoziumv1alpha1.Model) string {
	return model.Spec.Source.ModelID
}
