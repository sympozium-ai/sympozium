package webhook

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller"
)

// ModelDensityValidator is a validating webhook that rejects Model CRs
// when the DensityCache shows no node can fit the model. Only active for
// CREATE operations on auto-placement models.
//
// Gracefully degrades: if the cache is empty (DaemonSet not deployed or
// still warming up), the request is always allowed.
type ModelDensityValidator struct {
	Cache   *controller.DensityCache
	Log     logr.Logger
	Decoder admission.Decoder
}

// Handle validates a Model creation request against the cluster's current
// hardware fitness data.
func (v *ModelDensityValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	// Only validate CREATE requests.
	if req.Operation != admissionv1.Create {
		return admission.Allowed("fitness check only applies to CREATE")
	}

	model := &sympoziumv1alpha1.Model{}
	if err := v.Decoder.Decode(req, model); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Only check models requesting auto-placement.
	if model.Spec.Placement.Mode != sympoziumv1alpha1.PlacementAuto {
		return admission.Allowed("manual placement — skipping fitness check")
	}

	// Graceful degradation: if cache is nil or empty, allow.
	if v.Cache == nil || v.Cache.NodeCount() == 0 {
		return admission.Allowed("fitness cache not populated — skipping pre-flight check")
	}

	// Look up the model query and check if any node can run it.
	modelQuery := controller.ModelQueryForModel(model)

	bestNode, bestScore, _ := v.Cache.BestNodeForModel(modelQuery, "marginal")
	if bestNode == "" {
		nodes := v.Cache.All()
		nodeNames := make([]string, len(nodes))
		for i, n := range nodes {
			nodeNames[i] = n.NodeName
		}
		msg := fmt.Sprintf(
			"no node in the cluster can run model %q (checked %d nodes: %s). "+
				"Deploy on a node with more VRAM/RAM, or use manual placement.",
			modelQuery, len(nodes), strings.Join(nodeNames, ", "),
		)
		v.Log.Info("Pre-flight fitness check failed", "model", model.Name, "query", modelQuery)
		return admission.Denied(msg)
	}

	v.Log.V(1).Info("Pre-flight fitness check passed",
		"model", model.Name,
		"bestNode", bestNode,
		"score", bestScore,
	)
	return admission.Allowed(fmt.Sprintf("model fits on %s (score: %.0f)", bestNode, bestScore))
}
