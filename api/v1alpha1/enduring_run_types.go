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
	// +kubebuilder:validation:Maximum=3145728
	MaxOutputTokens int64 `json:"maxOutputTokens"`
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
	if e.LeaseSeconds < 1 || e.LeaseSeconds > 86400 || e.MaxTurns < 1 || e.MaxTurns > 1024 || e.MaxModelRequests < 0 || e.MaxModelRequests > 6144 || e.MaxOutputTokens < 0 || e.MaxOutputTokens > 3145728 {
		return "enduring lifecycle limits exceed supported bounds"
	}
	return ""
}
