package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InferenceServerType selects the inference server backend.
type InferenceServerType string

const (
	InferenceServerLlamaCpp InferenceServerType = "llama-cpp"
	InferenceServerVLLM     InferenceServerType = "vllm"
	InferenceServerTGI      InferenceServerType = "tgi"
	InferenceServerCustom   InferenceServerType = "custom"
)

// ModelCRDSpec defines the desired state of a Model.
// A Model declares a model to be served via an inference server (llama-server,
// vLLM, TGI, or custom) and exposed as an OpenAI-compatible endpoint.
type ModelCRDSpec struct {
	// Source defines where to obtain the model weights.
	Source ModelSource `json:"source"`

	// Storage configures the PersistentVolumeClaim for model weights.
	// +optional
	Storage ModelStorage `json:"storage,omitempty"`

	// Inference configures the inference server.
	// +optional
	Inference InferenceSpec `json:"inference,omitempty"`

	// Resources defines compute requirements for the inference server.
	// +optional
	Resources ModelResources `json:"resources,omitempty"`

	// NodeSelector constrains the inference server to nodes with matching labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Placement configures how the controller selects a node for the model.
	// +optional
	Placement ModelPlacement `json:"placement,omitempty"`

	// Tolerations for the inference server pod.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// ModelSource defines where to obtain the model weights.
type ModelSource struct {
	// URL is the download URL for a model file (e.g. GGUF).
	// Required when serverType is "llama-cpp" or unset.
	// +optional
	URL string `json:"url,omitempty"`

	// Filename is the target filename on the PVC.
	// +kubebuilder:default="model.gguf"
	// +optional
	Filename string `json:"filename,omitempty"`

	// ModelID is a HuggingFace model identifier
	// (e.g. "meta-llama/Llama-3.1-8B-Instruct").
	// Required for vllm and tgi server types. These servers pull
	// directly from HuggingFace at container startup.
	// +optional
	ModelID string `json:"modelID,omitempty"`

	// SHA256 is the expected SHA-256 hash of the downloaded model file.
	// When set, the download job verifies the file integrity after download
	// and fails if the checksum does not match. Recommended for production use.
	// +optional
	SHA256 string `json:"sha256,omitempty"`
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
	// ServerType selects the inference server backend.
	// Defaults to "llama-cpp" for backward compatibility.
	// +kubebuilder:default="llama-cpp"
	// +kubebuilder:validation:Enum=llama-cpp;vllm;tgi;custom
	// +optional
	ServerType InferenceServerType `json:"serverType,omitempty"`

	// Image is the container image for the inference server.
	// Defaults vary by serverType: llama-cpp uses ghcr.io/ggml-org/llama.cpp:server,
	// vllm uses vllm/vllm-openai:latest, tgi uses ghcr.io/huggingface/text-generation-inference:latest.
	// +optional
	Image string `json:"image,omitempty"`

	// Port is the inference server listen port.
	// Defaults: llama-cpp=8080, vllm=8000, tgi=8080.
	// +optional
	Port int32 `json:"port,omitempty"`

	// ContextSize is the maximum context window size in tokens.
	// Maps to --ctx-size (llama-cpp), --max-model-len (vllm), or --max-input-length (tgi).
	// +kubebuilder:default=4096
	// +optional
	ContextSize int `json:"contextSize,omitempty"`

	// Args are additional command-line arguments for the inference server.
	// +optional
	Args []string `json:"args,omitempty"`

	// Env are additional environment variables for the inference server container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// HuggingFaceTokenSecret references a Secret containing a HuggingFace API token.
	// The controller mounts the "token" key as the HF_TOKEN environment variable,
	// allowing vllm and tgi to access gated models (e.g. Llama, Mistral).
	// +optional
	HuggingFaceTokenSecret string `json:"huggingFaceTokenSecret,omitempty"`
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

// PlacementMode controls how the controller selects a node for the model.
type PlacementMode string

const (
	PlacementManual PlacementMode = "manual"
	PlacementAuto   PlacementMode = "auto"
)

// ModelPlacement configures node selection for the inference server.
type ModelPlacement struct {
	// Mode is "auto" or "manual". In auto mode the controller uses llmfit
	// probes to select the best-fit node. Defaults to "manual".
	// +kubebuilder:default="manual"
	// +kubebuilder:validation:Enum=auto;manual
	// +optional
	Mode PlacementMode `json:"mode,omitempty"`
}

// ModelPhase represents the lifecycle phase of a Model.
type ModelPhase string

const (
	ModelPhasePlacing     ModelPhase = "Placing"
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

	// PlacedNode is the node selected by auto-placement. Populated when
	// placement mode is "auto" and placement succeeds.
	// +optional
	PlacedNode string `json:"placedNode,omitempty"`

	// PlacementScore is the llmfit fitness score for the selected node.
	// +optional
	PlacementScore int `json:"placementScore,omitempty"`

	// PlacementMessage provides details about the placement decision.
	// +optional
	PlacementMessage string `json:"placementMessage,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Server",type="string",JSONPath=".spec.inference.serverType"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".status.endpoint"
// +kubebuilder:printcolumn:name="GPU",type="integer",JSONPath=".spec.resources.gpu"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Model is the Schema for the models API.
// A Model declares a model to be served via an inference server (llama-server,
// vLLM, TGI, or custom) and exposed as an OpenAI-compatible endpoint.
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
