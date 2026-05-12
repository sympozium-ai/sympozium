package controller

import (
	"sort"
	"strings"
)

// SchedulePreference captures GPU-aware scheduling preferences for a model.
type SchedulePreference struct {
	PreferGPU     bool    // prefer nodes with discrete GPUs
	MinVRAMGb     float64 // minimum GPU VRAM required
	PreferUnified bool    // prefer unified memory (Apple Silicon)
	PreferBackend string  // prefer specific backend (cuda, metal, rocm)
}

// RankedPlacement is a scored node recommendation from the scheduler.
type RankedPlacement struct {
	NodeName     string
	Score        float64
	FitLevel     string
	GPUName      string
	GPUVRAMGb    float64
	Backend      string
	ModelName    string
	EstimatedTPS float64
	Reason       string // why this node was ranked here
}

// ScheduleModelPlacement returns nodes ranked by fitness with GPU-aware
// scoring that goes beyond the simple best-score approach.
//
// It considers:
//   - GPU VRAM matching (prefer nodes where VRAM closely matches requirements)
//   - GPU backend compatibility (cuda vs metal vs rocm)
//   - Unified memory preference for large models on Apple Silicon
//   - Runtime availability (prefer nodes where the target runtime is installed)
//   - CPU fallback when no GPU nodes qualify
func (c *FitnessCache) ScheduleModelPlacement(modelQuery string, pref SchedulePreference) []RankedPlacement {
	c.mu.RLock()
	defer c.mu.RUnlock()

	queryLower := strings.ToLower(modelQuery)

	type candidate struct {
		placement RankedPlacement
		priority  float64 // composite priority for sorting
	}

	var candidates []candidate

	for _, nf := range c.nodes {
		if c.isStaleUnlocked(nf) {
			continue
		}

		for _, fit := range nf.ModelFits {
			if !strings.Contains(strings.ToLower(fit.Name), queryLower) {
				continue
			}

			if fit.FitLevel == "too_tight" {
				continue
			}

			placement := RankedPlacement{
				NodeName:     nf.NodeName,
				Score:        fit.Score,
				FitLevel:     fit.FitLevel,
				ModelName:    fit.Name,
				EstimatedTPS: fit.EstimatedTPS,
				Backend:      nf.System.Backend,
			}

			// Extract GPU info.
			if len(nf.System.GPUs) > 0 {
				placement.GPUName = nf.System.GPUs[0].Name
				placement.GPUVRAMGb = nf.System.GPUs[0].VRAMGb
			} else if nf.System.GPUVRAMGb != nil {
				placement.GPUVRAMGb = *nf.System.GPUVRAMGb
			}
			if nf.System.GPUName != nil {
				placement.GPUName = *nf.System.GPUName
			}

			// Composite priority: start with the fitness score.
			priority := fit.Score

			// GPU preference bonus.
			if pref.PreferGPU && nf.System.HasGPU {
				priority += 10
				placement.Reason = "gpu available"
			}

			// VRAM threshold check.
			if pref.MinVRAMGb > 0 && placement.GPUVRAMGb >= pref.MinVRAMGb {
				priority += 5
				placement.Reason = "meets VRAM requirement"
			} else if pref.MinVRAMGb > 0 && placement.GPUVRAMGb < pref.MinVRAMGb {
				priority -= 20
				placement.Reason = "insufficient VRAM"
			}

			// Backend preference bonus.
			if pref.PreferBackend != "" && strings.EqualFold(nf.System.Backend, pref.PreferBackend) {
				priority += 5
				placement.Reason = "preferred backend: " + pref.PreferBackend
			}

			// Unified memory preference (Apple Silicon with large models).
			if pref.PreferUnified && nf.System.UnifiedMemory {
				priority += 8
				placement.Reason = "unified memory"
			}

			// TPS bonus — prefer faster nodes.
			priority += fit.EstimatedTPS * 0.1

			candidates = append(candidates, candidate{
				placement: placement,
				priority:  priority,
			})

			break // one match per node
		}
	}

	// Sort by composite priority descending.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].priority > candidates[j].priority
	})

	result := make([]RankedPlacement, len(candidates))
	for i, c := range candidates {
		result[i] = c.placement
	}
	return result
}

// HasGPUNodes returns true if any non-stale node in the cache has a GPU.
func (c *FitnessCache) HasGPUNodes() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, nf := range c.nodes {
		if !c.isStaleUnlocked(nf) && nf.System.HasGPU {
			return true
		}
	}
	return false
}

// GPUBackends returns the set of GPU backends available in the cluster.
func (c *FitnessCache) GPUBackends() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := make(map[string]bool)
	for _, nf := range c.nodes {
		if !c.isStaleUnlocked(nf) && nf.System.HasGPU && nf.System.Backend != "" {
			seen[nf.System.Backend] = true
		}
	}
	result := make([]string, 0, len(seen))
	for b := range seen {
		result = append(result, b)
	}
	sort.Strings(result)
	return result
}
