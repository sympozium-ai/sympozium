package controller

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// GPUInfo describes a single GPU detected by llmfit.
type GPUInfo struct {
	Name          string  `json:"name"`
	VRAMGb        float64 `json:"vram_gb"`
	Backend       string  `json:"backend"`
	Count         int     `json:"count"`
	UnifiedMemory bool    `json:"unified_memory"`
}

// SystemSpecs holds the hardware specs for a single node, mirroring
// the JSON structure published by llmfit on the llmfit.system.* subject.
type SystemSpecs struct {
	TotalRAMGb     float64   `json:"total_ram_gb"`
	AvailableRAMGb float64   `json:"available_ram_gb"`
	CPUCores       int       `json:"cpu_cores"`
	CPUName        string    `json:"cpu_name"`
	HasGPU         bool      `json:"has_gpu"`
	GPUVRAMGb      *float64  `json:"gpu_vram_gb"`
	GPUName        *string   `json:"gpu_name"`
	GPUCount       int       `json:"gpu_count"`
	UnifiedMemory  bool      `json:"unified_memory"`
	Backend        string    `json:"backend"`
	GPUs           []GPUInfo `json:"gpus"`
}

// ModelFitInfo holds a single model's fitness assessment on a node.
type ModelFitInfo struct {
	Name            string  `json:"name"`
	Score           float64 `json:"score"`
	FitLevel        string  `json:"fit_level"` // "perfect", "good", "marginal", "too_tight"
	Runtime         string  `json:"runtime"`
	BestQuant       string  `json:"best_quant"`
	EstimatedTPS    float64 `json:"estimated_tps"`
	MemoryRequired  float64 `json:"memory_required_gb"`
	MemoryAvailable float64 `json:"memory_available_gb"`
	UtilizationPct  float64 `json:"utilization_pct"`
	UseCase         string  `json:"category"`
}

// RuntimeStatus describes an inference runtime on a node.
type RuntimeStatus struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
}

// InstalledModelInfo describes a model installed in a local runtime.
type InstalledModelInfo struct {
	Name    string `json:"name"`
	Runtime string `json:"runtime"`
}

// NodeFitness holds the latest llmfit telemetry for a single node.
type NodeFitness struct {
	NodeName        string
	LastSeen        time.Time
	System          SystemSpecs
	ModelFits       []ModelFitInfo
	Runtimes        []RuntimeStatus
	InstalledModels []InstalledModelInfo
}

// FitnessCache maintains per-node fitness data from llmfit NATS events.
// Thread-safe for concurrent reads from reconcilers and writes from the
// NATS subscriber goroutine.
type FitnessCache struct {
	mu    sync.RWMutex
	nodes map[string]*NodeFitness
	ttl   time.Duration
}

// NewFitnessCache creates a new cache. Entries older than ttl are considered stale.
func NewFitnessCache(ttl time.Duration) *FitnessCache {
	return &FitnessCache{
		nodes: make(map[string]*NodeFitness),
		ttl:   ttl,
	}
}

// Update inserts or updates a node's fitness data and refreshes its timestamp.
func (c *FitnessCache) Update(nf *NodeFitness) {
	c.mu.Lock()
	defer c.mu.Unlock()

	existing, ok := c.nodes[nf.NodeName]
	if !ok {
		c.nodes[nf.NodeName] = nf
		return
	}

	// Merge fields — llmfit publishes different event types at different times,
	// so we merge rather than overwrite the entire entry.
	existing.LastSeen = nf.LastSeen
	if nf.System.CPUCores > 0 {
		existing.System = nf.System
	}
	if len(nf.ModelFits) > 0 {
		existing.ModelFits = nf.ModelFits
	}
	if len(nf.Runtimes) > 0 {
		existing.Runtimes = nf.Runtimes
	}
	if len(nf.InstalledModels) > 0 {
		existing.InstalledModels = nf.InstalledModels
	}
}

// Get returns the fitness data for a node. Returns false if not found.
func (c *FitnessCache) Get(nodeName string) (*NodeFitness, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	nf, ok := c.nodes[nodeName]
	if !ok {
		return nil, false
	}
	return nf, true
}

// All returns a copy of all non-stale node fitness entries.
func (c *FitnessCache) All() []*NodeFitness {
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	result := make([]*NodeFitness, 0, len(c.nodes))
	for _, nf := range c.nodes {
		if now.Sub(nf.LastSeen) <= c.ttl {
			result = append(result, nf)
		}
	}
	return result
}

// IsStale returns true if the node's data is older than the TTL or not found.
func (c *FitnessCache) IsStale(nodeName string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	nf, ok := c.nodes[nodeName]
	if !ok {
		return true
	}
	return c.isStaleUnlocked(nf)
}

// isStaleUnlocked checks staleness without acquiring the lock.
// Caller must hold at least a read lock.
func (c *FitnessCache) isStaleUnlocked(nf *NodeFitness) bool {
	return time.Since(nf.LastSeen) > c.ttl
}

// NodeCount returns the number of nodes in the cache (including stale).
func (c *FitnessCache) NodeCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.nodes)
}

// BestNodeForModel finds the node with the highest fitness score for the given
// model query string. Returns the node name, score, and a human-readable message.
// If no suitable node is found, returns empty string and zero score.
func (c *FitnessCache) BestNodeForModel(modelQuery string, minFit string) (string, float64, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	queryLower := strings.ToLower(modelQuery)
	now := time.Now()

	var bestNode string
	var bestScore float64
	var bestMessage string

	for _, nf := range c.nodes {
		// Skip stale nodes.
		if now.Sub(nf.LastSeen) > c.ttl {
			continue
		}

		for _, fit := range nf.ModelFits {
			if !strings.Contains(strings.ToLower(fit.Name), queryLower) {
				continue
			}
			if !fitMeetsMinimum(fit.FitLevel, minFit) {
				continue
			}
			if fit.Score > bestScore {
				bestScore = fit.Score
				bestNode = nf.NodeName
				bestMessage = formatPlacementMessage(nf.NodeName, fit)
			}
		}
	}

	return bestNode, bestScore, bestMessage
}

// GarbageCollect removes entries older than 2x the TTL.
func (c *FitnessCache) GarbageCollect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().Add(-2 * c.ttl)
	for name, nf := range c.nodes {
		if nf.LastSeen.Before(cutoff) {
			delete(c.nodes, name)
		}
	}
}

// fitMeetsMinimum checks whether a fit level meets the minimum threshold.
func fitMeetsMinimum(actual, minimum string) bool {
	rank := map[string]int{
		"perfect":   3,
		"good":      2,
		"marginal":  1,
		"too_tight": 0,
	}
	a, aOk := rank[actual]
	m, mOk := rank[minimum]
	if !aOk || !mOk {
		// Unknown levels: accept if actual is non-empty.
		return actual != ""
	}
	return a >= m
}

func formatPlacementMessage(nodeName string, fit ModelFitInfo) string {
	return fmt.Sprintf("%s: %s (%s, score=%.1f, ~%.1f tok/s)",
		nodeName, fit.Name, fit.FitLevel, fit.Score, fit.EstimatedTPS)
}
