package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Transport",type=string,JSONPath=`.spec.transportType`
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Tools",type=integer,JSONPath=`.status.toolCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPServer manages the lifecycle of an MCP server in the cluster.
// Supports stdio (with HTTP adapter), HTTP, and external transport modes.
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MCPServerSpec   `json:"spec"`
	Status            MCPServerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}

type MCPServerSpec struct {
	// TransportType: "stdio" or "http".
	// stdio servers are wrapped with an HTTP adapter automatically.
	// +kubebuilder:validation:Enum=stdio;http
	TransportType string `json:"transportType"`

	// URL for external/pre-existing servers (no deployment created).
	// +optional
	URL string `json:"url,omitempty"`

	// Deployment spec for managed servers.
	// +optional
	Deployment *MCPServerDeployment `json:"deployment,omitempty"`

	// ToolsPrefix is prepended to tool names to avoid collisions.
	// +kubebuilder:validation:MinLength=1
	ToolsPrefix string `json:"toolsPrefix"`

	// Timeout is the per-request timeout in seconds.
	// +kubebuilder:default=30
	// +optional
	Timeout int `json:"timeout,omitempty"`

	// Replicas for the MCP server deployment.
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Suspended prevents the controller from creating or maintaining
	// Deployment and Service resources for this MCPServer. Existing
	// deployments are scaled to zero when suspended. Defaults to false.
	// +optional
	Suspended bool `json:"suspended,omitempty"`

	// ToolsAllow lists tool names (without prefix) to expose. If set, only these tools are registered.
	// +optional
	ToolsAllow []string `json:"toolsAllow,omitempty"`

	// ToolsDeny lists tool names (without prefix) to hide. Applied after toolsAllow.
	// +optional
	ToolsDeny []string `json:"toolsDeny,omitempty"`
}

type MCPServerDeployment struct {
	// Image is the container image for the MCP server.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// ImagePullPolicy overrides the MCP server container image pull policy.
	// Valid values are "Always", "IfNotPresent", or "Never". Defaults to
	// "IfNotPresent" when unset.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Cmd overrides the container command (for stdio: the MCP server binary).
	// +optional
	Cmd string `json:"cmd,omitempty"`

	// Args are command arguments.
	// +optional
	Args []string `json:"args,omitempty"`

	// Port is the HTTP port (http transport only). Defaults to 8080.
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`

	// Env are environment variables for the MCP server container.
	// +optional
	Env map[string]string `json:"env,omitempty"`

	// SecretRefs are Kubernetes Secrets whose keys are injected as env vars.
	// +optional
	SecretRefs []MCPSecretRef `json:"secretRefs,omitempty"`

	// Resources for the container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// ServiceAccountName for RBAC access.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// Volumes are additional pod-level volumes to attach to the MCP
	// server Deployment. Use this together with VolumeMounts to surface
	// secrets via CSI drivers (e.g. Vault CSI), config files via
	// ConfigMaps, etc. The reserved volume name `adapter-bin` is used
	// internally for stdio transports and must not be reused.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts are additional volume mounts applied to the MCP
	// server container. Names must reference entries in Volumes (or any
	// other volume defined on the pod).
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

type MCPSecretRef struct {
	// Name of the Kubernetes Secret.
	Name string `json:"name"`
}

type MCPServerStatus struct {
	// Ready indicates the MCP server is serving requests.
	Ready bool `json:"ready"`

	// URL is the resolved Service URL.
	// +optional
	URL string `json:"url,omitempty"`

	// ToolCount is the number of discovered tools.
	// +optional
	ToolCount int `json:"toolCount,omitempty"`

	// Tools lists discovered tool names.
	// +optional
	Tools []string `json:"tools,omitempty"`

	// Conditions represent the latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func init() {
	SchemeBuilder.Register(&MCPServer{}, &MCPServerList{})
}
