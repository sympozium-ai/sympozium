package controller

import (
	"fmt"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// llamaCppBackend serves GGUF models via llama-server (llama.cpp).
// This is the default backend and preserves the original controller behavior.
type llamaCppBackend struct{}

func (b *llamaCppBackend) NeedsDownload() bool              { return true }
func (b *llamaCppBackend) DefaultImage() string             { return "ghcr.io/ggml-org/llama.cpp:server" }
func (b *llamaCppBackend) DefaultPort() int32               { return 8080 }
func (b *llamaCppBackend) ContainerName() string            { return "llama-server" }
func (b *llamaCppBackend) HealthPath() string               { return "/health" }
func (b *llamaCppBackend) ReadinessFailureThreshold() int32 { return 60 } // ~5 min

func (b *llamaCppBackend) BuildArgs(model *sympoziumv1alpha1.Model, port int32) []string {
	filename := model.Spec.Source.Filename
	if filename == "" {
		filename = "model.gguf"
	}

	ctxSize := model.Spec.Inference.ContextSize
	if ctxSize <= 0 {
		ctxSize = 4096
	}

	cpuStr := model.Spec.Resources.CPU
	if cpuStr == "" {
		cpuStr = "4"
	}
	cpuQty := resource.MustParse(cpuStr)
	threads := cpuQty.Value()
	if threads < 1 {
		threads = 1
	}

	args := []string{
		"--model", filepath.Join(modelMountPath, filename),
		"--port", fmt.Sprintf("%d", port),
		"--host", "0.0.0.0",
		"--ctx-size", fmt.Sprintf("%d", ctxSize),
		"--threads", fmt.Sprintf("%d", threads),
	}
	args = append(args, model.Spec.Inference.Args...)
	return args
}

func (b *llamaCppBackend) BuildEnv(model *sympoziumv1alpha1.Model) []corev1.EnvVar {
	return model.Spec.Inference.Env
}

func (b *llamaCppBackend) VolumeMounts(_ *sympoziumv1alpha1.Model) []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{Name: "model-storage", MountPath: modelMountPath, ReadOnly: true},
	}
}

func (b *llamaCppBackend) Volumes(_ *sympoziumv1alpha1.Model, pvcName string) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "model-storage",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
					ReadOnly:  true,
				},
			},
		},
	}
}

func (b *llamaCppBackend) ModelQuery(model *sympoziumv1alpha1.Model) string {
	return modelQueryFromURL(model.Spec.Source.URL)
}
