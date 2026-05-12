package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// DensityWatcher monitors the DensityCache for degraded nodes and triggers
// model re-placement when fitness drops significantly. Implements
// sigs.k8s.io/controller-runtime manager.Runnable.
type DensityWatcher struct {
	Client           client.Client
	Cache            *DensityCache
	EventBus         eventbus.EventBus
	Log              logr.Logger
	CheckInterval    time.Duration // how often to check (default 30s)
	DegradeThreshold float64       // fractional score drop that triggers eviction (default 0.3 = 30%)
}

// Start runs the watcher loop until ctx is cancelled.
func (fw *DensityWatcher) Start(ctx context.Context) error {
	if fw.CheckInterval == 0 {
		fw.CheckInterval = 30 * time.Second
	}
	if fw.DegradeThreshold == 0 {
		fw.DegradeThreshold = 0.3
	}

	fw.Log.Info("Starting density watcher",
		"interval", fw.CheckInterval,
		"degradeThreshold", fw.DegradeThreshold,
	)

	ticker := time.NewTicker(fw.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fw.Log.Info("Stopping density watcher")
			return nil
		case <-ticker.C:
			fw.check(ctx)
		}
	}
}

// check iterates all Ready models with auto-placement and evaluates whether
// the placed node's fitness has degraded.
func (fw *DensityWatcher) check(ctx context.Context) {
	var models sympoziumv1alpha1.ModelList
	if err := fw.Client.List(ctx, &models); err != nil {
		fw.Log.V(1).Info("Failed to list models for fitness check", "error", err)
		return
	}

	for i := range models.Items {
		model := &models.Items[i]

		// Only check models that are Ready and were auto-placed.
		if model.Status.Phase != sympoziumv1alpha1.ModelPhaseReady {
			continue
		}
		if model.Spec.Placement.Mode != sympoziumv1alpha1.PlacementAuto {
			continue
		}
		if model.Status.PlacedNode == "" || model.Status.PlacementScore == 0 {
			continue
		}

		fw.evaluateModel(ctx, model)
	}
}

// evaluateModel checks a single model's placed node for fitness degradation.
func (fw *DensityWatcher) evaluateModel(ctx context.Context, model *sympoziumv1alpha1.Model) {
	log := fw.Log.WithValues("model", model.Name, "namespace", model.Namespace, "node", model.Status.PlacedNode)

	// Check if the node is stale (stopped reporting).
	if fw.Cache.IsStale(model.Status.PlacedNode) {
		log.Info("Placed node is stale — triggering re-placement")
		fw.triggerEviction(ctx, model, "node stopped reporting density data")
		return
	}

	// Look up current fitness for the placed node and model.
	nf, ok := fw.Cache.Get(model.Status.PlacedNode)
	if !ok {
		return // No data — not actionable.
	}

	// Find the model's current fitness score on this node.
	modelQuery := ModelQueryForModel(model)
	var currentScore float64
	found := false
	for _, fit := range nf.ModelFits {
		if fitContainsIgnoreCase(fit.Name, modelQuery) {
			currentScore = fit.Score
			found = true
			break
		}
	}

	if !found {
		// Model not in density data at all — may have been evicted from the
		// node's capability set.
		log.Info("Model no longer appears in node density data — triggering re-placement")
		fw.triggerEviction(ctx, model, "model no longer fits on placed node")
		return
	}

	// Check for significant score degradation.
	originalScore := float64(model.Status.PlacementScore)
	if originalScore == 0 {
		return
	}

	drop := (originalScore - currentScore) / originalScore
	if drop >= fw.DegradeThreshold {
		log.Info("Fitness score degraded beyond threshold",
			"originalScore", originalScore,
			"currentScore", currentScore,
			"drop", fmt.Sprintf("%.0f%%", drop*100),
		)
		fw.triggerEviction(ctx, model,
			fmt.Sprintf("fitness degraded %.0f%% (was %d, now %.0f)", drop*100, model.Status.PlacementScore, currentScore),
		)
	}
}

// triggerEviction transitions a model back to Placing so it can be re-placed.
func (fw *DensityWatcher) triggerEviction(ctx context.Context, model *sympoziumv1alpha1.Model, reason string) {
	log := fw.Log.WithValues("model", model.Name, "namespace", model.Namespace)

	// Clear placement data so reconcilePlacing starts fresh.
	model.Spec.NodeSelector = nil
	if err := fw.Client.Update(ctx, model); err != nil {
		log.Info("Failed to clear node selector for eviction", "error", err)
		return
	}

	model.Status.Phase = sympoziumv1alpha1.ModelPhasePlacing
	model.Status.Message = "Re-placing model: " + reason
	model.Status.PlacedNode = ""
	model.Status.PlacementScore = 0
	model.Status.PlacementMessage = ""
	if err := fw.Client.Status().Update(ctx, model); err != nil {
		log.Info("Failed to update status for eviction", "error", err)
		return
	}

	log.Info("Model eviction triggered", "reason", reason)

	// Publish eviction event.
	if fw.EventBus != nil {
		metadata := map[string]string{
			"model":     model.Name,
			"namespace": model.Namespace,
			"reason":    reason,
		}
		evt, err := eventbus.NewEvent(eventbus.TopicModelEviction, metadata, map[string]string{
			"model":  model.Name,
			"reason": reason,
		})
		if err == nil {
			_ = fw.EventBus.Publish(ctx, eventbus.TopicModelEviction, evt)
		}
	}
}

// fitContainsIgnoreCase checks if s contains substr (case-insensitive).
func fitContainsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
