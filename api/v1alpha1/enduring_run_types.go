package v1alpha1

// EnduringRunSpec is the requested ceiling for a native persistent Harness.
// Per-child limits and executable authority come from approved runtime grants.
type EnduringRunSpec struct {
	// RequireToolCall requires actual execution of at least one lent tool on
	// every turn before successful completion; it grants no additional tools.
	// +optional
	RequireToolCall bool `json:"requireToolCall,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	LeaseSeconds int32 `json:"leaseSeconds"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	MaxTurns int32 `json:"maxTurns"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=6144
	MaxModelRequests int32 `json:"maxModelRequests"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=25165824
	MaxOutputTokens int64 `json:"maxOutputTokens"`
}

// ConversationSpec is how an enduring run relates to the conversation it
// belongs to: whether a lost parent is re-created, and, for a run that
// continues an earlier one, the memory it starts with.
type ConversationSpec struct {
	// Continuation decides what happens when this parent's live context is
	// lost (its node left the fleet, its owner process was replaced):
	// automatic creates a new run that continues the conversation on any
	// node with capacity, seeded with the recorded exchanges; none ends it.
	// +kubebuilder:validation:Enum=automatic;none
	// +optional
	Continuation string `json:"continuation,omitempty"`
	// ContinuesFrom names the run whose conversation this run continues. Set
	// by the controller (automatic continuation) or the API (a restart); the
	// transcript travels in Seed so the previous run may already be gone.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ContinuesFrom string `json:"continuesFrom,omitempty"`
	// Depth counts continuations along the chain so a fleet that keeps
	// losing nodes cannot re-create a parent forever.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=16
	// +optional
	Depth int32 `json:"depth,omitempty"`
	// Seed is the memory a continued parent starts with: the committed
	// exchanges of the conversation so far, oldest first, bounded so a first
	// message still fits the turn. Text only; never instructions or tools.
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	// +optional
	Seed []ConversationExchange `json:"seed,omitempty"`
}

// ConversationExchange is one committed user message and the answer it got.
type ConversationExchange struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	User string `json:"user"`
	// +kubebuilder:validation:MaxLength=8192
	Assistant string `json:"assistant"`
}

// Bounds of one exchange in an enduring conversation, mirroring the Celln
// host's parent protocol. The CRD MaxLength markers on messages and answers
// state the same numbers; a marker cannot reference a constant, so
// TestConversationBoundsMatchSchema keeps them together.
const (
	// MaxConversationMessageBytes bounds one user message.
	MaxConversationMessageBytes = 2048
	// MaxConversationAnswerBytes bounds one committed answer.
	MaxConversationAnswerBytes = 8192
	// MaxWorkerTaskBytes is the worker task (history plus message) a current
	// starter package reports as its runtime profile's taskBytes.
	MaxWorkerTaskBytes = 16384
)

// Per-turn model allowance of the current Celln starter package. Every turn
// reserves the whole allowance from its parent's lifetime totals, so totals
// are sized as turns × allowance.
const (
	TurnModelRequests = 6
	TurnOutputTokens  = TurnModelRequests * DefaultRequestOutputTokens
)

// Output tokens one model request may produce. A fleet backend may set its own
// cap within the range; a turn on it reserves TurnModelRequests requests of
// that size, so the most a turn reserves is MaxTurnOutputTokens and the most a
// parent's lifetime total can be is MaxLifetimeOutputTokens (1024 turns).
const (
	DefaultRequestOutputTokens = 512
	MinRequestOutputTokens     = 256
	MaxRequestOutputTokens     = 4096
	MaxTurnOutputTokens        = TurnModelRequests * MaxRequestOutputTokens
	MaxLifetimeOutputTokens    = 1024 * MaxTurnOutputTokens
)

// MaxContinuationDepth bounds automatic re-creation along one conversation.
const MaxContinuationDepth = 16

// ContinuesOnLoss reports whether a lost parent is re-created automatically;
// an enduring run without a conversation block is continued by default.
func (s *AgentRunSpec) ContinuesOnLoss() bool {
	return s.ExecutionLifecycle == "enduring" && (s.Conversation == nil || s.Conversation.Continuation != "none")
}

// PlatformOneShotShape reports whether this spec asks for a single answer from
// the shared catalogue: backend celln, a one-shot lifecycle, cluster tools or a
// platform wrapper (no namespaced tools), the namespace's model connection and
// a string task. On a fleet such a run is served by a single-turn native
// parent that ends after its answer; the controller still confirms the runtime
// is a platform wrapper before taking that path.
func (s *AgentRunSpec) PlatformOneShotShape() bool {
	return s.Backend == "celln" && s.ExecutionLifecycle != "enduring" && s.Enduring == nil && s.Celln == nil &&
		s.CellnSelection != nil && len(s.CellnSelection.ToolRefs) == 0 && s.Model.ConnectionRef != "" &&
		(s.Mode == "" || s.Mode == "task") && s.Task.IsString() && s.ValidateLifecycle() == ""
}

// ValidateLifecycle also protects clients/fake API servers that do not evaluate
// CRD CEL. It does not claim that a selected backend implements this lifecycle.
func (s *AgentRunSpec) ValidateLifecycle() string {
	if s.ExecutionLifecycle != "" && s.ExecutionLifecycle != "one-shot" && s.ExecutionLifecycle != "enduring" {
		return "unknown AgentRun lifecycle"
	}
	if s.ExecutionLifecycle == "one-shot" && s.Mode == "server" {
		return "one-shot lifecycle cannot use server mode"
	}
	if s.ExecutionLifecycle != "enduring" {
		if s.Enduring != nil {
			return "enduring limits require enduring lifecycle"
		}
		return ""
	}
	if s.Backend != "celln" || s.CellnSelection == nil || s.Celln != nil || (s.Mode != "" && s.Mode != "task") || s.Enduring == nil {
		return "enduring lifecycle requires Celln catalogue selection, limits, and task mode"
	}
	e := s.Enduring
	if e.RequireToolCall && (len(s.CellnSelection.ToolRefs) == 0 || e.MaxModelRequests < 2 || e.MaxOutputTokens < 1) {
		return "required tool execution needs selected tools and model request/output budgets"
	}
	if e.LeaseSeconds < 1 || e.LeaseSeconds > 86400 || e.MaxTurns < 1 || e.MaxTurns > 1024 || e.MaxModelRequests < 0 || e.MaxModelRequests > 6144 || e.MaxOutputTokens < 0 || e.MaxOutputTokens > MaxLifetimeOutputTokens {
		return "enduring lifecycle limits exceed supported bounds"
	}
	return ""
}
