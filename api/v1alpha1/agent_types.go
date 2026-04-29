package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentSpec defines the desired state of a Agent.
// Each user or tenant gets a Agent that declares their desired channels,
// agents, and policy bindings.
type AgentSpec struct {
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

	// MCPServers configures remote MCP (Model Context Protocol) servers
	// that agents in this instance can access via the mcp-bridge sidecar.
	// +optional
	MCPServers []MCPServerRef `json:"mcpServers,omitempty"`

	// Deprecated: Use the "web-endpoint" SkillPack in Skills instead.
	// WebEndpoint exposes this agent as an HTTP API (OpenAI-compatible + MCP).
	// When nil or Enabled is false, no web-proxy infrastructure is deployed.
	// +optional
	WebEndpoint *WebEndpointSpec `json:"webEndpoint,omitempty"`

	// ImagePullSecrets are secrets to use when pulling container images.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Volumes are additional pod volumes to attach to agent pods spawned
	// by this Agent. Common use cases include CSI driver volumes
	// (e.g. Vault Secrets Store CSI / secrets-store.csi.k8s.io),
	// PersistentVolumeClaims, and projected Secret/ConfigMap volumes.
	// These are appended to the volumes Sympozium manages internally
	// (workspace, ipc, skills, tmp, memory, etc.). Names must not
	// collide with reserved volumes: workspace, ipc, skills, tmp,
	// memory, mcp-config.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts are additional volume mounts applied to the main
	// agent container. Names must reference entries in Volumes or any
	// other volume defined on the pod. Use Volumes + VolumeMounts
	// together to surface secrets from CSI drivers (e.g. Vault CSI)
	// inside the agent's filesystem.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// MCPServerRef references a remote MCP server for tool integration.
type MCPServerRef struct {
	// Name is a unique identifier for this MCP server connection.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// URL is the MCP server's Streamable HTTP endpoint. Optional if an MCPServer CR with this name exists.
	// +optional
	URL string `json:"url,omitempty"`

	// ToolsPrefix is prepended to tool names from this server to avoid collisions.
	// Must be unique across all configured servers.
	// +kubebuilder:validation:MinLength=1
	ToolsPrefix string `json:"toolsPrefix"`

	// Timeout is the per-request timeout in seconds for this server.
	// +kubebuilder:default=30
	// +optional
	Timeout int `json:"timeout,omitempty"`

	// AuthSecret references a Kubernetes Secret containing auth credentials.
	// The key specified by AuthKey (default "token") is used as a Bearer token.
	// +optional
	AuthSecret string `json:"authSecret,omitempty"`

	// AuthKey is the key within AuthSecret to use. Defaults to "token".
	// +optional
	AuthKey string `json:"authKey,omitempty"`

	// Headers are additional HTTP headers to send with every request.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// ToolsAllow lists tool names (without prefix) to expose. If set, only these tools are registered.
	// +optional
	ToolsAllow []string `json:"toolsAllow,omitempty"`

	// ToolsDeny lists tool names (without prefix) to hide. Applied after toolsAllow.
	// +optional
	ToolsDeny []string `json:"toolsDeny,omitempty"`
}

// MemorySpec configures persistent memory for a Agent.
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

	// AccessControl restricts which users and chats can interact via this channel.
	// When nil, all users and chats are allowed.
	// +optional
	AccessControl *ChannelAccessControl `json:"accessControl,omitempty"`
}

// ChannelAccessControl restricts which users and chats can interact via a channel.
type ChannelAccessControl struct {
	// AllowedSenders restricts interaction to these sender IDs.
	// When non-empty, only listed senders can trigger agent runs.
	// +optional
	AllowedSenders []string `json:"allowedSenders,omitempty"`

	// DeniedSenders blocks these sender IDs from interacting.
	// Overrides AllowedSenders when a sender appears in both lists.
	// +optional
	DeniedSenders []string `json:"deniedSenders,omitempty"`

	// AllowedChats restricts interaction to these chat/group IDs.
	// When non-empty, only messages from listed chats are accepted.
	// +optional
	AllowedChats []string `json:"allowedChats,omitempty"`

	// DenyMessage is the text sent back to rejected senders.
	// When empty, rejected messages are silently dropped.
	// +optional
	DenyMessage string `json:"denyMessage,omitempty"`
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

	// AgentSandbox configures the Kubernetes Agent Sandbox (CRD) execution backend defaults.
	// When enabled, agent runs use Sandbox CRs with gVisor/Kata kernel isolation.
	// +optional
	AgentSandbox *AgentSandboxDefaults `json:"agentSandbox,omitempty"`

	// Subagents configuration.
	// +optional
	Subagents *SubagentsSpec `json:"subagents,omitempty"`

	// RunTimeout is the maximum duration for each agent run (e.g. "30m", "1h").
	// Defaults to 10 minutes for cloud providers and 30 minutes for local
	// providers (ollama, lm-studio, vllm). Local models are significantly
	// slower per request, so the longer default prevents premature timeouts.
	// +optional
	RunTimeout string `json:"runTimeout,omitempty"`

	// NodeSelector constrains agent pods to nodes with matching labels.
	// Used for node-pinned inference (e.g., Ollama installed on specific GPU nodes).
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Lifecycle defines pre and post run hooks for agent runs.
	// Propagated to AgentRunSpec.Lifecycle when runs are created.
	// +optional
	Lifecycle *LifecycleHooks `json:"lifecycle,omitempty"`
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

// AgentSandboxDefaults defines instance-level defaults for the Kubernetes
// Agent Sandbox (CRD) execution backend.
type AgentSandboxDefaults struct {
	// Enabled activates Agent Sandbox mode for all runs in this instance.
	Enabled bool `json:"enabled"`

	// RuntimeClass is the default runtimeClassName (e.g., "gvisor", "kata").
	// +optional
	RuntimeClass string `json:"runtimeClass,omitempty"`

	// WarmPool configures a controller-managed SandboxWarmPool for this instance.
	// +optional
	WarmPool *WarmPoolSpec `json:"warmPool,omitempty"`
}

// WarmPoolSpec configures a managed SandboxWarmPool for pre-provisioned sandboxes.
type WarmPoolSpec struct {
	// Size is the number of pre-warmed sandboxes to maintain.
	// +kubebuilder:default=2
	Size int `json:"size,omitempty"`

	// RuntimeClass for warm pool sandboxes.
	// +optional
	RuntimeClass string `json:"runtimeClass,omitempty"`

	// Resources for each warm pool sandbox.
	// +optional
	Resources *ResourceSpec `json:"resources,omitempty"`
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
	// be configured differently per Agent (e.g. different GitHub repos).
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

// AgentStatus defines the observed state of Agent.
type AgentStatus struct {
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

// Agent is the Schema for the agents API.
// It represents a per-user/per-tenant gateway configuration.
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSpec   `json:"spec,omitempty"`
	Status AgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent.
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Agent{}, &AgentList{})
}
