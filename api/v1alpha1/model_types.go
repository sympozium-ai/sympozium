package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModelCRDSpec defines the desired state of a Model.
// A Model declares a GGUF model to be downloaded, served via llama-server,
// and exposed as an OpenAI-compatible endpoint within the cluster.
type ModelCRDSpec struct {
	// Source defines where to obtain the model weights.
	Source ModelSource `json:"source"`

	// Storage configures the PersistentVolumeClaim for model weights.
	// +optional
	Storage ModelStorage `json:"storage,omitempty"`

	// Inference configures the inference server (llama-server).
	// +optional
	Inference InferenceSpec `json:"inference,omitempty"`

	// Resources defines compute requirements for the inference server.
	// +optional
	Resources ModelResources `json:"resources,omitempty"`

	// NodeSelector constrains the inference server to nodes with matching labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations for the inference server pod.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// ModelSource defines where to obtain the model weights.
type ModelSource struct {
	// URL is the download URL for the GGUF model file.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// Filename is the target filename on the PVC.
	// +kubebuilder:default="model.gguf"
	// +optional
	Filename string `json:"filename,omitempty"`
}

// ModelStorage configures the PersistentVolumeClaim for model weights.
type ModelStorage struct {
	// Size is the PVC storage size (e.g. "10Gi").
	// +kubebuilder:default="10Gi"
	// +optional
	Size string `json:"size,omitempty"`

	// StorageClass is the PVC storage class. Empty uses the cluster default.
	// +optional
	StorageClass string `json:"storageClass,omitempty"`
}

// InferenceSpec configures the inference server.
type InferenceSpec struct {
	// Image is the llama-server container image.
	// +kubebuilder:default="ghcr.io/ggml-org/llama.cpp:server"
	// +optional
	Image string `json:"image,omitempty"`

	// Port is the inference server listen port.
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`

	// ContextSize is the maximum context window size in tokens.
	// +kubebuilder:default=4096
	// +optional
	ContextSize int `json:"contextSize,omitempty"`

	// Args are additional command-line arguments for the inference server.
	// +optional
	Args []string `json:"args,omitempty"`
}

// ModelResources defines compute requirements for the inference server.
type ModelResources struct {
	// GPU is the number of GPUs to request (nvidia.com/gpu).
	// Set to 0 for CPU-only inference. Defaults to 0.
	// +kubebuilder:default=0
	// +optional
	GPU int `json:"gpu,omitempty"`

	// Memory is the memory request/limit for the inference container (e.g. "16Gi").
	// +kubebuilder:default="16Gi"
	// +optional
	Memory string `json:"memory,omitempty"`

	// CPU is the CPU request/limit for the inference container (e.g. "4").
	// +kubebuilder:default="4"
	// +optional
	CPU string `json:"cpu,omitempty"`
}

// ModelPhase represents the lifecycle phase of a Model.
type ModelPhase string

const (
	ModelPhasePending     ModelPhase = "Pending"
	ModelPhaseDownloading ModelPhase = "Downloading"
	ModelPhaseLoading     ModelPhase = "Loading"
	ModelPhaseReady       ModelPhase = "Ready"
	ModelPhaseFailed      ModelPhase = "Failed"
)

// ModelStatus defines the observed state of a Model.
type ModelStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase ModelPhase `json:"phase,omitempty"`

	// Endpoint is the cluster-internal OpenAI-compatible API URL.
	// Populated when phase is Ready.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Message provides human-readable details about the current phase.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".status.endpoint"
// +kubebuilder:printcolumn:name="GPU",type="integer",JSONPath=".spec.resources.gpu"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Model is the Schema for the models API.
// A Model declares a GGUF model to be downloaded, served via llama-server,
// and exposed as an OpenAI-compatible inference endpoint within the cluster.
type Model struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelCRDSpec `json:"spec,omitempty"`
	Status ModelStatus  `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ModelList contains a list of Model.
type ModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Model `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Model{}, &ModelList{})
}
