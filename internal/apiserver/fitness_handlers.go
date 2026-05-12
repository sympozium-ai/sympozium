package apiserver

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/sympozium-ai/sympozium/internal/controller"
)

// fitnessNodesResponse is the response for GET /api/v1/fitness/nodes.
type fitnessNodesResponse struct {
	Nodes []fitnessNodeSummary `json:"nodes"`
	Total int                  `json:"total"`
}

type fitnessNodeSummary struct {
	NodeName        string                          `json:"nodeName"`
	LastSeen        string                          `json:"lastSeen"`
	Stale           bool                            `json:"stale"`
	System          controller.SystemSpecs          `json:"system"`
	ModelFitCount   int                             `json:"modelFitCount"`
	Runtimes        []controller.RuntimeStatus      `json:"runtimes,omitempty"`
	InstalledModels []controller.InstalledModelInfo `json:"installedModels,omitempty"`
}

// fitnessQueryResponse is the response for GET /api/v1/fitness/query.
type fitnessQueryResponse struct {
	Query       string              `json:"query"`
	RankedNodes []fitnessNodeResult `json:"rankedNodes"`
}

type fitnessNodeResult struct {
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

// listFitnessNodes returns all nodes with fitness data.
// GET /api/v1/fitness/nodes
func (s *Server) listFitnessNodes(w http.ResponseWriter, r *http.Request) {
	if s.fitnessCache == nil {
		writeFitnessJSON(w, fitnessNodesResponse{Nodes: []fitnessNodeSummary{}, Total: 0})
		return
	}

	all := s.fitnessCache.All()
	nodes := make([]fitnessNodeSummary, len(all))
	for i, nf := range all {
		nodes[i] = fitnessNodeSummary{
			NodeName:        nf.NodeName,
			LastSeen:        nf.LastSeen.Format("2006-01-02T15:04:05Z"),
			Stale:           s.fitnessCache.IsStale(nf.NodeName),
			System:          nf.System,
			ModelFitCount:   len(nf.ModelFits),
			Runtimes:        nf.Runtimes,
			InstalledModels: nf.InstalledModels,
		}
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeName < nodes[j].NodeName })
	writeFitnessJSON(w, fitnessNodesResponse{Nodes: nodes, Total: len(nodes)})
}

// getFitnessNode returns fitness data for a single node.
// GET /api/v1/fitness/nodes/{name}
func (s *Server) getFitnessNode(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.fitnessCache == nil {
		http.Error(w, "fitness cache not available", http.StatusServiceUnavailable)
		return
	}

	nf, ok := s.fitnessCache.Get(name)
	if !ok {
		http.Error(w, "node not found in fitness cache", http.StatusNotFound)
		return
	}

	writeFitnessJSON(w, nf)
}

// listFitnessRuntimes returns all runtimes across the cluster.
// GET /api/v1/fitness/runtimes
func (s *Server) listFitnessRuntimes(w http.ResponseWriter, r *http.Request) {
	if s.fitnessCache == nil {
		writeFitnessJSON(w, map[string]interface{}{"nodes": []interface{}{}})
		return
	}

	type nodeRuntimes struct {
		NodeName string                     `json:"nodeName"`
		Runtimes []controller.RuntimeStatus `json:"runtimes"`
	}

	all := s.fitnessCache.All()
	result := make([]nodeRuntimes, 0, len(all))
	for _, nf := range all {
		if len(nf.Runtimes) > 0 {
			result = append(result, nodeRuntimes{NodeName: nf.NodeName, Runtimes: nf.Runtimes})
		}
	}

	writeFitnessJSON(w, map[string]interface{}{"nodes": result})
}

// listFitnessInstalledModels returns installed models across the cluster.
// GET /api/v1/fitness/installed-models
func (s *Server) listFitnessInstalledModels(w http.ResponseWriter, r *http.Request) {
	if s.fitnessCache == nil {
		writeFitnessJSON(w, map[string]interface{}{"nodes": []interface{}{}})
		return
	}

	type nodeModels struct {
		NodeName string                          `json:"nodeName"`
		Models   []controller.InstalledModelInfo `json:"models"`
	}

	all := s.fitnessCache.All()
	result := make([]nodeModels, 0, len(all))
	for _, nf := range all {
		if len(nf.InstalledModels) > 0 {
			result = append(result, nodeModels{NodeName: nf.NodeName, Models: nf.InstalledModels})
		}
	}

	writeFitnessJSON(w, map[string]interface{}{"nodes": result})
}

// queryFitness returns ranked nodes for a model query.
// GET /api/v1/fitness/query?model=Qwen2.5&min_fit=good
func (s *Server) queryFitness(w http.ResponseWriter, r *http.Request) {
	modelQuery := r.URL.Query().Get("model")
	if modelQuery == "" {
		http.Error(w, "model query parameter is required", http.StatusBadRequest)
		return
	}

	minFit := r.URL.Query().Get("min_fit")
	if minFit == "" {
		minFit = "marginal"
	}

	if s.fitnessCache == nil {
		writeFitnessJSON(w, fitnessQueryResponse{Query: modelQuery})
		return
	}

	queryLower := strings.ToLower(modelQuery)
	all := s.fitnessCache.All()

	var results []fitnessNodeResult
	for _, nf := range all {
		for _, fit := range nf.ModelFits {
			if !strings.Contains(strings.ToLower(fit.Name), queryLower) {
				continue
			}
			results = append(results, fitnessNodeResult{
				NodeName: nf.NodeName,
				Score:    fit.Score,
				FitLevel: fit.FitLevel,
				Model:    fit,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	writeFitnessJSON(w, fitnessQueryResponse{
		Query:       modelQuery,
		RankedNodes: results,
	})
}

// getCatalog returns what models the cluster can run, aggregated across nodes.
// GET /api/v1/catalog
func (s *Server) getCatalog(w http.ResponseWriter, r *http.Request) {
	if s.fitnessCache == nil {
		writeFitnessJSON(w, catalogResponse{Models: []catalogEntry{}, Total: 0})
		return
	}

	all := s.fitnessCache.All()

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

	writeFitnessJSON(w, catalogResponse{Models: entries, Total: len(entries)})
}

// writeFitnessJSON writes a JSON response for fitness endpoints.
func writeFitnessJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
