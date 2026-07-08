package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	llmfitv1alpha1 "github.com/sympozium-ai/llmfit-dra/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Claim-based placement (docs/positioning.md): sympozium expresses the model
// requirement as an llmfit.ai ModelClaim; the llmfit-dra controller resolves
// the physics into a same-named ResourceClaimTemplate; the STOCK
// kube-scheduler places the serving pod. Sympozium never picks a node here —
// no NodeSelector mutation, no probe pods, no fitness cache.

// +kubebuilder:rbac:groups=llmfit.ai,resources=modelclaims,verbs=get;list;watch;create;update;patch;delete

// usesDRAPlacement reports whether this Model places via ModelClaim:
// explicitly (mode "dra"), or in "auto" mode when the cluster serves both
// DRA and the ModelClaim CRD (runtime-detected, never compile-time).
func (r *ModelReconciler) usesDRAPlacement(model *sympoziumv1alpha1.Model) bool {
	switch model.Spec.Placement.Mode {
	case sympoziumv1alpha1.PlacementDRA:
		return true
	case sympoziumv1alpha1.PlacementAuto:
		return r.DRA != nil && r.DRA.Available()
	default:
		return false
	}
}

// reconcilePlacingDRA ensures the same-named ModelClaim and transitions to
// Pending once it resolves. Satisfiable is advisory (llmfit-dra semantics):
// an unsatisfiable-right-now claim still gets its template, pods queue, and
// the exact shortfall is surfaced in PlacementMessage instead of blocking.
func (r *ModelReconciler) reconcilePlacingDRA(ctx context.Context, model *sympoziumv1alpha1.Model, log logr.Logger) (ctrl.Result, error) {
	mc := &llmfitv1alpha1.ModelClaim{
		ObjectMeta: metav1.ObjectMeta{Name: model.Name, Namespace: model.Namespace},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, mc, func() error {
		if err := controllerutil.SetControllerReference(model, mc, r.Scheme); err != nil {
			return err
		}
		mc.Spec.Model = ModelQueryForModel(model)
		if v := model.Spec.Placement.MinTps; v != nil {
			tps := float64(*v)
			mc.Spec.MinTps = &tps
		}
		mc.Spec.MinComputeTFLOPS = model.Spec.Placement.MinComputeTFLOPS
		return nil
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring ModelClaim: %w", err)
	}

	resolved := apimeta.FindStatusCondition(mc.Status.Conditions, llmfitv1alpha1.ConditionResolved)
	switch {
	case resolved == nil:
		// Controller hasn't reconciled the claim yet.
		return r.holdPlacing(ctx, model, "waiting for ModelClaim to resolve", 5*time.Second)
	case resolved.Status != metav1.ConditionTrue:
		// InvalidModel / ResolveFailed / TemplateConflict: surface the exact
		// reason and keep polling — a model-DB update or a spec edit fixes
		// these without any action on the Model itself.
		return r.holdPlacing(ctx, model,
			fmt.Sprintf("ModelClaim not resolved (%s): %s", resolved.Reason, resolved.Message),
			30*time.Second)
	}

	if sat := apimeta.FindStatusCondition(mc.Status.Conditions, llmfitv1alpha1.ConditionSatisfiable); sat != nil {
		model.Status.PlacementMessage = sat.Message
		if sat.Status == metav1.ConditionFalse {
			log.Info("ModelClaim unsatisfiable right now — proceeding; pods will queue",
				"model", model.Name, "shortfall", sat.Message)
		}
	} else {
		model.Status.PlacementMessage = "placed via ModelClaim"
	}
	// PlacedNode is intentionally left empty here: the scheduler decides at
	// pod-bind time. reconcileReady backfills it from the running pod.
	return r.transitionToPending(ctx, model, log)
}

// holdPlacing persists a placement status message and requeues.
func (r *ModelReconciler) holdPlacing(ctx context.Context, model *sympoziumv1alpha1.Model, msg string, after time.Duration) (ctrl.Result, error) {
	model.Status.Phase = sympoziumv1alpha1.ModelPhasePlacing
	model.Status.PlacementMessage = msg
	if err := r.Status().Update(ctx, model); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: after}, nil
}
