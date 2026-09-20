package controller

import (
	"context"
	"errors"

	"github.com/sympozium-ai/sympozium/internal/modelbudget"
	"reflect"
	"slices"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/sympozium-ai/sympozium/internal/cellnscoped"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const agentRunTurnFinalizer = "sympozium.ai/agentrunturn-finalizer"

// AgentRunTurnReconciler admits fresh, bounded operations to an already-owned
// scoped native parent. It never creates a parent or falls back to a legacy
// dispatcher or standing model grant.
type AgentRunTurnReconciler struct {
	client.Client
	APIReader        client.Reader
	ScopedDispatcher *cellnscoped.Dispatcher
	// ParentConfigPath remains source-compatible for the parent-only binary but
	// is deliberately not an execution fallback for scoped turns.
	ParentConfigPath string
}

// turnOnNativeParent reports whether a turn belongs to a run the fleet's
// provision path admitted (status.cellnParent) rather than to a scoped run.
// A turn already bound to a scoped operation stays scoped; a turn that carries
// a parent-path execution record stays on the parent path even after its run
// is gone. A turn whose run is bound to neither path keeps the scoped handling
// this manager has always given it.
func (r *AgentRunTurnReconciler) turnOnNativeParent(ctx context.Context, reader client.Reader, turn *api.AgentRunTurn) (bool, error) {
	if turn.Status.CellnScoped != nil {
		return false, nil
	}
	if turn.Status.Execution != nil {
		// The scoped path writes status.execution only beside status.cellnScoped.
		return true, nil
	}
	var run api.AgentRun
	if err := reader.Get(ctx, client.ObjectKey{Namespace: turn.Namespace, Name: turn.Spec.RunName}, &run); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return string(run.UID) == turn.Spec.RunUID && run.Status.CellnScoped == nil && run.Status.CellnParent != nil, nil
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=agentrunturns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=agentrunturns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=agentrunturns/finalizers,verbs=update;patch
func (r *AgentRunTurnReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var turn api.AgentRunTurn
	if err := reader.Get(ctx, request.NamespacedName, &turn); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// The separately deployed parent-only controller is an explicit legacy
	// mode, not a fallback from scoped reconciliation. The primary manager never
	// supplies ParentConfigPath to this reconciler.
	parentPath := r.ScopedDispatcher == nil && r.ParentConfigPath != ""
	if r.ScopedDispatcher != nil && r.ParentConfigPath != "" {
		// Both enduring paths on one manager: a turn follows its parent run.
		onParent, err := r.turnOnNativeParent(ctx, reader, &turn)
		if err != nil {
			return ctrl.Result{}, err
		}
		parentPath = onParent
	}
	if parentPath {
		if !turn.DeletionTimestamp.IsZero() {
			// A turn that waited under the scoped finalizer before its run was
			// bound to a native parent never acquired a scoped operation.
			if r.ScopedDispatcher != nil && turn.Status.CellnScoped == nil && controllerutil.ContainsFinalizer(&turn, agentRunTurnFinalizer) {
				return r.removeTurnFinalizer(ctx, &turn)
			}
			return ctrl.Result{}, nil
		}
		done, err := cellnparent.ReconcileTurn(ctx, r.Client, reader, request.NamespacedName, r.ParentConfigPath)
		if statusErr := r.recordTurnObservation(ctx, reader, &turn, err); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		if err != nil {
			ctrl.LoggerFrom(ctx).Info("Native turn outcome requires reconciliation", "turn", request.NamespacedName, "cause", err.Error())
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if done {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if turn.Status.CellnScoped != nil && turn.Status.CellnScoped.CleanupConfirmed {
		return r.removeTurnFinalizer(ctx, &turn)
	}
	if condition := meta.FindStatusCondition(turn.Status.Conditions, "CellnTurnComplete"); condition != nil && condition.Status == metav1.ConditionTrue && condition.Reason == "CancelledBeforeAdmission" {
		return r.removeTurnFinalizer(ctx, &turn)
	}
	if turn.Spec.CancelRequested && turn.Status.CellnScoped == nil {
		if err := r.updateTurnStatus(ctx, &turn, func(current *api.AgentRunTurn) error {
			meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "CellnTurnComplete", Status: metav1.ConditionTrue, Reason: "CancelledBeforeAdmission", Message: "Turn cancelled before native dispatch; no native operation was started.", ObservedGeneration: current.Generation})
			return nil
		}); err != nil {
			return ctrl.Result{}, err
		}
		return r.removeTurnFinalizer(ctx, &turn)
	}
	if turn.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(&turn, agentRunTurnFinalizer) {
		patch := client.MergeFrom(turn.DeepCopy())
		controllerutil.AddFinalizer(&turn, agentRunTurnFinalizer)
		if err := r.Patch(ctx, &turn, patch); err != nil {
			return ctrl.Result{}, err
		}
		if err := reader.Get(ctx, request.NamespacedName, &turn); err != nil {
			return ctrl.Result{}, err
		}
	}
	if r.ScopedDispatcher == nil {
		return r.turnUncertain(ctx, &turn, "ReconciliationRequired", errors.New("scoped turn dispatcher is not configured"))
	}
	if condition := meta.FindStatusCondition(turn.Status.Conditions, "CellnTurnComplete"); condition != nil && condition.Status == metav1.ConditionTrue && condition.Reason == "BudgetExhausted" {
		return r.cleanupTurn(ctx, &turn)
	}
	if !turn.DeletionTimestamp.IsZero() || turn.Spec.CancelRequested {
		return r.cleanupTurn(ctx, &turn)
	}

	if turn.Status.CellnScoped == nil && turn.Status.Execution == nil {
		var original api.AgentRun
		err := reader.Get(ctx, client.ObjectKey{Namespace: turn.Namespace, Name: turn.Spec.RunName}, &original)
		if apierrors.IsNotFound(err) || (err == nil && (string(original.UID) != turn.Spec.RunUID || !original.DeletionTimestamp.IsZero() || (original.Status.CellnScoped != nil && original.Status.CellnScoped.CleanupConfirmed))) {
			if err := r.updateTurnStatus(ctx, &turn, func(current *api.AgentRunTurn) error {
				meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "CellnTurnComplete", Status: metav1.ConditionTrue, Reason: "CancelledBeforeAdmission", Message: "Original parent is closed or unavailable; this turn never acquired native dispatch authority.", ObservedGeneration: current.Generation})
				return nil
			}); err != nil {
				return ctrl.Result{}, err
			}
			return r.removeTurnFinalizer(ctx, &turn)
		}
		if err != nil {
			return r.turnUncertain(ctx, &turn, "OriginalParentUnavailable", err)
		}
	}
	parent, err := r.scopedTurnParent(ctx, reader, &turn)
	if err != nil {
		return r.turnUncertain(ctx, &turn, "OriginalParentUnavailable", err)
	}
	originalPrepared, originalFinal, err := r.loadOriginalScoped(ctx, parent)
	if err != nil {
		return r.turnUncertain(ctx, &turn, "OriginalAuthorityUnavailable", err)
	}

	if turn.Status.CellnScoped == nil {
		prepared, err := r.ScopedDispatcher.PrepareTurn(ctx, client.ObjectKeyFromObject(parent), client.ObjectKeyFromObject(&turn), originalFinal, parent.Status.CellnScoped.ParentIncarnation)
		if err != nil {
			return r.turnUncertain(ctx, &turn, "PreparationUnconfirmed", err)
		}
		final, err := r.ScopedDispatcher.EnsureTurnFinal(ctx, prepared, originalFinal)
		if err != nil {
			return r.turnUncertain(ctx, &turn, "FinalDecisionUnconfirmed", err)
		}
		want := &api.CellnScopedStatus{PreparationName: prepared.Name, PreparationUID: string(prepared.UID), DecisionName: final.Name, DecisionUID: string(final.UID), ParentIncarnation: parent.Status.CellnScoped.ParentIncarnation, TurnID: string(turn.UID)}
		if err := r.updateTurnStatus(ctx, &turn, func(current *api.AgentRunTurn) error {
			if current.Status.CellnScoped == nil {
				current.Status.CellnScoped = want
				current.Status.ParentIncarnation = want.ParentIncarnation
			}
			return nil
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	prepared, final, err := r.loadTurnScoped(ctx, parent, &turn, originalPrepared, originalFinal)
	if err != nil {
		return r.turnUncertain(ctx, &turn, "TurnAuthorityUnavailable", err)
	}
	s := turn.Status.CellnScoped
	if !s.StartAttempted {
		if err := r.ScopedDispatcher.RevalidateAdmission(ctx, prepared); err != nil {
			return r.turnUncertain(ctx, &turn, "AuthorityChangedBeforeAdmission", err)
		}
	}
	if s.ReceiverID == "" {
		enrolled, err := r.ScopedDispatcher.Enroll(ctx, prepared, final)
		if err != nil {
			return r.turnUncertain(ctx, &turn, "ReceiverEnrollmentUnconfirmed", err)
		}
		if err := validateScopedOwner(enrolled.ID, enrolled.Owner); err != nil {
			return ctrl.Result{}, err
		}
		if enrolled.Owner != parent.Status.CellnScoped.Owner {
			return r.turnUncertain(ctx, &turn, "OriginalOwnerUnavailable", errors.New("turn enrollment did not preserve the original native parent owner"))
		}
		if err := r.updateTurnScoped(ctx, &turn, func(state *api.CellnScopedStatus) error {
			state.ReceiverID, state.Owner = enrolled.ID, enrolled.Owner
			return nil
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if s.StartAttempted {
		if turn.Spec.CancelRequested {
			return r.cleanupTurn(ctx, &turn)
		}
		observed, err := r.ScopedDispatcher.Read(ctx, s.ReceiverID, final)
		if err != nil {
			return r.turnUncertain(ctx, &turn, "ExecutionOutcomeUnconfirmed", err)
		}
		return r.applyTurnStatus(ctx, &turn, prepared, final, observed)
	}

	decision, execution, model, err := r.ScopedDispatcher.StartTokens(final)
	if err != nil {
		return r.turnUncertain(ctx, &turn, "TurnAdmissionWindowUnavailable", err)
	}
	if decision.Route.Provider != "none" && !s.GatewayRegistered {
		if !s.GatewayRegistrationAttempted {
			if err := r.updateTurnScoped(ctx, &turn, func(state *api.CellnScopedStatus) error { state.GatewayRegistrationAttempted = true; return nil }); err != nil {
				return ctrl.Result{}, err
			}
		}
		if err := r.ScopedDispatcher.RegisterGateway(ctx, final, decision, execution); err != nil {
			var refusal *cellnscoped.HTTPError
			if errors.As(err, &refusal) && refusal.Status == 429 && refusal.Reason == modelbudget.ReasonExhausted {
				if err := r.updateTurnStatus(ctx, &turn, func(current *api.AgentRunTurn) error {
					meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "CellnTurnComplete", Status: metav1.ConditionTrue, Reason: "BudgetExhausted", Message: "AUTH_BUDGET_EXHAUSTED: the original parent allowance refused this unstarted turn. Exact native and gateway registration fences are required before cleanup is confirmed.", ObservedGeneration: current.Generation})
					return nil
				}); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{Requeue: true}, nil
			}
			return r.turnUncertain(ctx, &turn, "GatewayRegistrationUnconfirmed", err)
		}
		if err := r.updateTurnScoped(ctx, &turn, func(state *api.CellnScopedStatus) error { state.GatewayRegistered = true; return nil }); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.updateTurnScoped(ctx, &turn, func(state *api.CellnScopedStatus) error { state.StartAttempted = true; return nil }); err != nil {
		return ctrl.Result{}, err
	}
	observed, err := r.ScopedDispatcher.Start(ctx, s.ReceiverID, s.Owner, execution, model)
	if err != nil {
		return r.turnUncertain(ctx, &turn, "ExecutionOutcomeUnconfirmed", err)
	}
	return r.applyTurnStatus(ctx, &turn, prepared, final, observed)
}

func (r *AgentRunTurnReconciler) scopedTurnParent(ctx context.Context, reader client.Reader, turn *api.AgentRunTurn) (*api.AgentRun, error) {
	var run api.AgentRun
	if err := reader.Get(ctx, client.ObjectKey{Namespace: turn.Namespace, Name: turn.Spec.RunName}, &run); err != nil {
		return nil, err
	}
	owner := metav1.GetControllerOf(turn)
	if owner == nil || owner.UID != run.UID || owner.Name != run.Name || turn.Spec.RunUID != string(run.UID) || run.Spec.ExecutionLifecycle != "enduring" || run.Status.Phase != api.AgentRunPhaseRunning || run.Status.CellnScoped == nil || !run.Status.CellnScoped.StartAttempted || run.Status.CellnScoped.ReceiverID == "" || run.Status.CellnScoped.Owner == "" || run.Status.CellnScoped.ParentIncarnation == "" || run.Status.CellnScoped.NativePhase != "Running" {
		return nil, errors.New("turn is not bound to the live original scoped parent")
	}
	return &run, nil
}

func (r *AgentRunTurnReconciler) loadOriginalScoped(ctx context.Context, run *api.AgentRun) (*cellnauthority.StoredPreparation, *cellnauthority.FinalizedPreparation, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	helper := &AgentRunReconciler{Client: r.Client, APIReader: reader, ScopedDispatcher: r.ScopedDispatcher}
	return helper.loadScopedBinding(ctx, run)
}

func (r *AgentRunTurnReconciler) loadTurnScoped(ctx context.Context, run *api.AgentRun, turn *api.AgentRunTurn, originalPrepared *cellnauthority.StoredPreparation, originalFinal *cellnauthority.FinalizedPreparation) (*cellnauthority.StoredPreparation, *cellnauthority.FinalizedPreparation, error) {
	s := turn.Status.CellnScoped
	if s == nil || s.ParentIncarnation != run.Status.CellnScoped.ParentIncarnation || s.TurnID != string(turn.UID) || (s.Owner != "" && s.Owner != run.Status.CellnScoped.Owner) {
		return nil, nil, errors.New("scoped turn identity changed")
	}
	prepared, err := r.ScopedDispatcher.Store.Load(ctx, s.PreparationName)
	if err != nil || string(prepared.UID) != s.PreparationUID {
		return nil, nil, errors.New("protected turn preparation is unavailable")
	}
	if prepared.Operation.Resolution.Execution.Source.RunUID != string(run.UID) || prepared.Operation.Resolution.Execution.TurnUID != string(turn.UID) || prepared.Operation.Resolution.Decision.Parent == nil || prepared.Operation.Resolution.Decision.Parent.TurnID == nil || *prepared.Operation.Resolution.Decision.Parent.TurnID != string(turn.UID) {
		return nil, nil, errors.New("protected turn preparation has another source")
	}
	final, err := r.ScopedDispatcher.Store.LoadFinal(ctx, s.DecisionName, prepared)
	if err != nil || string(final.UID) != s.DecisionUID {
		return nil, nil, errors.New("protected turn decision is unavailable")
	}
	if originalPrepared.Operation.Resolution.Execution.Source.RunUID != prepared.Operation.Resolution.Execution.Source.RunUID || final.Decision.Budget.BudgetID != originalFinal.Decision.Budget.BudgetID || final.Decision.Budget.RunCap != originalFinal.Decision.Budget.RunCap || final.Decision.Budget.MaxTurns != originalFinal.Decision.Budget.MaxTurns || final.Decision.Budget.ParentDeadlineUnix != originalFinal.Decision.Budget.ParentDeadlineUnix || !reflect.DeepEqual(final.Decision.Route.CredentialSource, originalFinal.Decision.Route.CredentialSource) {
		return nil, nil, errors.New("turn did not preserve original run allowance, deadline, or route Secret UID")
	}
	return prepared, final, nil
}

func (r *AgentRunTurnReconciler) applyTurnStatus(ctx context.Context, turn *api.AgentRunTurn, prepared *cellnauthority.StoredPreparation, final *cellnauthority.FinalizedPreparation, observed cellnscoped.OperationStatus) (ctrl.Result, error) {
	s := turn.Status.CellnScoped
	if s == nil || observed.ID != s.ReceiverID || observed.Owner != s.Owner {
		return ctrl.Result{}, errors.New("scoped turn receiver owner mismatch")
	}
	if err := validateScopedCorrelation(prepared, observed); err != nil {
		return ctrl.Result{}, err
	}
	if int64(len(observed.Output)) > prepared.Operation.Resolution.Execution.RuntimeLimits.OutputBytes || (observed.ReceiptDigest != "" && !scopedReceiptPattern.MatchString(observed.ReceiptDigest)) {
		return ctrl.Result{}, errors.New("scoped turn output violates prepared bounds")
	}
	active := []string{"Prepared", "Admitting", "Admitted", "Running", "Cancelling"}
	terminal := []string{"Succeeded", "Failed", "Refused", "Cancelled"}
	if !slices.Contains(active, observed.Phase) && !slices.Contains(terminal, observed.Phase) {
		return ctrl.Result{}, errors.New("scoped turn returned an unknown phase")
	}
	executionEvidence, err := boundedNativeEvidence(observed.Execution)
	if err != nil {
		return ctrl.Result{}, err
	}
	substrateEvidence, err := boundedNativeEvidence(observed.Substrate)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.updateTurnScoped(ctx, turn, func(state *api.CellnScopedStatus) error {
		state.NativePhase = observed.Phase
		if observed.ReceiptDigest != "" {
			if state.ReceiptDigest != "" && state.ReceiptDigest != observed.ReceiptDigest {
				return errors.New("scoped turn receipt changed")
			}
			state.ReceiptDigest = observed.ReceiptDigest
		}
		if observed.Output != "" {
			if state.Output != "" && state.Output != observed.Output {
				return errors.New("scoped turn correlated output changed")
			}
			state.Output = observed.Output
		}
		for current, next := range map[*string]string{&state.ParentID: observed.ParentID, &state.ChildID: observed.ChildID, &state.CellID: observed.CellID, &state.ExecutionProvenance: executionEvidence, &state.SubstrateProvenance: substrateEvidence} {
			if next != "" {
				if *current != "" && *current != next {
					return errors.New("scoped turn provenance changed")
				}
				*current = next
			}
		}
		return nil
	}); err != nil {
		return ctrl.Result{}, err
	}
	if slices.Contains(active, observed.Phase) {
		_ = r.recordTurnObservation(ctx, r.APIReader, turn, nil)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if observed.Phase == "Succeeded" && (observed.ReceiptDigest == "" || observed.Output == "") {
		return ctrl.Result{}, errors.New("successful scoped turn lacks correlated output or receipt")
	}
	if err := r.updateTurnStatus(ctx, turn, func(current *api.AgentRunTurn) error {
		state := current.Status.CellnScoped
		if state == nil {
			return errors.New("scoped turn status lost")
		}
		if len(current.UID) <= 64 && len(state.Output) >= 1 && len(state.Output) <= api.MaxConversationAnswerBytes && len(state.ChildID) == 71 && state.ChildID[:7] == "blake3:" && scopedReceiptPattern.MatchString(state.ChildID) {
			current.Status.Execution = &api.CellnParentTurnStatus{ID: string(current.UID), Message: current.Spec.Message, Child: state.ChildID, Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: observed.Phase == "Succeeded", Answer: state.Output}}
		}
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "CellnTurnComplete", Status: metav1.ConditionTrue, Reason: "Committed", Message: "The original scoped turn owner returned a terminal correlated result; child cleanup confirmation is pending.", ObservedGeneration: current.Generation})
		return nil
	}); err != nil {
		return ctrl.Result{}, err
	}
	return r.cleanupTurn(ctx, turn)
}

func (r *AgentRunTurnReconciler) cleanupTurn(ctx context.Context, turn *api.AgentRunTurn) (ctrl.Result, error) {
	if turn.Status.CellnScoped == nil {
		return r.removeTurnFinalizer(ctx, turn)
	}
	var parent api.AgentRun
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: turn.Namespace, Name: turn.Spec.RunName}, &parent); err != nil {
		return r.turnUncertain(ctx, turn, "OriginalParentUnavailable", err)
	}
	// Root cleanup is stronger evidence: the lifecycle-aware receiver only sets
	// this after stopping and joining the retained parent and every child owner.
	// Do not send a child cleanup request after that owner has been destroyed.
	if parent.Status.CellnScoped != nil && parent.Status.CellnScoped.CleanupConfirmed && parent.Status.CellnScoped.ParentIncarnation == turn.Status.CellnScoped.ParentIncarnation && parent.Status.CellnScoped.Owner == turn.Status.CellnScoped.Owner {
		if err := r.updateTurnScoped(ctx, turn, func(state *api.CellnScopedStatus) error { state.CleanupConfirmed = true; return nil }); err != nil {
			return ctrl.Result{}, err
		}
		return r.removeTurnFinalizer(ctx, turn)
	}
	originalPrepared, originalFinal, err := r.loadOriginalScoped(ctx, &parent)
	if err != nil {
		return r.turnUncertain(ctx, turn, "OriginalAuthorityUnavailable", err)
	}
	_, final, err := r.loadTurnScoped(ctx, &parent, turn, originalPrepared, originalFinal)
	if err != nil {
		return r.turnUncertain(ctx, turn, "TurnAuthorityUnavailable", err)
	}
	s := turn.Status.CellnScoped
	if s.ReceiverID == "" {
		prepared, _, err := r.loadTurnScoped(ctx, &parent, turn, originalPrepared, originalFinal)
		if err != nil {
			return r.turnUncertain(ctx, turn, "TurnAuthorityUnavailable", err)
		}
		enrolled, err := r.ScopedDispatcher.Enroll(ctx, prepared, final)
		if err != nil {
			return r.turnUncertain(ctx, turn, "ReceiverEnrollmentUnconfirmed", err)
		}
		if err := validateScopedOwner(enrolled.ID, enrolled.Owner); err != nil {
			return ctrl.Result{}, err
		}
		if enrolled.Owner != parent.Status.CellnScoped.Owner {
			return r.turnUncertain(ctx, turn, "OriginalOwnerUnavailable", errors.New("turn cleanup could not recover the original native parent owner"))
		}
		if err := r.updateTurnScoped(ctx, turn, func(state *api.CellnScopedStatus) error {
			state.ReceiverID, state.Owner = enrolled.ID, enrolled.Owner
			return nil
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if turn.Spec.CancelRequested && !turn.Status.CancelAttempted {
		if err := r.updateTurnStatus(ctx, turn, func(current *api.AgentRunTurn) error { current.Status.CancelAttempted = true; return nil }); err != nil {
			return ctrl.Result{}, err
		}
	}
	status, err := r.ScopedDispatcher.Cleanup(ctx, s.ReceiverID, final, s.GatewayRegistrationAttempted)
	if err != nil || status.ID != s.ReceiverID || status.Owner != s.Owner || !status.CleanupConfirmed {
		if err == nil {
			err = errors.New("scoped child cleanup is unconfirmed")
		}
		return r.turnUncertain(ctx, turn, "CleanupUnconfirmed", err)
	}
	if err := r.updateTurnScoped(ctx, turn, func(state *api.CellnScopedStatus) error {
		state.CleanupConfirmed = true
		state.NativePhase = status.Phase
		return nil
	}); err != nil {
		return ctrl.Result{}, err
	}
	return r.removeTurnFinalizer(ctx, turn)
}

func (r *AgentRunTurnReconciler) removeTurnFinalizer(ctx context.Context, turn *api.AgentRunTurn) (ctrl.Result, error) {
	var current api.AgentRunTurn
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(turn), &current); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !controllerutil.ContainsFinalizer(&current, agentRunTurnFinalizer) {
		return ctrl.Result{}, nil
	}
	patch := client.MergeFrom(current.DeepCopy())
	controllerutil.RemoveFinalizer(&current, agentRunTurnFinalizer)
	return ctrl.Result{}, r.Patch(ctx, &current, patch)
}

func (r *AgentRunTurnReconciler) updateTurnScoped(ctx context.Context, turn *api.AgentRunTurn, mutate func(*api.CellnScopedStatus) error) error {
	return r.updateTurnStatus(ctx, turn, func(current *api.AgentRunTurn) error {
		if current.Status.CellnScoped == nil {
			return errors.New("scoped turn recovery identity unavailable")
		}
		before := *current.Status.CellnScoped
		if err := mutate(current.Status.CellnScoped); err != nil {
			return err
		}
		after := current.Status.CellnScoped
		if before.PreparationName != after.PreparationName || before.PreparationUID != after.PreparationUID || before.DecisionName != after.DecisionName || before.DecisionUID != after.DecisionUID || before.ParentIncarnation != after.ParentIncarnation || before.TurnID != after.TurnID || (before.ReceiverID != "" && (before.ReceiverID != after.ReceiverID || before.Owner != after.Owner)) {
			return errors.New("scoped turn immutable identity changed")
		}
		return nil
	})
}

func (r *AgentRunTurnReconciler) updateTurnStatus(ctx context.Context, turn *api.AgentRunTurn, mutate func(*api.AgentRunTurn) error) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current api.AgentRunTurn
		reader := r.APIReader
		if reader == nil {
			reader = r.Client
		}
		if err := reader.Get(ctx, client.ObjectKeyFromObject(turn), &current); err != nil {
			return err
		}
		if current.UID != turn.UID {
			return errors.New("turn identity changed")
		}
		if err := mutate(&current); err != nil {
			return err
		}
		return r.Status().Update(ctx, &current)
	})
}

func (r *AgentRunTurnReconciler) turnUncertain(ctx context.Context, turn *api.AgentRunTurn, reason string, cause error) (ctrl.Result, error) {
	if err := r.recordTurnObservationReason(ctx, turn, reason); err != nil {
		return ctrl.Result{}, err
	}
	ctrl.LoggerFrom(ctx).Info("Scoped turn outcome requires reconciliation", "turn", client.ObjectKeyFromObject(turn), "reason", reason, "cause", cause.Error())
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *AgentRunTurnReconciler) recordTurnObservation(ctx context.Context, reader client.Reader, observed *api.AgentRunTurn, reconcileErr error) error {
	reason := "Pending"
	if reconcileErr != nil {
		reason = "ReconciliationRequired"
	}
	// An expired original lease is terminal for new turns, not a transient
	// failure: say so explicitly instead of reporting generic reconciliation.
	if errors.Is(reconcileErr, cellnparent.ErrParentLeaseExpired) {
		reason = "LeaseExpired"
	}
	return r.recordTurnObservationReason(ctx, observed, reason)
}

func (r *AgentRunTurnReconciler) recordTurnObservationReason(ctx context.Context, observed *api.AgentRunTurn, reason string) error {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var fresh api.AgentRunTurn
	if err := reader.Get(ctx, client.ObjectKeyFromObject(observed), &fresh); err != nil {
		return client.IgnoreNotFound(err)
	}
	if fresh.UID != observed.UID || fresh.Generation != observed.Generation || fresh.DeletionTimestamp != nil {
		return nil
	}
	before := fresh.DeepCopy()
	condition := metav1.Condition{Type: "CellnTurnComplete", Status: metav1.ConditionFalse, Reason: reason, Message: "Waiting for the original scoped turn owner; refusal or uncertainty cannot create replacement work.", ObservedGeneration: fresh.Generation}
	if reason == "LeaseExpired" {
		condition.Message = "The parent's original execution lease has elapsed; new turns are refused without spending budget. In-flight turns may still reconcile. Create a new run for further work."
	}
	if fresh.Status.CellnScoped != nil && slices.Contains([]string{"Succeeded", "Failed", "Refused", "Cancelled"}, fresh.Status.CellnScoped.NativePhase) {
		condition.Status, condition.Reason, condition.Message = metav1.ConditionTrue, "Committed", "The original scoped turn owner returned a terminal correlated result."
	}
	if fresh.Status.Execution != nil && fresh.Status.Execution.Result != nil {
		condition.Status, condition.Reason, condition.Message = metav1.ConditionTrue, "Committed", "Turn result recorded. Completion does not imply success; inspect the result."
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
