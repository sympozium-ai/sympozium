package controller

import (
	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// inferenceBackend abstracts the differences between inference server types
// (llama-cpp, vLLM, TGI, custom) so the model controller can reconcile
// them uniformly.
type inferenceBackend interface {
	// NeedsDownload returns true if the backend requires a download Job
	// to fetch model weights before starting the server (e.g. GGUF download).
	NeedsDownload() bool

	// DefaultImage returns the default container image for this backend.
	DefaultImage() string

	// DefaultPort returns the default listen port for this backend.
	DefaultPort() int32

	// ContainerName returns the name for the inference container in the pod.
	ContainerName() string

	// BuildArgs returns the command-line arguments for the inference container.
	BuildArgs(model *sympoziumv1alpha1.Model, port int32) []string

	// BuildEnv returns additional environment variables for the inference container.
	BuildEnv(model *sympoziumv1alpha1.Model) []corev1.EnvVar

	// VolumeMounts returns volume mounts for the inference container.
	VolumeMounts(model *sympoziumv1alpha1.Model) []corev1.VolumeMount

	// Volumes returns pod volumes.
	Volumes(model *sympoziumv1alpha1.Model, pvcName string) []corev1.Volume

	// HealthPath returns the HTTP path for readiness/liveness probes.
	HealthPath() string

	// ReadinessFailureThreshold returns how many probe failures to tolerate
	// before marking the container unready. vLLM/TGI need longer due to
	// HuggingFace downloads at startup.
	ReadinessFailureThreshold() int32

	// ModelQuery returns a search string for llmfit placement probes.
	ModelQuery(model *sympoziumv1alpha1.Model) string
}

// newInferenceBackend returns the appropriate backend for the given server type.
func newInferenceBackend(serverType sympoziumv1alpha1.InferenceServerType) inferenceBackend {
	switch serverType {
	case sympoziumv1alpha1.InferenceServerVLLM:
		return &vllmBackend{}
	case sympoziumv1alpha1.InferenceServerTGI:
		return &tgiBackend{}
	case sympoziumv1alpha1.InferenceServerCustom:
		return &customBackend{}
	default:
		return &llamaCppBackend{}
	}
}

// customBackend is a minimal backend for user-provided inference server images.
type customBackend struct{}

func (b *customBackend) NeedsDownload() bool              { return false }
func (b *customBackend) DefaultImage() string             { return "" }
func (b *customBackend) DefaultPort() int32               { return 8080 }
func (b *customBackend) ContainerName() string            { return "inference" }
func (b *customBackend) HealthPath() string               { return "/health" }
func (b *customBackend) ReadinessFailureThreshold() int32 { return 60 }

func (b *customBackend) BuildArgs(model *sympoziumv1alpha1.Model, port int32) []string {
	return model.Spec.Inference.Args
}

func (b *customBackend) BuildEnv(model *sympoziumv1alpha1.Model) []corev1.EnvVar {
	return model.Spec.Inference.Env
}

func (b *customBackend) VolumeMounts(model *sympoziumv1alpha1.Model) []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: "model-storage", MountPath: modelMountPath},
	}
}

func (b *customBackend) Volumes(model *sympoziumv1alpha1.Model, pvcName string) []corev1.Volume {
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

func (b *customBackend) ModelQuery(model *sympoziumv1alpha1.Model) string {
	if model.Spec.Source.ModelID != "" {
		return model.Spec.Source.ModelID
	}
	return model.Name
}
