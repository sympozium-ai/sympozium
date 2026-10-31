package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/sympozium-ai/sympozium/internal/cellnscoped"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// scopedUncertainExpired reports a run whose owner has reported its context
// unavailable for longer than scopedUncertainGrace.
func scopedUncertainExpired(run *api.AgentRun, now time.Time) bool {
	c := meta.FindStatusCondition(run.Status.Conditions, "CellnScopedExecution")
	return c != nil && c.Status == metav1.ConditionUnknown && c.Reason == "NativeOwnerUncertain" && now.Sub(c.LastTransitionTime.Time) > scopedUncertainGrace
}

// boundedTurnFailure keeps the first line of a failed turn's text, cut to a
// size that fits a status field.
func boundedTurnFailure(text string) string {
	const limit = 512
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	// The owner's cause comes first, then everything the worker printed. Keep
	// the cause and the harness's own error line, not the event stream.
	cause, printed, _ := strings.Cut(text, " CELLN_HARNESS_EVENT ")
	cause = strings.TrimSuffix(strings.TrimSpace(cause), ":")
	if i := strings.LastIndex(printed, "CELLN_HARNESS_ERROR "); i >= 0 {
		cause += ": " + strings.TrimPrefix(printed[i:], "CELLN_HARNESS_ERROR ")
	}
	if len(cause) > limit {
		cause = cause[:limit] + "…"
	}
	return cause
}

// scopedUncertainGrace is how long an owner may report its context
// unavailable before the run ends.
var scopedUncertainGrace = 2 * time.Minute

// scopedLeaseElapsed reports an enduring conversation that answered and whose
// parent lease (the decision's parent deadline, from spec.enduring.leaseSeconds)
// has passed: its parent ending on the node is the conversation reaching its
// end, not a failure. A conversation that never answered, a one-shot, or a
// parent lost before its deadline is not a lease end.
func scopedLeaseElapsed(run *api.AgentRun, parentDeadlineUnix int64, now time.Time) bool {
	return run.Spec.ExecutionLifecycle == "enduring" && run.Status.Result != "" && parentDeadlineUnix > 0 && !now.Before(time.Unix(parentDeadlineUnix, 0))
}

// completeLeasedRun ends an enduring conversation whose lease elapsed as
// succeeded: its answers stand, its parent is gone and its allowance is fenced
// by cleanup. The condition says why so it is not read as a lost node.
func (r *AgentRunReconciler) completeLeasedRun(ctx context.Context, run *api.AgentRun) error {
	now := metav1.Now()
	if err := r.updateStatusWithRetry(ctx, run, func(current *api.AgentRun) {
		current.Status.Phase = api.AgentRunPhaseSucceeded
		current.Status.CompletedAt = &now
		current.Status.Error = ""
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "CellnScopedExecution", Status: metav1.ConditionTrue, Reason: "LeaseElapsed", Message: "The conversation reached the end of its lease (spec.enduring.leaseSeconds); its parent ended on its node and its answers stand. Start a new run to continue.", ObservedGeneration: current.Generation})
	}); err != nil {
		return err
	}
	r.Log.Info("Enduring Celln conversation ended at the end of its lease", "run", run.Name, "namespace", run.Namespace)
	return nil
}

const scopedOutcomeUnconfirmed = "Scoped execution outcome is uncertain. The controller will only read or clean up the original prepared owner; it will not create replacement authority. Ask an administrator to inspect the configured receiver and gateway."

var scopedReceiptPattern = regexp.MustCompile(`^(?:sha256:|blake3:)?[0-9a-f]{64}$`)

func scopedCatalogueSelected(run *api.AgentRun) bool {
	return run.Spec.CellnSelection != nil && (run.Spec.ExecutionLifecycle == "" || run.Spec.ExecutionLifecycle == "one-shot" || run.Spec.ExecutionLifecycle == "enduring")
}

// Refusals for runs that name an execution plane this controller no longer
// has. Runs that would have taken those planes fail with these messages
// instead of reaching another backend.
const (
	// cellnCatalogueRemoved refuses the namespaced catalogue path (operator-reviewed
	// CellnTool catalogue, issuer and host profiles) removed in 0.12.
	cellnCatalogueRemoved = "Namespaced Celln catalogue selection (cellnSelection.toolRefs issued from namespace grants) is no longer supported on this controller. Select platform tools with cellnSelection.clusterToolRefs or a platform runtime profile so the run executes through the Celln platform and model gateway."
	// cellnRouterPlaneRemoved refuses a backend=celln run with no catalogue
	// selection: the one-shot router plane (explicit spec.celln artifacts or a
	// forged task posted to the router) was removed in 0.12.
	cellnRouterPlaneRemoved = "Celln runs execute through the Celln platform: backend celln requires a cellnSelection (clusterToolRefs or a platform runtime profile) and a string task. Direct router dispatch of a task or explicit artifacts was removed in 0.12; create a new run with a selection."
	// cellnNativeParentRemoved ends a run bound to a native parent
	// (status.cellnParent) by the parent plane removed in 0.12. Its owner can no
	// longer be observed or stopped from here; nothing is replayed.
	cellnNativeParentRemoved = "This run was bound to a Celln native parent by the parent plane removed in 0.12; this controller cannot observe or resume it. Create a new run; it executes through the Celln platform and model gateway."
)

// sharedCatalogueSelected reports whether a run belongs to the scoped platform
// path, using only API reads. It intentionally runs before model normalization
// and prerequisite creation. A persisted scoped binding always wins recovery,
// and a run bound to a native parent by the removed plane never moves to the
// scoped receiver.
//
// Every enduring catalogue selection is scoped. A one-shot selection is scoped
// when it names cluster tools or a platform wrapper runtime (an AgentRuntime
// with cellnProfileRef and no inline celln profile). Anything else a selection
// could have meant was namespaced catalogue issuance, which no longer exists;
// reconcilePending refuses it.
func (r *AgentRunReconciler) sharedCatalogueSelected(ctx context.Context, run *api.AgentRun) (bool, error) {
	if run.Status.CellnScoped != nil {
		return true, nil
	}
	if run.Status.CellnParent != nil {
		return false, nil
	}
	selection := run.Spec.CellnSelection
	if selection == nil {
		return false, nil
	}
	if run.Spec.ExecutionLifecycle == "enduring" {
		// The scoped receiver owns every enduring catalogue selection; unsupported
		// wrappers are refused by its resolver rather than falling through, and
		// without a configured receiver the run is held (ScopedDispatchDisabled).
		return true, nil
	}
	if len(selection.ClusterToolRefs) != 0 {
		return true, nil
	}
	if len(selection.ToolRefs) != 0 {
		return false, nil
	}
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	runtimeName := selection.RuntimeRef
	if runtimeName == "" {
		var agent api.Agent
		if err := reader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.AgentRef}, &agent); err != nil {
			if apierrors.IsNotFound(err) {
				// A tool-free selection without an agent has no wrapper to
				// inspect; reconcilePending refuses it.
				return false, nil
			}
			return false, err
		}
		runtimeName = agent.Spec.RuntimeRef
	}
	if runtimeName == "" {
		return false, nil
	}
	var runtime api.AgentRuntime
	if err := reader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: runtimeName}, &runtime); err != nil {
		return false, err
	}
	return runtime.Spec.CellnProfileRef != nil && runtime.Spec.Celln == nil, nil
}

// scopedActiveObservation is how often the controller reads a scoped run's
// owner record while its answer is outstanding; scopedIdleObservation once an
// enduring parent has answered and waits for turns.
const (
	scopedActiveObservation = time.Second
	scopedIdleObservation   = 5 * time.Second
)

func (r *AgentRunReconciler) scopedProgress(ctx context.Context, run *api.AgentRun, status metav1.ConditionStatus, reason, message string) error {
	return r.updateStatusWithRetry(ctx, run, func(current *api.AgentRun) {
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "CellnScopedExecution", Status: status, Reason: reason, Message: message, ObservedGeneration: current.Generation})
	})
}

func (r *AgentRunReconciler) reconcilePendingScoped(ctx context.Context, log logr.Logger, run *api.AgentRun) (ctrl.Result, error) {
	if run.Spec.Backend != "celln" || !run.Spec.Task.IsString() || !scopedCatalogueSelected(run) {
		return ctrl.Result{}, r.failRun(ctx, run, "Scoped catalogue execution requires backend celln, a supported lifecycle and a string task")
	}
	if r.ScopedDispatcher == nil {
		if err := r.scopedProgress(ctx, run, metav1.ConditionFalse, "ScopedDispatchDisabled", "Operator scoped receiver configuration is absent; no legacy, model, OCI, or native execution was submitted"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	// The run's own key must be its Agent's, claimed before the gateway is
	// asked to serve it: the gateway refuses a key its Agent does not own.
	if run.Status.CellnScoped == nil {
		var agent api.Agent
		if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.AgentRef}, &agent); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, r.failRun(ctx, run, fmt.Sprintf("Agent %q not found", run.Spec.AgentRef))
			}
			return ctrl.Result{}, err
		}
		if refusal, err := r.authorizeRunKeys(ctx, run, &agent); err != nil || refusal != nil {
			if refusal != nil {
				return ctrl.Result{}, r.refuseRun(ctx, run, refusal)
			}
			return ctrl.Result{}, err
		}
	}
	// Admission timing at debug level: how long each receiver and gateway step
	// took, and how long after creation the first step ran.
	step := func(name string, began time.Time) {
		log.V(1).Info("Scoped Celln admission step", "step", name, "ms", time.Since(began).Milliseconds(), "sinceCreation", time.Since(run.CreationTimestamp.Time).Round(time.Millisecond).String())
	}
	if run.Status.CellnScoped == nil {
		step("first reconcile", time.Now())
	}
	began := time.Now()
	prepared, err := r.ScopedDispatcher.Prepare(ctx, client.ObjectKeyFromObject(run))
	step("prepare", began)
	if err != nil {
		if reason := cellnauthority.PlatformReason(err); reason != "" && run.Status.CellnScoped == nil {
			// No external enrollment/start can precede the persisted scoped
			// binding. A definitive policy refusal here is not lost VM context.
			message := reason + ": scoped authority refused before receiver enrollment; no native execution started"
			if err := r.scopedProgress(ctx, run, metav1.ConditionFalse, "AdmissionRefused", message); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.failRun(ctx, run, message)
		}
		return r.scopedUncertain(ctx, run, "PreparationUnconfirmed", err)
	}
	began = time.Now()
	final, err := r.ScopedDispatcher.EnsureFinal(ctx, prepared)
	step("final decision", began)
	if err != nil {
		return r.scopedUncertain(ctx, run, "FinalDecisionUnconfirmed", err)
	}
	began = time.Now()
	if err := r.persistScopedBinding(ctx, run, prepared, final); err != nil {
		return ctrl.Result{}, err
	}
	step("persist binding", began)
	if run.Status.CellnScoped == nil {
		return ctrl.Result{Requeue: true}, nil
	}
	// Reload after the status write so every following side effect is guarded by
	// the API-persisted immutable references, not this reconcile's local values.
	var current api.AgentRun
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
		return ctrl.Result{}, err
	}
	*run = current
	prepared, final, err = r.loadScopedBinding(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Authority is revalidated once, on the pass that starts execution: the
	// receiver's prepare on the enrolment pass executes nothing, and a change
	// between prepare and start is caught at start, which is the only side
	// effect that matters. Revalidating on every pass cost two seconds each.
	if !run.Status.CellnScoped.StartAttempted && run.Status.CellnScoped.ReceiverID != "" {
		began = time.Now()
		err := r.ScopedDispatcher.PreflightArtifacts(ctx, prepared)
		step("preflight artifacts", began)
		if err == nil {
			began = time.Now()
			err = r.ScopedDispatcher.RevalidateAuthority(ctx, prepared)
			step("revalidate authority", began)
		}
		if err != nil {
			return ctrl.Result{}, r.failRun(ctx, run, "Scoped authority changed before admission; no replacement execution was submitted")
		}
	}

	if run.Status.CellnScoped.ReceiverID == "" {
		began = time.Now()
		enrolled, err := r.ScopedDispatcher.Enroll(ctx, prepared, final)
		step("enroll", began)
		if err != nil {
			return r.scopedUncertain(ctx, run, "ReceiverEnrollmentUnconfirmed", err)
		}
		if err := validateScopedOwner(enrolled.ID, enrolled.Owner); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.updateScopedStatus(ctx, run, func(s *api.CellnScopedStatus) error {
			if s.ReceiverID != "" && (s.ReceiverID != enrolled.ID || s.Owner != enrolled.Owner) {
				return errors.New("scoped receiver enrollment identity changed")
			}
			s.ReceiverID, s.Owner = enrolled.ID, enrolled.Owner
			return nil
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if run.Status.CellnScoped.StartAttempted && (run.Status.CellnScoped.GatewayRegistered || final.Decision.Route.Provider == "none") {
		observed, readErr := r.ScopedDispatcher.Read(ctx, run.Status.CellnScoped.ReceiverID, final)
		if readErr == nil {
			return r.observeScoped(ctx, log, run, prepared, final, observed)
		}
		// Absence or loss of the receiver-owned admission journal is not proof
		// that the persisted attempt never arrived. Never relaunch from a JTI or
		// from a missing record; retain the finalizer and expose uncertainty.
		return r.scopedUncertain(ctx, run, "ExecutionOutcomeUnconfirmed", readErr)
	}
	decision, execution, model, err := r.ScopedDispatcher.StartTokens(final)
	if err != nil {
		return r.scopedUncertain(ctx, run, "OriginalAdmissionWindowUnavailable", err)
	}
	if decision.Route.Provider != "none" && !run.Status.CellnScoped.GatewayRegistered {
		if !run.Status.CellnScoped.GatewayRegistrationAttempted {
			if err := r.updateScopedStatus(ctx, run, func(s *api.CellnScopedStatus) error { s.GatewayRegistrationAttempted = true; return nil }); err != nil {
				return ctrl.Result{}, err
			}
			run.Status.CellnScoped.GatewayRegistrationAttempted = true
		}
		began = time.Now()
		if err := r.ScopedDispatcher.RegisterGateway(ctx, final, decision, execution); err != nil {
			return r.scopedUncertain(ctx, run, "GatewayRegistrationUnconfirmed", err)
		}
		step("register gateway", began)
		if err := r.updateScopedStatus(ctx, run, func(s *api.CellnScopedStatus) error { s.GatewayRegistered = true; return nil }); err != nil {
			return ctrl.Result{}, err
		}
		run.Status.CellnScoped.GatewayRegistered = true
	}
	if !run.Status.CellnScoped.StartAttempted {
		if err := r.updateScopedStatus(ctx, run, func(s *api.CellnScopedStatus) error { s.StartAttempted = true; return nil }); err != nil {
			return ctrl.Result{}, err
		}
		run.Status.CellnScoped.StartAttempted = true
	}
	began = time.Now()
	observed, err := r.ScopedDispatcher.Start(ctx, run.Status.CellnScoped.ReceiverID, run.Status.CellnScoped.Owner, execution, model)
	step("start", began)
	if err != nil {
		return r.scopedUncertain(ctx, run, "ExecutionOutcomeUnconfirmed", err)
	}
	return r.applyScopedStatus(ctx, log, run, prepared, observed)
}

func (r *AgentRunReconciler) reconcileRunningScoped(ctx context.Context, log logr.Logger, run *api.AgentRun) (ctrl.Result, error) {
	prepared, final, err := r.loadScopedBinding(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	observed, err := r.ScopedDispatcher.Read(ctx, run.Status.CellnScoped.ReceiverID, final)
	if err != nil {
		return r.scopedUncertain(ctx, run, "ExecutionOutcomeUnconfirmed", err)
	}
	return r.observeScoped(ctx, log, run, prepared, final, observed)
}

// observeScoped applies an owner record. A single-turn parent (a one-shot the
// resolver issued as a parent with one turn) that has answered is stopped
// here, cells returned and model allowance fenced, and its record applied as
// a success; everything else is applied as observed.
func (r *AgentRunReconciler) observeScoped(ctx context.Context, log logr.Logger, run *api.AgentRun, prepared *cellnauthority.StoredPreparation, final *cellnauthority.FinalizedPreparation, observed cellnscoped.OperationStatus) (ctrl.Result, error) {
	singleTurn := run.Spec.ExecutionLifecycle != "enduring" && final.Decision.Parent != nil
	if singleTurn && observed.Phase == "Cancelled" && run.Status.CellnScoped != nil && run.Status.CellnScoped.CleanupConfirmed && run.Status.CellnScoped.Output != "" && run.Status.CellnScoped.ReceiptDigest != "" {
		// The owner now reports the parent this controller stopped after its
		// answer: that is the success already recorded, read again, not a
		// cancellation of the run.
		observed.Phase = "Succeeded"
		if observed.Output == "" {
			observed.Output = run.Status.CellnScoped.Output
		}
	}
	if singleTurn && observed.Phase == "Running" && observed.Output != "" && observed.ReceiptDigest != "" && run.Status.CellnScoped != nil && !run.Status.CellnScoped.CleanupConfirmed {
		status, err := r.ScopedDispatcher.Cleanup(ctx, run.Status.CellnScoped.ReceiverID, final, run.Status.CellnScoped.GatewayRegistrationAttempted)
		if err != nil {
			return r.scopedUncertain(ctx, run, "SingleTurnParentCleanupUnconfirmed", err)
		}
		if status.ID != run.Status.CellnScoped.ReceiverID || !status.CleanupConfirmed {
			return r.scopedUncertain(ctx, run, "SingleTurnParentCleanupUnconfirmed", errors.New("scoped receiver has not confirmed the single-turn parent's cleanup"))
		}
		if err := r.updateScopedStatus(ctx, run, func(s *api.CellnScopedStatus) error {
			s.CleanupConfirmed = true
			return nil
		}); err != nil {
			return ctrl.Result{}, err
		}
		observed.Phase = "Succeeded"
		log.Info("Single-turn Celln parent answered and was stopped", "run", run.Name)
	}
	return r.applyScopedStatus(ctx, log, run, prepared, observed)
}

func (r *AgentRunReconciler) scopedUncertain(ctx context.Context, run *api.AgentRun, reason string, cause error) (ctrl.Result, error) {
	if cellnscoped.IsContextLost(cause) {
		// The node that held this run's native state is gone. Nothing is
		// re-placed elsewhere; the run ends and cleanup fences its allowance.
		r.Log.Info("Scoped Celln run ended: its receiver reported the context lost", "run", run.Name, "namespace", run.Namespace, "step", reason, "cause", cause.Error())
		// A separate condition type, because cleanup later rewrites
		// CellnScopedExecution; written in the same update as Failed, and
		// read by the seam as ContextLost.
		return ctrl.Result{}, r.failRun(ctx, run, "Celln node holding this run is gone (AUTH_CONTEXT_LOST); no replacement execution is permitted. Start a new run.",
			metav1.Condition{Type: conditionCellnContextLost, Status: metav1.ConditionTrue, Reason: "AUTH_CONTEXT_LOST", Message: "The receiver reported the node holding this run's native context gone"})
	}
	if cellnscoped.IsReceiverMisconfigured(cause) {
		if err := r.scopedProgress(ctx, run, metav1.ConditionFalse, "ReceiverUnavailable", receiverMisconfiguredMessage(cause)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if cellnscoped.IsUnsupported(cause) {
		if err := r.scopedProgress(ctx, run, metav1.ConditionFalse, "Unsupported", "AUTH_PROTOCOL_UNSUPPORTED: the native backend does not advertise the required scoped artifact contract; no fallback was submitted"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if statusErr := r.scopedProgress(ctx, run, metav1.ConditionUnknown, reason, scopedOutcomeUnconfirmed); statusErr != nil {
		return ctrl.Result{}, statusErr
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, cause
}

func (r *AgentRunReconciler) persistScopedBinding(ctx context.Context, run *api.AgentRun, prepared *cellnauthority.StoredPreparation, final *cellnauthority.FinalizedPreparation) error {
	want := api.CellnScopedStatus{PreparationName: prepared.Name, PreparationUID: string(prepared.UID), DecisionName: final.Name, DecisionUID: string(final.UID)}
	if final.Decision.Parent != nil {
		want.ParentIncarnation = final.Decision.Parent.Incarnation
	}
	return r.updateStatusWithRetry(ctx, run, func(current *api.AgentRun) {
		if current.Status.CellnScoped == nil {
			current.Status.CellnScoped = &want
		}
	})
}

func (r *AgentRunReconciler) updateScopedStatus(ctx context.Context, run *api.AgentRun, mutate func(*api.CellnScopedStatus) error) error {
	var mutationErr error
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current api.AgentRun
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(run), &current); err != nil {
			return err
		}
		if current.UID != run.UID || current.Status.CellnScoped == nil {
			return errors.New("scoped run identity is unavailable")
		}
		before := *current.Status.CellnScoped
		if err := mutate(current.Status.CellnScoped); err != nil {
			mutationErr = err
			return nil
		}
		if before.PreparationName != current.Status.CellnScoped.PreparationName || before.PreparationUID != current.Status.CellnScoped.PreparationUID || before.DecisionName != current.Status.CellnScoped.DecisionName || before.DecisionUID != current.Status.CellnScoped.DecisionUID || before.ParentIncarnation != current.Status.CellnScoped.ParentIncarnation || before.TurnID != current.Status.CellnScoped.TurnID || (before.ReceiverID != "" && (before.ReceiverID != current.Status.CellnScoped.ReceiverID || before.Owner != current.Status.CellnScoped.Owner)) {
			return errors.New("scoped immutable status identity changed")
		}
		return r.Status().Update(ctx, &current)
	})
	if mutationErr != nil {
		return mutationErr
	}
	return err
}

func (r *AgentRunReconciler) loadScopedBinding(ctx context.Context, run *api.AgentRun) (*cellnauthority.StoredPreparation, *cellnauthority.FinalizedPreparation, error) {
	if r.ScopedDispatcher == nil || r.APIReader == nil || run.Status.CellnScoped == nil {
		return nil, nil, errors.New("scoped dispatcher recovery is unavailable")
	}
	s := run.Status.CellnScoped
	prepared, err := r.ScopedDispatcher.Store.Load(ctx, s.PreparationName)
	if err != nil {
		return nil, nil, err
	}
	if prepared.Operation.Resolution.Execution.Source.ClusterID != r.ScopedDispatcher.ClusterID {
		return nil, nil, errors.New("scoped controller cluster identity changed")
	}
	if err := r.ScopedDispatcher.Store.ValidateCurrent(ctx, client.ObjectKeyFromObject(run), prepared); err != nil {
		return nil, nil, err
	}
	if string(prepared.UID) != s.PreparationUID {
		return nil, nil, errors.New("protected scoped preparation UID changed")
	}
	final, err := r.ScopedDispatcher.Store.LoadFinal(ctx, s.DecisionName, prepared)
	if err != nil {
		return nil, nil, err
	}
	if string(final.UID) != s.DecisionUID {
		return nil, nil, errors.New("protected scoped decision UID changed")
	}
	if final.Decision.Parent == nil {
		if s.ParentIncarnation != "" || s.TurnID != "" {
			return nil, nil, errors.New("one-shot scoped status carries parent identity")
		}
	} else if s.ParentIncarnation != final.Decision.Parent.Incarnation || (final.Decision.Parent.TurnID != nil && s.TurnID != *final.Decision.Parent.TurnID) {
		return nil, nil, errors.New("scoped parent identity changed")
	}
	return prepared, final, nil
}

func validateScopedOwner(id, owner string) error {
	if id == "" || owner == "" || len(id) > 256 || len(owner) > 256 {
		return errors.New("scoped receiver returned an invalid owner identity")
	}
	return nil
}

func (r *AgentRunReconciler) applyScopedStatus(ctx context.Context, log logr.Logger, run *api.AgentRun, prepared *cellnauthority.StoredPreparation, observed cellnscoped.OperationStatus) (ctrl.Result, error) {
	s := run.Status.CellnScoped
	if s == nil || observed.ID != s.ReceiverID || observed.Owner != s.Owner || validateScopedOwner(observed.ID, observed.Owner) != nil {
		return ctrl.Result{}, errors.New("scoped receiver result owner mismatch")
	}
	if int64(len(observed.Output)) > prepared.Operation.Resolution.Execution.RuntimeLimits.OutputBytes || len(observed.Reason) > 4096 || (observed.ReceiptDigest != "" && !scopedReceiptPattern.MatchString(observed.ReceiptDigest)) {
		return ctrl.Result{}, errors.New("scoped receiver result violates the prepared output contract")
	}
	if err := validateScopedCorrelation(prepared, observed); err != nil {
		return ctrl.Result{}, err
	}
	executionProvenance, err := boundedNativeEvidence(observed.Execution)
	if err != nil {
		return ctrl.Result{}, err
	}
	substrateProvenance, err := boundedNativeEvidence(observed.Substrate)
	if err != nil {
		return ctrl.Result{}, err
	}
	active := []string{"Prepared", "Admitting", "Admitted", "Running", "Cancelling", "Uncertain"}
	terminal := []string{"Succeeded", "Failed", "Refused", "Cancelled"}
	if !slices.Contains(active, observed.Phase) && !slices.Contains(terminal, observed.Phase) {
		return ctrl.Result{}, errors.New("scoped receiver returned an unknown phase")
	}
	now := metav1.Now()
	if err := r.updateScopedStatus(ctx, run, func(state *api.CellnScopedStatus) error {
		state.NativePhase = observed.Phase
		if observed.ReceiptDigest != "" {
			if state.ReceiptDigest != "" && state.ReceiptDigest != observed.ReceiptDigest {
				return errors.New("scoped receipt digest changed")
			}
			state.ReceiptDigest = observed.ReceiptDigest
		}
		if observed.Output != "" {
			if state.Output != "" && state.Output != observed.Output {
				return errors.New("scoped correlated output changed")
			}
			state.Output = observed.Output
		}
		for current, next := range map[*string]string{&state.ParentID: observed.ParentID, &state.ChildID: observed.ChildID, &state.CellID: observed.CellID, &state.ExecutionProvenance: executionProvenance, &state.SubstrateProvenance: substrateProvenance} {
			if next != "" {
				if *current != "" && *current != next {
					return errors.New("scoped native provenance changed")
				}
				*current = next
			}
		}
		return nil
	}); err != nil {
		return ctrl.Result{}, err
	}
	if observed.Phase == "Uncertain" {
		// The owner keeps reporting its context unavailable: the parent ended
		// on its node (its budget ran out, or it exited). Past a grace period
		// for blips, end the run so it stops looking alive. Nothing is
		// re-created elsewhere, and cleanup still stops the original owner.
		if scopedUncertainExpired(run, time.Now()) {
			if scopedLeaseElapsed(run, prepared.Operation.Resolution.Decision.Budget.ParentDeadlineUnix, time.Now()) {
				return ctrl.Result{}, r.completeLeasedRun(ctx, run)
			}
			return ctrl.Result{}, r.failRun(ctx, run, scopedEndedSummary(observed.Reason))
		}
		return r.scopedUncertain(ctx, run, "NativeOwnerUncertain", errors.New("original native owner context is unavailable; no replacement execution is permitted"))
	}
	if slices.Contains(active, observed.Phase) {
		answered := false
		if err := r.updateStatusWithRetry(ctx, run, func(current *api.AgentRun) {
			current.Status.Phase = api.AgentRunPhaseRunning
			if current.Status.StartedAt == nil {
				current.Status.StartedAt = &now
			}
			reason := "OwnerRecordObserved"
			message := "The configured receiver returned the original prepared owner record"
			if current.Spec.ExecutionLifecycle == "enduring" && observed.Phase == "Running" && current.Status.CellnScoped != nil && current.Status.CellnScoped.ReceiptDigest != "" && current.Status.CellnScoped.Output != "" {
				reason = "EnduringParentReady"
				message = "The original native parent completed its initial turn and remains running"
				current.Status.Result = current.Status.CellnScoped.Output
				answered = true
			}
			meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "CellnScopedExecution", Status: metav1.ConditionTrue, Reason: reason, Message: message, ObservedGeneration: current.Generation})
		}); err != nil {
			return ctrl.Result{}, err
		}
		// Someone is waiting on the first answer: look again every second
		// until it lands. An enduring parent that has answered is idle between
		// turns (turns are observed by their own controller), so it is read
		// less often.
		if answered {
			return ctrl.Result{RequeueAfter: scopedIdleObservation}, nil
		}
		return ctrl.Result{RequeueAfter: scopedActiveObservation}, nil
	}
	if observed.Phase == "Succeeded" && observed.ReceiptDigest == "" {
		return ctrl.Result{}, errors.New("scoped success lacks a receipt digest")
	}
	if err := r.updateStatusWithRetry(ctx, run, func(current *api.AgentRun) {
		if current.Status.Phase == api.AgentRunPhaseSucceeded || current.Status.Phase == api.AgentRunPhaseFailed {
			// A reconcile that read a stale cache after another ended this run
			// must not re-end it: the owner's later record (a stopped parent
			// reads as Cancelled) never overturns a recorded outcome.
			return
		}
		current.Status.CompletedAt = &now
		if observed.Phase == "Succeeded" {
			current.Status.Phase, current.Status.Result, current.Status.Error = api.AgentRunPhaseSucceeded, observed.Output, ""
		} else {
			// Never expose raw native diagnostics. Only mapped public refusal
			// codes are safe for tenant-visible, actionable explanations. A
			// failed turn's own text is the tenant's: the owner committed it as
			// the turn's answer, so it says why (a tool budget, a refused call,
			// a model error) where the native reason only says "Failed".
			current.Status.Phase, current.Status.Error = api.AgentRunPhaseFailed, scopedFailureSummary(observed.Phase, observed.Reason)
			if strings.HasPrefix(observed.Output, "Turn failed") || strings.HasPrefix(observed.Output, "Turn refused") {
				current.Status.Error += ": " + boundedTurnFailure(observed.Output)
			}
		}
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: "CellnScopedExecution", Status: metav1.ConditionTrue, Reason: "TerminalOwnerRecordObserved", Message: "The original prepared owner returned a terminal record; cleanup confirmation is pending", ObservedGeneration: current.Generation})
	}); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("Scoped Celln execution reached terminal owner record", "phase", observed.Phase)
	return ctrl.Result{Requeue: true}, nil
}

func validateScopedCorrelation(prepared *cellnauthority.StoredPreparation, observed cellnscoped.OperationStatus) error {
	d := prepared.Operation.Resolution.Decision
	if d.Parent == nil {
		if observed.ParentIncarnation != "" || observed.TurnID != "" {
			return errors.New("one-shot receiver result carries parent correlation")
		}
		return nil
	}
	if observed.ParentIncarnation != d.Parent.Incarnation {
		return errors.New("scoped receiver returned another parent incarnation")
	}
	if d.Parent.TurnID != nil && observed.TurnID != *d.Parent.TurnID {
		return errors.New("scoped receiver returned another turn identity")
	}
	return nil
}

func boundedNativeEvidence(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if len(raw) > 65536 || !json.Valid(raw) {
		return "", errors.New("scoped native provenance is invalid or exceeds its bound")
	}
	var compact []byte
	buffer := &bytes.Buffer{}
	if err := json.Compact(buffer, raw); err != nil {
		return "", err
	}
	compact = buffer.Bytes()
	return string(compact), nil
}

func (r *AgentRunReconciler) cleanupScoped(ctx context.Context, run *api.AgentRun) (bool, error) {
	if r.ScopedDispatcher == nil || run.Status.CellnScoped == nil {
		return false, errors.New("scoped cleanup configuration is unavailable")
	}
	if run.Status.CellnScoped.CleanupConfirmed {
		return r.scopedChildrenFinalized(ctx, run)
	}
	prepared, final, err := r.loadScopedBinding(ctx, run)
	if err != nil {
		return false, err
	}
	if run.Status.CellnScoped.ReceiverID == "" {
		enrolled, err := r.ScopedDispatcher.Enroll(ctx, prepared, final)
		if err != nil {
			return false, err
		}
		if err := validateScopedOwner(enrolled.ID, enrolled.Owner); err != nil {
			return false, err
		}
		if err := r.updateScopedStatus(ctx, run, func(s *api.CellnScopedStatus) error { s.ReceiverID, s.Owner = enrolled.ID, enrolled.Owner; return nil }); err != nil {
			return false, err
		}
		return false, nil
	}
	status, err := r.ScopedDispatcher.Cleanup(ctx, run.Status.CellnScoped.ReceiverID, final, run.Status.CellnScoped.GatewayRegistrationAttempted)
	if err != nil {
		return false, err
	}
	if status.ID != run.Status.CellnScoped.ReceiverID || (!status.ContextLost && status.Owner != run.Status.CellnScoped.Owner) || !status.CleanupConfirmed {
		return false, errors.New("scoped receiver has not confirmed cleanup for the prepared owner")
	}
	if err := r.updateScopedStatus(ctx, run, func(s *api.CellnScopedStatus) error {
		s.CleanupConfirmed = true
		s.NativePhase = status.Phase
		return nil
	}); err != nil {
		return false, err
	}
	if err := r.scopedProgress(ctx, run, metav1.ConditionTrue, "CleanupConfirmed", "The original native owner confirmed cleanup and any model allowance was fenced; no replacement authority was created"); err != nil {
		return false, err
	}
	return r.scopedChildrenFinalized(ctx, run)
}

// Keep the stopped root's protected recovery binding available until existing
// scoped child finalizers have consumed its parent-and-descendants cleanup proof.
func (r *AgentRunReconciler) scopedChildrenFinalized(ctx context.Context, run *api.AgentRun) (bool, error) {
	if run.Spec.ExecutionLifecycle != "enduring" {
		return true, nil
	}
	var children api.AgentRunTurnList
	if err := r.APIReader.List(ctx, &children, client.InNamespace(run.Namespace)); err != nil {
		return false, err
	}
	for _, child := range children.Items {
		if child.Spec.RunUID == string(run.UID) && child.Spec.RunName == run.Name && slices.Contains(child.Finalizers, agentRunTurnFinalizer) {
			return false, nil
		}
	}
	return true, nil
}

// receiverMisconfiguredMessage tells an operator what to fix when the scoped
// receiver refused or never answered the controller's capability preflight.
func receiverMisconfiguredMessage(cause error) string {
	var e *cellnscoped.HTTPError
	if errors.As(cause, &e) && e.Reason == cellnscoped.ReasonReceiverAuthRejected {
		return "RECEIVER_AUTH_REJECTED: the Celln scoped receiver rejected the controller's token. If mediation trust was rotated, restart the controller-manager and model gateway; nothing was submitted"
	}
	return "RECEIVER_UNREACHABLE: the Celln scoped receiver did not answer over trusted TLS. Check the celln-router and, if mediation trust was rotated, restart the controller-manager and model gateway; nothing was submitted"
}
