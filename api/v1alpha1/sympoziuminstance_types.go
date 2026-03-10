package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SympoziumInstanceSpec defines the desired state of a SympoziumInstance.
// Each user or tenant gets a SympoziumInstance that declares their desired channels,
// agents, and policy bindings.
type SympoziumInstanceSpec struct {
	// Channels this instance connects to.
	// +optional
	Channels []ChannelSpec `json:"channels,omitempty"`

	// Agent configuration.
	Agents AgentsSpec `json:"agents"`

	// Skills to mount (from SkillPack CRDs or ConfigMaps).
	// +optional
	Skills []SkillRef `json:"skills,omitempty"`

	// PolicyRef references the SympoziumPolicy that applies to this instance.
	// +optional
	PolicyRef string `json:"policyRef,omitempty"`

	// AuthRefs references secrets containing AI provider credentials.
	// +optional
	AuthRefs []SecretRef `json:"authRefs,omitempty"`

	// Memory configures persistent memory for this instance.
	// When enabled, a MEMORY.md ConfigMap is managed and mounted into agent pods.
	// +optional
	Memory *MemorySpec `json:"memory,omitempty"`

	// Observability configures OpenTelemetry for agent pods spawned by this instance.
	// When nil, inherits from Helm chart global values.
	// +optional
	Observability *ObservabilitySpec `json:"observability,omitempty"`

	// Deprecated: Use the "web-endpoint" SkillPack in Skills instead.
	// WebEndpoint exposes this agent as an HTTP API (OpenAI-compatible + MCP).
	// When nil or Enabled is false, no web-proxy infrastructure is deployed.
	// +optional
	WebEndpoint *WebEndpointSpec `json:"webEndpoint,omitempty"`
}

// MemorySpec configures persistent memory for a SympoziumInstance.
type MemorySpec struct {
	// Enabled indicates whether persistent memory is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// MaxSizeKB caps the memory ConfigMap size in kilobytes.
	// +kubebuilder:default=256
	// +optional
	MaxSizeKB int `json:"maxSizeKB,omitempty"`

	// SystemPrompt is injected into every agent run for this instance
	// to instruct the agent on how to use memory.
	// +optional
	SystemPrompt string `json:"systemPrompt,omitempty"`
}

// ObservabilitySpec configures OpenTelemetry for agent runs.
type ObservabilitySpec struct {
	// Enabled turns OpenTelemetry tracing/metrics on for this instance.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// OTLPEndpoint is the collector endpoint (for example:
	// "otel-collector.observability.svc:4317" for gRPC or
	// "http://otel-collector.observability.svc:4318" for HTTP/protobuf).
	// +optional
	OTLPEndpoint string `json:"otlpEndpoint,omitempty"`

	// OTLPProtocol is "grpc" or "http/protobuf".
	// +kubebuilder:validation:Enum=grpc;http/protobuf
	// +optional
	OTLPProtocol string `json:"otlpProtocol,omitempty"`

	// ServiceName overrides the OTel service name (default: "sympozium-agent-runner").
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// Headers are additional OTLP export headers (e.g., auth tokens).
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// HeadersSecretRef references a Secret containing OTLP export headers.
	// +optional
	HeadersSecretRef string `json:"headersSecretRef,omitempty"`

	// SamplingRatio is the trace sampling probability as a string ("0.0" to "1.0").
	// Parsed to float64 at runtime. String type avoids controller-gen float issues.
	// +optional
	SamplingRatio string `json:"samplingRatio,omitempty"`

	// ResourceAttributes are additional OTel resource attributes (key/value).
	// +optional
	ResourceAttributes map[string]string `json:"resourceAttributes,omitempty"`
}

// WebEndpointSpec configures the web-proxy that exposes an agent as an HTTP API.
// When the field is absent or Enabled is false, the controller deploys nothing.
// Infrastructure is only created when Enabled is explicitly set to true.
type WebEndpointSpec struct {
	// Enabled is the master switch. When false (or when WebEndpoint is nil),
	// no web-proxy Deployment, Service, HTTPRoute, or Secret is created.
	// When toggled from true→false, the controller tears down all resources.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// Hostname for this instance's HTTPRoute (e.g. "alice.sympozium.example.com").
	// If empty, defaults to "<instance-name>.<gateway.baseDomain>" from Helm values.
	// +optional
	Hostname string `json:"hostname,omitempty"`

	// AuthSecretRef references a K8s Secret containing the API key.
	// The Secret must have a key named "api-key".
	// If empty, one is auto-generated with a random sk-<hex> key.
	// +optional
	AuthSecretRef string `json:"authSecretRef,omitempty"`

	// RateLimit defines request rate limiting.
	// +optional
	RateLimit *RateLimitSpec `json:"rateLimit,omitempty"`
}

// RateLimitSpec defines rate limiting for the web endpoint.
type RateLimitSpec struct {
	// RequestsPerMinute is the maximum requests per minute per API key.
	// +kubebuilder:default=60
	RequestsPerMinute int `json:"requestsPerMinute,omitempty"`

	// BurstSize allows short bursts above the rate limit.
	// +kubebuilder:default=10
	BurstSize int `json:"burstSize,omitempty"`
}

// ChannelSpec defines a channel connection.
type ChannelSpec struct {
	// Type is the channel type (telegram, whatsapp, discord, slack).
	Type string `json:"type"`

	// ConfigRef references the secret containing channel credentials.
	// Optional for channels that use alternative authentication (e.g. WhatsApp QR pairing).
	ConfigRef SecretRef `json:"configRef,omitempty"`
}

// AgentsSpec defines agent configuration.
type AgentsSpec struct {
	// Default is the default agent configuration.
	Default AgentConfig `json:"default"`
}

// AgentConfig defines configuration for an agent.
type AgentConfig struct {
	// Model is the LLM model to use.
	Model string `json:"model"`

	// BaseURL overrides the provider's default API endpoint.
	// Use for OpenAI-compatible providers (GitHub Copilot, Azure OpenAI, Ollama, etc.).
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// Thinking is the thinking mode (off, low, medium, high).
	// +optional
	Thinking string `json:"thinking,omitempty"`

	// Sandbox configuration.
	// +optional
	Sandbox *SandboxSpec `json:"sandbox,omitempty"`

	// Subagents configuration.
	// +optional
	Subagents *SubagentsSpec `json:"subagents,omitempty"`
}

// SandboxSpec defines sandbox configuration.
type SandboxSpec struct {
	// Enabled indicates whether sandboxing is enabled.
	Enabled bool `json:"enabled"`

	// Image is the sandbox container image.
	// +optional
	Image string `json:"image,omitempty"`

	// Resources for the sandbox container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// SubagentsSpec defines sub-agent spawning configuration.
type SubagentsSpec struct {
	// MaxDepth is the maximum nesting depth for sub-agents.
	// +kubebuilder:default=2
	MaxDepth int `json:"maxDepth,omitempty"`

	// MaxConcurrent is the maximum number of concurrent agent runs.
	// +kubebuilder:default=5
	MaxConcurrent int `json:"maxConcurrent,omitempty"`

	// MaxChildrenPerAgent is the maximum number of children per agent.
	// +kubebuilder:default=3
	MaxChildrenPerAgent int `json:"maxChildrenPerAgent,omitempty"`
}

// SkillRef references a SkillPack or ConfigMap containing skills.
type SkillRef struct {
	// SkillPackRef references a SkillPack CRD by name.
	// +optional
	SkillPackRef string `json:"skillPackRef,omitempty"`

	// ConfigMapRef references a ConfigMap by name.
	// +optional
	ConfigMapRef string `json:"configMapRef,omitempty"`

	// Params are per-instance key/value pairs injected as SKILL_<KEY> environment
	// variables into the skill sidecar container. This allows the same SkillPack to
	// be configured differently per SympoziumInstance (e.g. different GitHub repos).
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// SecretRef references a Kubernetes Secret.
type SecretRef struct {
	// Provider is the AI provider name (e.g. "openai", "anthropic", "azure-openai", "ollama").
	// +optional
	Provider string `json:"provider,omitempty"`
	// Secret is the name of the Secret.
	Secret string `json:"secret"`
}

// SympoziumInstanceStatus defines the observed state of SympoziumInstance.
type SympoziumInstanceStatus struct {
	// Phase is the current phase (Pending, Running, Serving, Error).
	// +optional
	Phase string `json:"phase,omitempty"`

	// Channels reports the status of each connected channel.
	// +optional
	Channels []ChannelStatus `json:"channels,omitempty"`

	// ActiveAgentPods is the number of currently running agent pods.
	// +optional
	ActiveAgentPods int `json:"activeAgentPods,omitempty"`

	// TotalAgentRuns is the total number of agent runs for this instance.
	// +optional
	TotalAgentRuns int64 `json:"totalAgentRuns,omitempty"`

	// Conditions represent the latest available observations of an object's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// WebEndpoint reports the status of the web endpoint.
	// +optional
	WebEndpoint *WebEndpointStatus `json:"webEndpoint,omitempty"`
}

// WebEndpointStatus reports the observed state of a web endpoint.
type WebEndpointStatus struct {
	// Status is the current status (Pending, Ready, Error).
	Status string `json:"status"`

	// URL is the external URL for the web endpoint.
	// +optional
	URL string `json:"url,omitempty"`

	// AuthSecretName is the name of the Secret containing the API key.
	// +optional
	AuthSecretName string `json:"authSecretName,omitempty"`
}

// ChannelStatus reports the status of a channel.
type ChannelStatus struct {
	// Type is the channel type.
	Type string `json:"type"`

	// Status is the connection status (Connected, Disconnected, Error).
	Status string `json:"status"`

	// LastHealthCheck is the timestamp of the last health check.
	// +optional
	LastHealthCheck *metav1.Time `json:"lastHealthCheck,omitempty"`

	// Message provides additional details about the channel status.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Active Agents",type="integer",JSONPath=".status.activeAgentPods"
// +kubebuilder:printcolumn:name="Total Runs",type="integer",JSONPath=".status.totalAgentRuns"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SympoziumInstance is the Schema for the sympoziuminstances API.
// It represents a per-user/per-tenant gateway configuration.
type SympoziumInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SympoziumInstanceSpec   `json:"spec,omitempty"`
	Status SympoziumInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SympoziumInstanceList contains a list of SympoziumInstance.
type SympoziumInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SympoziumInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SympoziumInstance{}, &SympoziumInstanceList{})
}
