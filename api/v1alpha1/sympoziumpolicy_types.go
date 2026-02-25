package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SympoziumPolicySpec defines the desired state of SympoziumPolicy.
// Policies enforce governance over agent behaviour, sandbox isolation,
// resource limits, and tool access.
type SympoziumPolicySpec struct {
	// SandboxPolicy defines sandbox requirements.
	// +optional
	SandboxPolicy *SandboxPolicySpec `json:"sandboxPolicy,omitempty"`

	// SubagentPolicy defines sub-agent depth and concurrency limits.
	// +optional
	SubagentPolicy *SubagentPolicySpec `json:"subagentPolicy,omitempty"`

	// ToolGating defines tool access rules.
	// +optional
	ToolGating *ToolGatingSpec `json:"toolGating,omitempty"`

	// FeatureGates controls which features are enabled/disabled.
	// +optional
	FeatureGates map[string]bool `json:"featureGates,omitempty"`

	// NetworkPolicy defines network isolation settings.
	// +optional
	NetworkPolicy *NetworkPolicySpec `json:"networkPolicy,omitempty"`
}

// SandboxPolicySpec defines sandbox enforcement.
type SandboxPolicySpec struct {
	// Required makes sandbox mandatory.
	Required bool `json:"required"`

	// DefaultImage is the fallback sandbox image.
	// +optional
	DefaultImage string `json:"defaultImage,omitempty"`

	// MaxCPU is the maximum CPU allowed for sandbox containers.
	// +optional
	MaxCPU string `json:"maxCPU,omitempty"`

	// MaxMemory is the maximum memory allowed for sandbox containers.
	// +optional
	MaxMemory string `json:"maxMemory,omitempty"`

	// AllowHostMounts controls whether host path mounts are allowed.
	// +kubebuilder:default=false
	AllowHostMounts bool `json:"allowHostMounts,omitempty"`
}

// SubagentPolicySpec defines sub-agent limits.
type SubagentPolicySpec struct {
	// MaxDepth is the maximum nesting depth for sub-agents.
	// +kubebuilder:default=3
	MaxDepth int `json:"maxDepth,omitempty"`

	// MaxConcurrent is the maximum concurrent sub-agent runs.
	// +kubebuilder:default=5
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
}

// ToolGatingSpec defines tool access rules.
type ToolGatingSpec struct {
	// DefaultAction is the default action for unmatched tools (allow, deny, ask).
	// +kubebuilder:default="allow"
	DefaultAction string `json:"defaultAction,omitempty"`

	// Rules is the list of tool-specific rules.
	Rules []ToolGatingRule `json:"rules,omitempty"`
}

// ToolGatingRule defines a rule for a specific tool.
type ToolGatingRule struct {
	// Tool is the tool name this rule applies to.
	Tool string `json:"tool"`

	// Action is the action to take (allow, deny, ask).
	Action string `json:"action"`
}

// NetworkPolicySpec defines network isolation settings.
type NetworkPolicySpec struct {
	// DenyAll denies all network access from agent pods.
	// +kubebuilder:default=true
	DenyAll bool `json:"denyAll,omitempty"`

	// AllowDNS allows DNS resolution.
	// +kubebuilder:default=true
	AllowDNS bool `json:"allowDNS,omitempty"`

	// AllowEventBus allows communication with the event bus.
	// +kubebuilder:default=true
	AllowEventBus bool `json:"allowEventBus,omitempty"`

	// AllowedEgress defines allowed egress endpoints.
	// +optional
	AllowedEgress []EgressRule `json:"allowedEgress,omitempty"`
}

// EgressRule defines an allowed egress endpoint.
type EgressRule struct {
	// Host is the allowed destination host or CIDR.
	Host string `json:"host"`

	// Port is the allowed destination port.
	// +optional
	Port int `json:"port,omitempty"`
}

// SympoziumPolicyStatus defines the observed state of SympoziumPolicy.
type SympoziumPolicyStatus struct {
	// BoundInstances is the number of SympoziumInstances bound to this policy.
	// +optional
	BoundInstances int `json:"boundInstances,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Bound",type="integer",JSONPath=".status.boundInstances"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SympoziumPolicy is the Schema for the sympoziumpolicies API.
// It enforces governance, sandbox requirements, network isolation,
// and tool access for bound SympoziumInstances.
type SympoziumPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SympoziumPolicySpec   `json:"spec,omitempty"`
	Status SympoziumPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SympoziumPolicyList contains a list of SympoziumPolicy.
type SympoziumPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SympoziumPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SympoziumPolicy{}, &SympoziumPolicyList{})
}
