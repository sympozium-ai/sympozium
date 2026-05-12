package apiserver

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// simulateRequest is the body for POST /api/v1/density/simulate.
type simulateRequest struct {
	Model        string  `json:"model"`
	Quantization string  `json:"quantization,omitempty"`
	Context      int     `json:"context,omitempty"`
	MemoryGB     float64 `json:"memoryGb,omitempty"` // override memory requirement
}

// simulateResponse is the response for POST /api/v1/density/simulate.
type simulateResponse struct {
	Model          string               `json:"model"`
	RankedNodes    []simulateNodeResult `json:"rankedNodes"`
	CanFitAnywhere bool                 `json:"canFitAnywhere"`
}

type simulateNodeResult struct {
	NodeName          string  `json:"nodeName"`
	CurrentScore      float64 `json:"currentScore"`
	FitLevel          string  `json:"fitLevel"`
	AvailableMemoryGB float64 `json:"availableMemoryGb"`
	RequiredMemoryGB  float64 `json:"requiredMemoryGb"`
	RemainingMemoryGB float64 `json:"remainingMemoryGb"`
	UtilizationPct    float64 `json:"utilizationPct"`
}

// handleSimulate handles POST /api/v1/density/simulate.
// Simulates deploying a model and returns per-node impact analysis.
func (s *Server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	if s.densityCache == nil {
		http.Error(w, "fitness cache not available", http.StatusServiceUnavailable)
		return
	}

	var req simulateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		http.Error(w, "model field is required", http.StatusBadRequest)
		return
	}

	queryLower := strings.ToLower(req.Model)
	all := s.densityCache.All()

	var results []simulateNodeResult
	canFit := false

	for _, nf := range all {
		for _, fit := range nf.ModelFits {
			if !strings.Contains(strings.ToLower(fit.Name), queryLower) {
				continue
			}

			memRequired := fit.MemoryRequired
			if req.MemoryGB > 0 {
				memRequired = req.MemoryGB
			}

			remaining := fit.MemoryAvailable - memRequired
			if remaining < 0 {
				remaining = 0
			}

			result := simulateNodeResult{
				NodeName:          nf.NodeName,
				CurrentScore:      fit.Score,
				FitLevel:          fit.FitLevel,
				AvailableMemoryGB: fit.MemoryAvailable,
				RequiredMemoryGB:  memRequired,
				RemainingMemoryGB: remaining,
				UtilizationPct:    fit.UtilizationPct,
			}

			results = append(results, result)
			if fit.FitLevel != "too_tight" {
				canFit = true
			}
			break // one match per node
		}
	}

	sort.Slice(results, func(i, j int) bool { return results[i].CurrentScore > results[j].CurrentScore })

	writeDensityJSON(w, simulateResponse{
		Model:          req.Model,
		RankedNodes:    results,
		CanFitAnywhere: canFit,
	})
}
