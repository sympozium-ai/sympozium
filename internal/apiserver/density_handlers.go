package apiserver

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/sympozium-ai/sympozium/internal/controller"
)

// densityNodesResponse is the response for GET /api/v1/density/nodes.
type densityNodesResponse struct {
	Nodes []densityNodeSummary `json:"nodes"`
	Total int                  `json:"total"`
}

type densityNodeSummary struct {
	NodeName        string                          `json:"nodeName"`
	LastSeen        string                          `json:"lastSeen"`
	Stale           bool                            `json:"stale"`
	System          controller.SystemSpecs          `json:"system"`
	ModelFitCount   int                             `json:"modelFitCount"`
	Runtimes        []controller.RuntimeStatus      `json:"runtimes,omitempty"`
	InstalledModels []controller.InstalledModelInfo `json:"installedModels,omitempty"`
}

// densityQueryResponse is the response for GET /api/v1/density/query.
type densityQueryResponse struct {
	Query       string              `json:"query"`
	RankedNodes []densityNodeResult `json:"rankedNodes"`
}

type densityNodeResult struct {
	NodeName string                  `json:"nodeName"`
	Score    float64                 `json:"score"`
	FitLevel string                  `json:"fitLevel"`
	Model    controller.ModelFitInfo `json:"model"`
}

// catalogResponse is the response for GET /api/v1/catalog.
type catalogResponse struct {
	Models []catalogEntry `json:"models"`
	Total  int            `json:"total"`
}

type catalogEntry struct {
	ModelName string      `json:"modelName"`
	BestScore float64     `json:"bestScore"`
	BestNode  string      `json:"bestNode"`
	FitLevel  string      `json:"fitLevel"`
	Nodes     []nodeScore `json:"nodes"`
}

type nodeScore struct {
	NodeName string  `json:"nodeName"`
	Score    float64 `json:"score"`
	FitLevel string  `json:"fitLevel"`
}

// listDensityNodes returns all nodes with fitness data.
// GET /api/v1/density/nodes
func (s *Server) listDensityNodes(w http.ResponseWriter, r *http.Request) {
	if s.densityCache == nil {
		writeDensityJSON(w, densityNodesResponse{Nodes: []densityNodeSummary{}, Total: 0})
		return
	}

	all := s.densityCache.All()
	nodes := make([]densityNodeSummary, len(all))
	for i, nf := range all {
		nodes[i] = densityNodeSummary{
			NodeName:        nf.NodeName,
			LastSeen:        nf.LastSeen.Format("2006-01-02T15:04:05Z"),
			Stale:           s.densityCache.IsStale(nf.NodeName),
			System:          nf.System,
			ModelFitCount:   len(nf.ModelFits),
			Runtimes:        nf.Runtimes,
			InstalledModels: nf.InstalledModels,
		}
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeName < nodes[j].NodeName })
	writeDensityJSON(w, densityNodesResponse{Nodes: nodes, Total: len(nodes)})
}

// getDensityNode returns fitness data for a single node.
// GET /api/v1/density/nodes/{name}
func (s *Server) getDensityNode(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.densityCache == nil {
		http.Error(w, "fitness cache not available", http.StatusServiceUnavailable)
		return
	}

	nf, ok := s.densityCache.Get(name)
	if !ok {
		http.Error(w, "node not found in fitness cache", http.StatusNotFound)
		return
	}

	writeDensityJSON(w, nf)
}

// listDensityRuntimes returns all runtimes across the cluster.
// GET /api/v1/density/runtimes
func (s *Server) listDensityRuntimes(w http.ResponseWriter, r *http.Request) {
	if s.densityCache == nil {
		writeDensityJSON(w, map[string]interface{}{"nodes": []interface{}{}})
		return
	}

	type nodeRuntimes struct {
		NodeName string                     `json:"nodeName"`
		Runtimes []controller.RuntimeStatus `json:"runtimes"`
	}

	all := s.densityCache.All()
	result := make([]nodeRuntimes, 0, len(all))
	for _, nf := range all {
		if len(nf.Runtimes) > 0 {
			result = append(result, nodeRuntimes{NodeName: nf.NodeName, Runtimes: nf.Runtimes})
		}
	}

	writeDensityJSON(w, map[string]interface{}{"nodes": result})
}

// listDensityInstalledModels returns installed models across the cluster.
// GET /api/v1/density/installed-models
func (s *Server) listDensityInstalledModels(w http.ResponseWriter, r *http.Request) {
	if s.densityCache == nil {
		writeDensityJSON(w, map[string]interface{}{"nodes": []interface{}{}})
		return
	}

	type nodeModels struct {
		NodeName string                          `json:"nodeName"`
		Models   []controller.InstalledModelInfo `json:"models"`
	}

	all := s.densityCache.All()
	result := make([]nodeModels, 0, len(all))
	for _, nf := range all {
		if len(nf.InstalledModels) > 0 {
			result = append(result, nodeModels{NodeName: nf.NodeName, Models: nf.InstalledModels})
		}
	}

	writeDensityJSON(w, map[string]interface{}{"nodes": result})
}

// queryDensity returns ranked nodes for a model query.
// GET /api/v1/density/query?model=Qwen2.5&min_fit=good
func (s *Server) queryDensity(w http.ResponseWriter, r *http.Request) {
	modelQuery := r.URL.Query().Get("model")
	if modelQuery == "" {
		http.Error(w, "model query parameter is required", http.StatusBadRequest)
		return
	}

	minFit := r.URL.Query().Get("min_fit")
	if minFit == "" {
		minFit = "marginal"
	}

	if s.densityCache == nil {
		writeDensityJSON(w, densityQueryResponse{Query: modelQuery})
		return
	}

	queryLower := strings.ToLower(modelQuery)
	all := s.densityCache.All()

	var results []densityNodeResult
	for _, nf := range all {
		for _, fit := range nf.ModelFits {
			if !strings.Contains(strings.ToLower(fit.Name), queryLower) {
				continue
			}
			results = append(results, densityNodeResult{
				NodeName: nf.NodeName,
				Score:    fit.Score,
				FitLevel: fit.FitLevel,
				Model:    fit,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	writeDensityJSON(w, densityQueryResponse{
		Query:       modelQuery,
		RankedNodes: results,
	})
}

// getCatalog returns what models the cluster can run, aggregated across nodes.
// GET /api/v1/catalog
func (s *Server) getCatalog(w http.ResponseWriter, r *http.Request) {
	if s.densityCache == nil {
		writeDensityJSON(w, catalogResponse{Models: []catalogEntry{}, Total: 0})
		return
	}

	all := s.densityCache.All()

	// Aggregate: model name -> list of (node, score, fitLevel).
	type nodeHit struct {
		nodeName string
		score    float64
		fitLevel string
	}
	modelNodes := make(map[string][]nodeHit)

	for _, nf := range all {
		for _, fit := range nf.ModelFits {
			modelNodes[fit.Name] = append(modelNodes[fit.Name], nodeHit{
				nodeName: nf.NodeName,
				score:    fit.Score,
				fitLevel: fit.FitLevel,
			})
		}
	}

	entries := make([]catalogEntry, 0, len(modelNodes))
	for name, hits := range modelNodes {
		sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
		nodes := make([]nodeScore, len(hits))
		for i, h := range hits {
			nodes[i] = nodeScore{NodeName: h.nodeName, Score: h.score, FitLevel: h.fitLevel}
		}
		entries = append(entries, catalogEntry{
			ModelName: name,
			BestScore: hits[0].score,
			BestNode:  hits[0].nodeName,
			FitLevel:  hits[0].fitLevel,
			Nodes:     nodes,
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].ModelName < entries[j].ModelName })

	writeDensityJSON(w, catalogResponse{Models: entries, Total: len(entries)})
}

// writeDensityJSON writes a JSON response for fitness endpoints.
func writeDensityJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
