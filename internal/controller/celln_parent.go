package controller

import (
	"context"
	"fmt"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ParentAdmission interface {
	Admit(context.Context, types.NamespacedName) error
}

func (r *AgentRunReconciler) parentReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// Native parent readiness is not task success. Keep the enduring run live for
// turn delivery; never create an OCI workload or mark the initial task complete.
func (r *AgentRunReconciler) reconcileCellnParent(ctx context.Context, run *api.AgentRun) (ctrl.Result, error) {
	if r.ParentConfigPath == "" {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, fmt.Errorf("parent operator configuration unavailable; preserve owner")
	}
	if r.ParentAdmission != nil && run.Status.CellnParent == nil {
		if err := r.ParentAdmission.Admit(ctx, client.ObjectKeyFromObject(run)); err != nil {
			if statusErr := r.recordParentAdmissionPending(ctx, run); statusErr != nil {
				return ctrl.Result{}, statusErr
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, fmt.Errorf("parent admission unavailable; no execution attempted: %w", err)
		}
	}
	observed, observeErr := cellnparent.ReconcileStart(ctx, r.Client, r.parentReader(), client.ObjectKeyFromObject(run), r.ParentConfigPath)
	var turnDone bool
	var turnErr error
	turnChecked := false
	if observeErr == nil && observed.Owner != nil && observed.Owner.Live && observed.Owner.Status == "Ready" {
		turnChecked = true
		turnDone, turnErr = cellnparent.ReconcileInitialTurn(ctx, r.Client, r.parentReader(), client.ObjectKeyFromObject(run), r.ParentConfigPath)
	} else if observeErr == nil && observed.Owner != nil && (observed.Owner.Status == "ContextLost" || observed.Owner.Status == "Stopped" || observed.Owner.Status == "TeardownUncertain") {
		turnChecked = true
		turnDone, turnErr = cellnparent.RecoverInitialTurn(ctx, r.Client, r.parentReader(), client.ObjectKeyFromObject(run), r.ParentConfigPath)
	}
	// Startup may have updated status; never overwrite that durable claim with
	// the stale object passed into this reconciliation.
	var fresh api.AgentRun
	if err := r.parentReader().Get(ctx, client.ObjectKeyFromObject(run), &fresh); err != nil {
		return ctrl.Result{}, err
	}
	if fresh.UID != run.UID || fresh.DeletionTimestamp != nil {
		return ctrl.Result{Requeue: true}, nil
	}
	if fresh.Status.Phase != "" && fresh.Status.Phase != api.AgentRunPhasePending && fresh.Status.Phase != api.AgentRunPhaseRunning {
		return ctrl.Result{Requeue: true}, nil
	}
	before := fresh.DeepCopy()
	if turnChecked {
		turnCondition := metav1.Condition{Type: "CellnInitialTurnComplete", Status: metav1.ConditionFalse, Reason: "Pending", Message: "Initial turn pending; preserving its original identity", ObservedGeneration: fresh.Generation}
		if turnDone {
			turnCondition.Status = metav1.ConditionTrue
			turnCondition.Reason = "Committed"
			turnCondition.Message = "Initial turn result recorded; parent lifecycle is reported separately"
		}
		if turnErr != nil {
			turnCondition.Reason = "ReconciliationRequired"
			turnCondition.Message = "Initial turn outcome unavailable; no automatic resubmission"
		}
		meta.SetStatusCondition(&fresh.Status.Conditions, turnCondition)
	}
	condition := metav1.Condition{Type: "CellnParentReady", Status: metav1.ConditionFalse, Reason: "Initializing", Message: "Parent startup pending; no turn completion implied", ObservedGeneration: fresh.Generation}
	if observeErr != nil {
		condition.Reason = "ReconciliationRequired"
		condition.Message = "Parent outcome unavailable; preserving original incarnation without replay"
	} else if observed.Owner != nil {
		switch observed.Owner.Status {
		case "Ready", "TurnActive":
			fresh.Status.Phase = api.AgentRunPhaseRunning
			condition.Status = metav1.ConditionTrue
			condition.Reason = observed.Owner.Status
			condition.Message = "Native parent initialized; turn completion is tracked separately"
		case "ContextLost", "Stopped", "TeardownUncertain":
			meta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{Type: "CellnParentReady", Status: metav1.ConditionFalse, Reason: observed.Owner.Status, Message: "Parent context unavailable; no automatic reconstruction", ObservedGeneration: fresh.Generation})
			// failRun rereads status and applies only terminal fields. Persist
			// these independent turn/owner observations before that fresh read.
			if err := r.Status().Update(ctx, &fresh); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.failRun(ctx, &fresh, "Celln parent context lost or stopped; reconcile recorded turns")
		}
	}
	meta.SetStatusCondition(&fresh.Status.Conditions, condition)
	if apiequality.Semantic.DeepEqual(before.Status, fresh.Status) {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.Status().Update(ctx, &fresh); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// Report admission without leaking operator paths or overwriting a concurrently
// bound parent. This observation grants no authority and does not start work.
func (r *AgentRunReconciler) recordParentAdmissionPending(ctx context.Context, observed *api.AgentRun) error {
	var fresh api.AgentRun
	if err := r.parentReader().Get(ctx, client.ObjectKeyFromObject(observed), &fresh); err != nil {
		return err
	}
	if fresh.UID != observed.UID || fresh.Generation != observed.Generation || fresh.DeletionTimestamp != nil || fresh.Status.CellnParent != nil || (fresh.Status.Phase != "" && fresh.Status.Phase != api.AgentRunPhasePending) {
		return nil
	}
	before := fresh.DeepCopy()
	meta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{Type: "CellnParentReady", Status: metav1.ConditionFalse, Reason: "AdmissionPending", Message: "Waiting for a matching operator-prepared parent registration and current grants. Ask the operator to check admission; do not create a replacement run.", ObservedGeneration: fresh.Generation})
	if apiequality.Semantic.DeepEqual(before.Status, fresh.Status) {
		return nil
	}
	return r.Status().Update(ctx, &fresh)
}

func (r *AgentRunReconciler) stopCellnParent(ctx context.Context, run *api.AgentRun) error {
	if run.Status.CellnParent == nil || !run.Status.CellnParent.CreateAttempted {
		return nil
	}
	if r.ParentConfigPath == "" {
		return fmt.Errorf("parent cleanup requires original operator configuration")
	}
	return cellnparent.ReconcileStop(ctx, r.parentReader(), client.ObjectKeyFromObject(run), r.ParentConfigPath)
}
