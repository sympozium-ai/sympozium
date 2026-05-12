package apiserver

import (
	"context"
	"net/http"
	"sort"
	"strings"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// costResponse is the response for GET /api/v1/fitness/cost.
type costResponse struct {
	Models     []modelCost     `json:"models"`
	Namespaces []namespaceCost `json:"namespaces"`
}

type modelCost struct {
	Name           string  `json:"name"`
	Namespace      string  `json:"namespace"`
	PlacedNode     string  `json:"placedNode"`
	Phase          string  `json:"phase"`
	GPU            int     `json:"gpu"`
	MemoryRequired float64 `json:"memoryRequiredGb"`
	UtilizationPct float64 `json:"utilizationPct"`
	FitnessScore   int     `json:"fitnessScore"`
}

type namespaceCost struct {
	Namespace  string  `json:"namespace"`
	ModelCount int     `json:"modelCount"`
	TotalGPU   int     `json:"totalGpu"`
	TotalMemGB float64 `json:"totalMemoryGb"`
}

// handleCost handles GET /api/v1/fitness/cost.
// Correlates deployed models with fitness data for resource attribution.
func (s *Server) handleCost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var models sympoziumv1alpha1.ModelList
	if err := s.client.List(ctx, &models, &client.ListOptions{}); err != nil {
		http.Error(w, "failed to list models: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var modelCosts []modelCost
	nsAgg := make(map[string]*namespaceCost)

	for i := range models.Items {
		m := &models.Items[i]
		mc := modelCost{
			Name:         m.Name,
			Namespace:    m.Namespace,
			PlacedNode:   m.Status.PlacedNode,
			Phase:        string(m.Status.Phase),
			FitnessScore: m.Status.PlacementScore,
		}

		// Get GPU from spec.
		if m.Spec.Resources.GPU > 0 {
			mc.GPU = int(m.Spec.Resources.GPU)
		}

		// Enrich with fitness cache data if available.
		if s.fitnessCache != nil && m.Status.PlacedNode != "" {
			mc.MemoryRequired, mc.UtilizationPct = s.lookupModelCost(ctx, m)
		}

		modelCosts = append(modelCosts, mc)

		// Aggregate by namespace.
		ns, ok := nsAgg[m.Namespace]
		if !ok {
			ns = &namespaceCost{Namespace: m.Namespace}
			nsAgg[m.Namespace] = ns
		}
		ns.ModelCount++
		ns.TotalGPU += mc.GPU
		ns.TotalMemGB += mc.MemoryRequired
	}

	nsCosts := make([]namespaceCost, 0, len(nsAgg))
	for _, ns := range nsAgg {
		nsCosts = append(nsCosts, *ns)
	}
	sort.Slice(nsCosts, func(i, j int) bool { return nsCosts[i].TotalMemGB > nsCosts[j].TotalMemGB })
	sort.Slice(modelCosts, func(i, j int) bool { return modelCosts[i].MemoryRequired > modelCosts[j].MemoryRequired })

	writeFitnessJSON(w, costResponse{
		Models:     modelCosts,
		Namespaces: nsCosts,
	})
}

// lookupModelCost looks up memory/utilization from the fitness cache for a placed model.
func (s *Server) lookupModelCost(_ context.Context, m *sympoziumv1alpha1.Model) (memGB float64, utilPct float64) {
	nf, ok := s.fitnessCache.Get(m.Status.PlacedNode)
	if !ok {
		return 0, 0
	}

	modelQuery := m.Spec.Source.ModelID
	if modelQuery == "" {
		modelQuery = m.Name
	}

	for _, fit := range nf.ModelFits {
		if containsLower(fit.Name, modelQuery) {
			return fit.MemoryRequired, fit.UtilizationPct
		}
	}
	return 0, 0
}

func containsLower(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
