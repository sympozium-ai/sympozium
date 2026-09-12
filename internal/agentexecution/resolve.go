// Package agentexecution resolves Agent-level execution defaults onto runs.
package agentexecution

import (
	"fmt"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Input is the caller-supplied run request. Empty Backend / ExecutionLifecycle
// and a nil CellnSelection mean "inherit from the Agent when present".
// A non-nil CellnSelection (even with empty toolRefs) is an explicit override
// and is never replaced by Agent defaults.
type Input struct {
	Backend            string
	ExecutionLifecycle string
	Enduring           *api.EnduringRunSpec
	CellnSelection     *api.CellnCatalogueSelection
	ModelConnectionRef string
	Provider           string
	Model              string
	RuntimeRef         string // top-level harness override; not CellnSelection.RuntimeRef
}

// Result is the effective configuration after inheritance.
type Result struct {
	Backend            string
	ExecutionLifecycle string
	Enduring           *api.EnduringRunSpec
	CellnSelection     *api.CellnCatalogueSelection
	ModelConnectionRef string
	Provider           string
	Model              string
	// Inherited records which fields came from Agent defaults (for status/audit).
	Inherited []string
}

// Resolve merges Agent execution defaults with an explicit request.
// It does not grant tool authority and does not convert OCI HarnessSessions.
func Resolve(agent *api.Agent, in Input) (Result, error) {
	out := Result{
		Backend:            strings.TrimSpace(in.Backend),
		ExecutionLifecycle: strings.TrimSpace(in.ExecutionLifecycle),
		Enduring:           in.Enduring.DeepCopy(),
		ModelConnectionRef: strings.TrimSpace(in.ModelConnectionRef),
		Provider:           strings.TrimSpace(in.Provider),
		Model:              strings.TrimSpace(in.Model),
	}
	if in.CellnSelection != nil {
		out.CellnSelection = in.CellnSelection.DeepCopy()
	}

	var defaults *api.AgentExecutionDefaults
	if agent != nil {
		defaults = agent.Spec.Execution
	}
	if defaults != nil {
		if reason := defaults.Validate(); reason != "" {
			return Result{}, fmt.Errorf("agent execution defaults are invalid: %s", reason)
		}
		if out.Backend == "" && defaults.Backend != "" {
			out.Backend = defaults.Backend
			out.Inherited = append(out.Inherited, "backend")
		}
		if out.ExecutionLifecycle == "" && defaults.ExecutionLifecycle != "" {
			out.ExecutionLifecycle = defaults.ExecutionLifecycle
			out.Inherited = append(out.Inherited, "executionLifecycle")
		}
		if out.Enduring == nil && defaults.Enduring != nil && (out.ExecutionLifecycle == "enduring" || (out.ExecutionLifecycle == "" && defaults.ExecutionLifecycle == "enduring")) {
			out.Enduring = defaults.Enduring.DeepCopy()
			out.Inherited = append(out.Inherited, "enduring")
		}
		if out.CellnSelection == nil && defaults.CellnSelection != nil {
			out.CellnSelection = defaults.CellnSelection.DeepCopy()
			out.Inherited = append(out.Inherited, "cellnSelection")
		}
		if out.CellnSelection != nil {
			if out.ModelConnectionRef == "" && in.Provider == "" {
				out.ModelConnectionRef = defaults.ModelConnectionRef
			}
			if out.Provider == "" && defaults.Provider != "" && in.ModelConnectionRef == "" {
				out.Provider = defaults.Provider
				out.Inherited = append(out.Inherited, "provider")
			}
			if out.Model == "" && defaults.Model != "" {
				out.Model = defaults.Model
				out.Inherited = append(out.Inherited, "model")
			}
		}
	}

	// Catalogue selection implies Celln; do not silently fall back to Kubernetes.
	if out.CellnSelection != nil && out.Backend == "" {
		out.Backend = "celln"
		out.Inherited = append(out.Inherited, "backend")
	}
	if out.CellnSelection != nil && out.Backend != "celln" {
		return Result{}, fmt.Errorf("catalogue selection requires backend celln; refusing to drop tools or switch to Kubernetes")
	}
	if out.ExecutionLifecycle == "enduring" && out.Backend != "celln" {
		return Result{}, fmt.Errorf("enduring lifecycle requires backend celln")
	}
	if out.Backend == "celln" && agent != nil && (len(agent.Spec.Skills) != 0 || len(agent.Spec.MCPServers) != 0) {
		return Result{}, fmt.Errorf("native Celln cannot use this Agent's SkillPacks or MCP connections; use a dedicated Agent with approved borrowed tools, or choose a compatible backend. Nothing was submitted or silently removed")
	}
	if out.CellnSelection != nil {
		if len(out.CellnSelection.ClusterToolRefs) != 0 {
			return Result{}, fmt.Errorf("AUTH_PROTOCOL_UNSUPPORTED: shared catalogue selection requires mediated admission; refusing legacy fallback")
		}
		if out.CellnSelection.ToolRefs == nil {
			return Result{}, fmt.Errorf("cellnSelection.toolRefs must be present (use [] for an explicit empty tool set)")
		}
		if len(out.CellnSelection.ToolRefs) > 16 {
			return Result{}, fmt.Errorf("cellnSelection.toolRefs supports at most 16 tools")
		}
		if strings.TrimSpace(in.RuntimeRef) != "" {
			return Result{}, fmt.Errorf("catalogue selection requires backend celln, a provider or model connection, model and toolRefs; use only cellnSelection.runtimeRef for an override")
		}
		if out.Provider == "" && out.ModelConnectionRef == "" {
			out.Provider = "deepseek"
		}
		if out.Model == "" {
			return Result{}, fmt.Errorf("catalogue selection requires backend celln, a provider or model connection, model and toolRefs; use only cellnSelection.runtimeRef for an override")
		}
	}

	probe := api.AgentRunSpec{
		Backend:            out.Backend,
		ExecutionLifecycle: out.ExecutionLifecycle,
		Enduring:           out.Enduring,
		CellnSelection:     out.CellnSelection,
	}
	if reason := probe.ValidateLifecycle(); reason != "" {
		return Result{}, fmt.Errorf("%s", reason)
	}
	return out, nil
}

// Apply copies resolved execution fields onto an AgentRunSpec.
func Apply(spec *api.AgentRunSpec, resolved Result) {
	if spec == nil {
		return
	}
	spec.Backend = resolved.Backend
	spec.ExecutionLifecycle = resolved.ExecutionLifecycle
	spec.Enduring = resolved.Enduring.DeepCopy()
	if resolved.CellnSelection != nil {
		spec.CellnSelection = resolved.CellnSelection.DeepCopy()
	}
}
