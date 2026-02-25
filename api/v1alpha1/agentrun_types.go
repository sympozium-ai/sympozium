package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentRunSpec defines the desired state of an AgentRun.
// Each agent invocation (including sub-agents) produces an AgentRun CR.
type AgentRunSpec struct {
	// InstanceRef is the name of the SympoziumInstance this run belongs to.
	InstanceRef string `json:"instanceRef"`

	// AgentID identifies the agent configuration to use.
	AgentID string `json:"agentId"`

	// SessionKey is the unique session identifier for this run.
	SessionKey string `json:"sessionKey"`

	// Parent contains parent run information for sub-agents.
	// +optional
	Parent *ParentRunRef `json:"parent,omitempty"`

	// Task is the task description for the agent.
	Task string `json:"task"`

	// SystemPrompt is the system prompt for the agent.
	// +optional
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Model specifies the LLM configuration for this run.
	Model ModelSpec `json:"model"`

	// Sandbox defines sandbox configuration for this run.
	// +optional
	Sandbox *AgentRunSandboxSpec `json:"sandbox,omitempty"`

	// Skills to mount into the agent pod.
	// +optional
	Skills []SkillRef `json:"skills,omitempty"`

	// ToolPolicy defines which tools this agent is allowed to use.
	// +optional
	ToolPolicy *ToolPolicySpec `json:"toolPolicy,omitempty"`

	// Timeout is the maximum duration for this agent run.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Cleanup policy: "delete" to remove pod after completion, "keep" for debugging.
	// +kubebuilder:default="delete"
	// +kubebuilder:validation:Enum=delete;keep
	Cleanup string `json:"cleanup,omitempty"`
}

// ParentRunRef links a sub-agent to its parent.
type ParentRunRef struct {
	// RunName is the name of the parent AgentRun.
	RunName string `json:"runName"`

	// SessionKey is the session key of the parent.
	SessionKey string `json:"sessionKey"`

	// SpawnDepth is how many levels deep this sub-agent is.
	SpawnDepth int `json:"spawnDepth"`
}

// ModelSpec defines which LLM to use.
type ModelSpec struct {
	// Provider is the AI provider (openai, anthropic, azure-openai, github-copilot, ollama, etc.).
	Provider string `json:"provider"`

	// Model is the model identifier.
	Model string `json:"model"`

	// BaseURL overrides the provider's default API endpoint.
	// Use this for OpenAI-compatible providers (GitHub Copilot, Azure OpenAI,
	// Ollama, vLLM, LMStudio, etc.).
	// Examples:
	//   GitHub Copilot: https://api.githubcopilot.com
	//   Azure OpenAI:   https://<resource>.openai.azure.com/openai/deployments/<deployment>
	//   Ollama:         http://ollama.default.svc:11434/v1
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// Thinking mode (off, low, medium, high).
	// +optional
	Thinking string `json:"thinking,omitempty"`

	// AuthSecretRef references the secret containing the API key.
	AuthSecretRef string `json:"authSecretRef"`
}

// AgentRunSandboxSpec defines sandbox settings for an individual agent run.
type AgentRunSandboxSpec struct {
	// Enabled indicates whether sandboxing is enabled.
	Enabled bool `json:"enabled"`

	// Image is the sandbox container image.
	// +optional
	Image string `json:"image,omitempty"`

	// SecurityContext for the sandbox container.
	// +optional
	SecurityContext *SandboxSecurityContext `json:"securityContext,omitempty"`

	// Resources for the sandbox container.
	// +optional
	Resources *ResourceSpec `json:"resources,omitempty"`
}

// SandboxSecurityContext defines security settings for the sandbox.
type SandboxSecurityContext struct {
	// ReadOnlyRootFilesystem makes the root filesystem read-only.
	ReadOnlyRootFilesystem bool `json:"readOnlyRootFilesystem,omitempty"`

	// RunAsNonRoot ensures the container runs as a non-root user.
	RunAsNonRoot bool `json:"runAsNonRoot,omitempty"`

	// Capabilities to add or drop.
	// +optional
	Capabilities *CapabilitiesSpec `json:"capabilities,omitempty"`

	// SeccompProfile defines the seccomp profile.
	// +optional
	SeccompProfile *SeccompProfileSpec `json:"seccompProfile,omitempty"`
}

// CapabilitiesSpec defines Linux capabilities.
type CapabilitiesSpec struct {
	// Drop is a list of capabilities to drop.
	Drop []string `json:"drop,omitempty"`
}

// SeccompProfileSpec defines seccomp settings.
type SeccompProfileSpec struct {
	// Type is the seccomp profile type.
	Type string `json:"type"`
}

// ResourceSpec defines resource requests and limits.
type ResourceSpec struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

// ToolPolicySpec defines which tools an agent may use.
type ToolPolicySpec struct {
	// Allow lists explicitly allowed tools.
	Allow []string `json:"allow,omitempty"`

	// Deny lists explicitly denied tools.
	Deny []string `json:"deny,omitempty"`
}

// AgentRunPhase represents the lifecycle phase of an AgentRun.
type AgentRunPhase string

const (
	AgentRunPhasePending   AgentRunPhase = "Pending"
	AgentRunPhaseRunning   AgentRunPhase = "Running"
	AgentRunPhaseSucceeded AgentRunPhase = "Succeeded"
	AgentRunPhaseFailed    AgentRunPhase = "Failed"
)

// AgentRunStatus defines the observed state of AgentRun.
type AgentRunStatus struct {
	// Phase is the current phase (Pending, Running, Succeeded, Failed).
	// +optional
	Phase AgentRunPhase `json:"phase,omitempty"`

	// PodName is the name of the pod running this agent.
	// +optional
	PodName string `json:"podName,omitempty"`

	// JobName is the name of the Job created for this run.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// StartedAt is when the agent run started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the agent run completed.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Result is the agent's final reply (populated on success).
	// +optional
	Result string `json:"result,omitempty"`

	// Error is the error message (populated on failure).
	// +optional
	Error string `json:"error,omitempty"`

	// ExitCode of the agent container.
	// +optional
	ExitCode *int32 `json:"exitCode,omitempty"`

	// TokenUsage contains LLM token counts and timing for this run.
	// +optional
	TokenUsage *TokenUsage `json:"tokenUsage,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// TokenUsage tracks LLM token consumption and timing for an AgentRun.
type TokenUsage struct {
	// InputTokens is the total number of prompt/input tokens sent to the LLM.
	InputTokens int `json:"inputTokens"`

	// OutputTokens is the total number of completion/output tokens received.
	OutputTokens int `json:"outputTokens"`

	// TotalTokens is InputTokens + OutputTokens.
	TotalTokens int `json:"totalTokens"`

	// ToolCalls is the number of tool invocations during this run.
	ToolCalls int `json:"toolCalls"`

	// DurationMs is the wall-clock time of the LLM interaction in milliseconds.
	DurationMs int64 `json:"durationMs"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Instance",type="string",JSONPath=".spec.instanceRef"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Pod",type="string",JSONPath=".status.podName"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AgentRun is the Schema for the agentruns API.
// Each agent invocation produces an AgentRun CR that the orchestrator
// reconciles into a Kubernetes Job.
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRunSpec   `json:"spec,omitempty"`
	Status AgentRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentRunList contains a list of AgentRun.
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRun{}, &AgentRunList{})
}
