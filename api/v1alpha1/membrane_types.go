package v1alpha1

// MembraneSpec configures the synthetic membrane layer for shared memory,
// enabling selective permeability, provenance tracking, token budgets,
// and circuit breakers across agent configurations in an ensemble.
type MembraneSpec struct {
	// Permeability defines per-agent-config visibility and selectivity rules.
	// If empty, all entries default to the ensemble-level DefaultVisibility.
	// +optional
	Permeability []PermeabilityRule `json:"permeability,omitempty"`

	// DefaultVisibility is the default visibility tier for new entries
	// when not overridden by a per-agent-config PermeabilityRule.
	// +kubebuilder:validation:Enum=public;trusted;private
	// +kubebuilder:default="public"
	// +optional
	DefaultVisibility string `json:"defaultVisibility,omitempty"`

	// TrustGroups defines named groups of agent configs that share "trusted"
	// visibility. Agents in the same trust group can see each other's
	// "trusted" entries. If empty, trust is derived from ensemble Relationships
	// (delegation and supervision edges imply mutual trust).
	// +optional
	TrustGroups []TrustGroup `json:"trustGroups,omitempty"`

	// TokenBudget configures ensemble-level token spending limits.
	// +optional
	TokenBudget *TokenBudgetSpec `json:"tokenBudget,omitempty"`

	// CircuitBreaker configures failure thresholds for delegation chains.
	// +optional
	CircuitBreaker *CircuitBreakerSpec `json:"circuitBreaker,omitempty"`

	// TimeDecay configures how old entries lose salience in search results.
	// +optional
	TimeDecay *TimeDecaySpec `json:"timeDecay,omitempty"`

	// EvidencePolicy configures quality-based filtering for shared memory entries.
	// When set, auto-context injection only includes entries at or above the
	// specified evidence quality threshold.
	// +optional
	EvidencePolicy *EvidencePolicySpec `json:"evidencePolicy,omitempty"`
}

// PermeabilityRule defines what an agent config exposes to and accepts from
// the shared memory membrane.
type PermeabilityRule struct {
	// AgentConfig is the agent config name this rule applies to.
	AgentConfig string `json:"agentConfig"`

	// DefaultVisibility for entries created by this agent config.
	// Overrides the ensemble-level default.
	// +kubebuilder:validation:Enum=public;trusted;private
	// +optional
	DefaultVisibility string `json:"defaultVisibility,omitempty"`

	// ExposeTags lists tags this agent config publishes through the membrane.
	// Empty means expose all tags. Entries with tags not in this list
	// are treated as "private" regardless of their visibility setting.
	// +optional
	ExposeTags []string `json:"exposeTags,omitempty"`

	// AcceptTags lists tags this agent config is interested in receiving.
	// Empty means accept all visible entries. When set, search results
	// are filtered to only include entries with at least one matching tag.
	// +optional
	AcceptTags []string `json:"acceptTags,omitempty"`
}

// TrustGroup defines a named group of agent configs that share "trusted"
// visibility with each other.
type TrustGroup struct {
	// Name is a human-readable identifier for this trust group.
	Name string `json:"name"`

	// AgentConfigs lists the agent config names in this group.
	AgentConfigs []string `json:"agentConfigs"`
}

// TokenBudgetSpec configures ensemble-level token spending limits.
type TokenBudgetSpec struct {
	// MaxTokens is the maximum total tokens (input+output) across all agent
	// runs in a single ensemble execution wave. 0 means unlimited.
	// +optional
	MaxTokens int64 `json:"maxTokens,omitempty"`

	// MaxTokensPerRun limits tokens for any single AgentRun. 0 means unlimited.
	// +optional
	MaxTokensPerRun int64 `json:"maxTokensPerRun,omitempty"`

	// Action determines what happens when the budget is exceeded.
	// "halt" (default) prevents new runs from starting.
	// "warn" allows runs but sets a warning condition on the ensemble.
	// +kubebuilder:validation:Enum=halt;warn
	// +kubebuilder:default="halt"
	// +optional
	Action string `json:"action,omitempty"`
}

// CircuitBreakerSpec configures failure detection for delegation chains.
type CircuitBreakerSpec struct {
	// ConsecutiveFailures is how many consecutive delegate failures
	// trigger the circuit breaker.
	// +kubebuilder:default=3
	// +optional
	ConsecutiveFailures int `json:"consecutiveFailures,omitempty"`

	// CooldownDuration is how long the breaker stays open before
	// allowing retries. Format: "5m", "1h". Empty means manual reset only.
	// +optional
	CooldownDuration string `json:"cooldownDuration,omitempty"`
}

// EvidencePolicySpec configures evidence quality thresholds for the membrane.
type EvidencePolicySpec struct {
	// MinKind is the minimum evidence kind for auto-context injection.
	// Entries below this quality threshold are excluded from automatic
	// injection into agent prompts (but remain searchable via tools).
	// Ordered from highest to lowest quality:
	// tool_result > external_source > llm_interpretation > agent_opinion
	// +kubebuilder:validation:Enum=tool_result;external_source;llm_interpretation;agent_opinion
	// +optional
	MinKind string `json:"minKind,omitempty"`
}

// TimeDecaySpec configures salience decay for memory entries.
type TimeDecaySpec struct {
	// TTL is the default time-to-live for entries. Entries older than this
	// are excluded from search results but not deleted.
	// Format: "24h", "168h" (7 days).
	// +optional
	TTL string `json:"ttl,omitempty"`

	// DecayFunction controls how relevance decreases with age.
	// +kubebuilder:validation:Enum=linear;exponential
	// +kubebuilder:default="linear"
	// +optional
	DecayFunction string `json:"decayFunction,omitempty"`
}
