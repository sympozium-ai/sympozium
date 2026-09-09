package controller

import (
	"context"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AgentRunTurnReconciler delivers data to an already-admitted parent. It never
// creates a parent, Kubernetes workload, model grant or tool selection.
type AgentRunTurnReconciler struct {
	client.Client
	APIReader        client.Reader
	ParentConfigPath string
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=agentrunturns,verbs=get;list;watch
// +kubebuilder:rbac:groups=sympozium.ai,resources=agentrunturns/status,verbs=get;update;patch
func (r *AgentRunTurnReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var turn api.AgentRunTurn
	if err := reader.Get(ctx, request.NamespacedName, &turn); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if turn.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	done, err := cellnparent.ReconcileTurn(ctx, r.Client, reader, request.NamespacedName, r.ParentConfigPath)
	if statusErr := r.recordTurnObservation(ctx, reader, &turn, err != nil); statusErr != nil {
		return ctrl.Result{}, statusErr
	}
	if err != nil {
		ctrl.LoggerFrom(ctx).Info("Parent turn pending reconciliation", "turn", request.NamespacedName, "reason", err.Error())
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if done {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *AgentRunTurnReconciler) recordTurnObservation(ctx context.Context, reader client.Reader, observed *api.AgentRunTurn, failed bool) error {
	var fresh api.AgentRunTurn
	if err := reader.Get(ctx, client.ObjectKeyFromObject(observed), &fresh); err != nil {
		return client.IgnoreNotFound(err)
	}
	if fresh.UID != observed.UID || fresh.Generation != observed.Generation || fresh.DeletionTimestamp != nil {
		return nil
	}
	before := fresh.DeepCopy()
	condition := metav1.Condition{Type: "CellnTurnComplete", Status: metav1.ConditionFalse, Reason: "Pending", Message: "Waiting for turn admission or a committed result from the original parent.", ObservedGeneration: fresh.Generation}
	if fresh.Spec.CancelRequested {
		condition.Reason = "CancellationPending"
		condition.Message = "Cancellation requested for this turn only. Waiting for the original parent's committed result; cancellation does not confirm child teardown."
		if fresh.Status.CancelAttempted {
			condition.Message = "Cancellation send was attempted and will not be automatically repeated. Waiting for the original parent's committed result; delivery and child teardown may be uncertain."
		}
	}
	if failed {
		condition.Reason = "ReconciliationRequired"
		condition.Message = "Turn admission or outcome could not be confirmed. The original request is retained; do not resubmit it. Ask the operator to reconcile its parent and turn record."
	}
	// A concurrent committed result is stronger evidence than a failed read.
	if fresh.Status.Execution != nil && fresh.Status.Execution.Result != nil {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "Committed"
		condition.Message = "Turn result recorded. Completion does not imply success; inspect the result."
	}
	meta.SetStatusCondition(&fresh.Status.Conditions, condition)
	if equality.Semantic.DeepEqual(before.Status, fresh.Status) {
		return nil
	}
	return r.Status().Update(ctx, &fresh)
}

func (r *AgentRunTurnReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).For(&api.AgentRunTurn{}).Complete(r)
}
