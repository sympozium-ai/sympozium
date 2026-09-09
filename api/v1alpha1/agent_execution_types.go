package v1alpha1

// AgentExecutionDefaults stores Agent-level defaults for run execution choices.
// Omission preserves historical behaviour: Kubernetes Job backend, no Celln
// catalogue selection, and no enduring lifecycle. Defaults and toolbox
// suggestions do not grant authority; operator and runtime admissions still apply.
//
// CellnSelection presence is meaningful: a non-nil value with an empty toolRefs
// list is an explicit "lend no tools" default. A nil CellnSelection means runs
// must supply their own selection when using the Celln catalogue path.
type AgentExecutionDefaults struct {
	// Backend selects the default execution environment.
	// "job" (Kubernetes containers/OCI) remains the product default when unset.
	// "celln" is a privileged opt-in for hardware-isolated cells.
	// +kubebuilder:validation:Enum=job;celln
	// +optional
	Backend string `json:"backend,omitempty"`

	// ExecutionLifecycle selects the default one-shot or enduring lifecycle.
	// Enduring is the native Celln parent/turn path, not an OCI HarnessSession.
	// +kubebuilder:validation:Enum=one-shot;enduring
	// +optional
	ExecutionLifecycle string `json:"executionLifecycle,omitempty"`

	// Enduring bounds inherited by enduring runs when the request omits limits.
	// +optional
	Enduring *EnduringRunSpec `json:"enduring,omitempty"`

	// CellnSelection is the default catalogue selection for backend=celln.
	// RuntimeRef here overrides Agent.spec.runtimeRef for inherited Celln runs only.
	// +optional
	CellnSelection *CellnCatalogueSelection `json:"cellnSelection,omitempty"`

	// Provider is the default model provider for inherited Celln catalogue runs.
	// Celln catalogue admission currently requires DeepSeek-hosted model grants.
	// +optional
	Provider string `json:"provider,omitempty"`

	// Model is the default model id for inherited Celln catalogue runs.
	// +optional
	Model string `json:"model,omitempty"`
}

// Validate reports an actionable reason when Agent execution defaults are inconsistent.
// An empty result means the defaults are structurally acceptable; it does not
// claim host readiness, approvals, or that a selected runtime is Celln-compatible.
func (e *AgentExecutionDefaults) Validate() string {
	if e == nil {
		return ""
	}
	if e.Backend != "" && e.Backend != "job" && e.Backend != "celln" {
		return "execution.backend must be job or celln"
	}
	if e.ExecutionLifecycle != "" && e.ExecutionLifecycle != "one-shot" && e.ExecutionLifecycle != "enduring" {
		return "execution.executionLifecycle must be one-shot or enduring"
	}
	if e.CellnSelection != nil {
		if e.Backend != "" && e.Backend != "celln" {
			return "execution.cellnSelection requires execution.backend=celln"
		}
		if e.CellnSelection.ToolRefs == nil {
			return "execution.cellnSelection.toolRefs must be present (use [] to lend no tools)"
		}
		if len(e.CellnSelection.ToolRefs) > 16 {
			return "execution.cellnSelection.toolRefs supports at most 16 tools"
		}
	}
	if e.ExecutionLifecycle == "enduring" {
		if e.Backend != "celln" {
			return "enduring execution defaults require execution.backend=celln"
		}
		if e.CellnSelection == nil {
			return "enduring execution defaults require execution.cellnSelection (toolRefs may be empty)"
		}
		if e.Enduring == nil {
			return "enduring execution defaults require execution.enduring limits"
		}
		probe := AgentRunSpec{
			Backend:            "celln",
			ExecutionLifecycle: "enduring",
			CellnSelection:     e.CellnSelection,
			Enduring:           e.Enduring,
		}
		if reason := probe.ValidateLifecycle(); reason != "" {
			return reason
		}
	}
	if e.ExecutionLifecycle == "one-shot" && e.Enduring != nil {
		return "execution.enduring limits require enduring lifecycle"
	}
	if e.Backend == "job" && (e.CellnSelection != nil || e.ExecutionLifecycle == "enduring") {
		return "Kubernetes (job) execution defaults cannot carry Celln selection or enduring lifecycle"
	}
	if (e.Provider != "" || e.Model != "") && e.Backend != "" && e.Backend != "celln" {
		return "execution.provider/model defaults apply only to Celln catalogue runs"
	}
	return ""
}
