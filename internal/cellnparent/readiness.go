package cellnparent

import (
	"fmt"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ParentReadyCondition is the run condition the controller writes from the
// owner's own status report. It is the only recorded evidence that the parent
// is live and initialized.
const ParentReadyCondition = "CellnParentReady"

// Reasons a native parent refuses another turn. They are stable: the API
// reports them to callers and the console explains them.
const (
	TurnRefusedDeleting         = "Deleting"
	TurnRefusedNotEnduring      = "NotEnduring"
	TurnRefusedNotAdmitted      = "ParentNotAdmitted"
	TurnRefusedParentLost       = "ParentLost"
	TurnRefusedNotRunning       = "RunNotRunning"
	TurnRefusedInitialPending   = "InitialTurnPending"
	TurnRefusedActiveTurn       = "ActiveTurn"
	TurnRefusedParentNotReady   = "ParentNotReady"
	TurnRefusedStaleReadiness   = "StaleReadiness"
	TurnRefusedChangedIntent    = "ChangedIntent"
	TurnRefusedLeaseExpired     = "LeaseExpired"
	TurnRefusedTurnLimitReached = "TurnLimitReached"
)

// TurnRefusal says why a native parent may not take another turn. Message is
// safe to show a user: it names recorded state only, never operator paths.
type TurnRefusal struct {
	Reason  string
	Message string
	cause   error
}

func (r *TurnRefusal) Error() string { return r.Message }

// Unwrap keeps errors.Is(err, ErrTurnBusy) and ErrParentLeaseExpired working
// for callers that treat those two refusals specially.
func (r *TurnRefusal) Unwrap() error { return r.cause }

// TurnsUsed counts the turns a native parent has spent from enduring.maxTurns:
// the initial turn once it exists, plus every claimed follow-up. A failed turn
// (initial or later) still spent its turn; nothing is refunded.
func TurnsUsed(parent *api.CellnParentStatus) int32 {
	if parent == nil {
		return 0
	}
	used := parent.AcceptedTurns
	if parent.InitialTurn != nil {
		used++
	}
	return used
}

// TurnReadiness is the single rule for "may this native parent run take
// another turn". The API applies it before recording a turn and the controller
// applies it again, against a fresh read, before claiming the parent's slot.
// It returns nil when the turn may proceed and a *TurnRefusal otherwise.
//
// A turn is accepted when the run is a live enduring run in phase Running, no
// terminal owner outcome is recorded, the initial turn has a committed result,
// the owner reports the parent ready for the run's current generation, the
// frozen binding still matches the run's intent, no other turn owns the slot,
// the original lease has not elapsed, and the turn budget is not spent.
//
// Whether a committed turn SUCCEEDED is deliberately not part of the rule.
// The owner keeps a parent's context after a failed turn result (only a
// handler error ends the session, and that is reported as ContextLost), so a
// failed initial turn is as survivable as a failed later one. What makes a
// follow-up unsafe is an unknown owner state: a lost or stopped parent, an
// uncommitted turn, or readiness that was never (or is no longer) reported.
//
// claimant is the turn asking for the slot, or nil when no turn exists yet
// (API admission). The turn that already owns the slot is let through without
// spending budget again so its in-flight reconciliation can finish, even if
// the lease has since elapsed or readiness has since been withdrawn; the
// dispatch itself still checks the live owner before submitting anything.
func TurnReadiness(run *api.AgentRun, claimant *api.CellnActiveTurn, now time.Time) error {
	if run.DeletionTimestamp != nil {
		return &TurnRefusal{Reason: TurnRefusedDeleting, Message: "run is being deleted; its parent accepts no further turns"}
	}
	if run.Spec.ExecutionLifecycle != "enduring" || run.Spec.Enduring == nil {
		return &TurnRefusal{Reason: TurnRefusedNotEnduring, Message: "a one-shot parent answers once; it accepts no further turns"}
	}
	parent := run.Status.CellnParent
	if parent == nil || !parent.CreateAttempted {
		return &TurnRefusal{Reason: TurnRefusedNotAdmitted, Message: "parent not started yet; no turn can be accepted before it is admitted"}
	}
	if parent.OwnerOutcome != nil {
		return &TurnRefusal{Reason: TurnRefusedParentLost, Message: fmt.Sprintf("parent lost (%s); its live context cannot take another turn", parent.OwnerOutcome.Status)}
	}
	if run.Status.Phase != api.AgentRunPhaseRunning {
		phase := string(run.Status.Phase)
		if phase == "" {
			phase = "Pending"
		}
		return &TurnRefusal{Reason: TurnRefusedNotRunning, Message: fmt.Sprintf("run is %s, not Running; its parent accepts no turns", phase)}
	}
	if parent.InitialTurn == nil || parent.InitialTurn.Result == nil {
		return &TurnRefusal{Reason: TurnRefusedInitialPending, Message: "initial turn still running; its result must be committed before another turn"}
	}
	if active := parent.ActiveTurn; active != nil {
		if claimant != nil && active.Name == claimant.Name && active.UID == claimant.UID {
			return nil
		}
		return &TurnRefusal{Reason: TurnRefusedActiveTurn, Message: ErrTurnBusy.Error(), cause: ErrTurnBusy}
	}
	ready := meta.FindStatusCondition(run.Status.Conditions, ParentReadyCondition)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		reason := "no readiness reported"
		if ready != nil && ready.Reason != "" {
			reason = ready.Reason
		}
		return &TurnRefusal{Reason: TurnRefusedParentNotReady, Message: fmt.Sprintf("parent not ready (%s)", reason)}
	}
	if run.Generation < 1 || ready.ObservedGeneration != run.Generation {
		return &TurnRefusal{Reason: TurnRefusedStaleReadiness, Message: "parent readiness does not match current run intent"}
	}
	if err := ValidateAdmission(run, parent.Binding); err != nil {
		return &TurnRefusal{Reason: TurnRefusedChangedIntent, Message: "parent readiness does not match current run intent", cause: err}
	}
	if ParentLeaseExpired(parent, int64(run.Spec.Enduring.LeaseSeconds), now) {
		return &TurnRefusal{Reason: TurnRefusedLeaseExpired, Message: ErrParentLeaseExpired.Error() + "; original admission does not authorize new turns", cause: ErrParentLeaseExpired}
	}
	if parent.AcceptedTurns < 0 || parent.AcceptedTurns >= run.Spec.Enduring.MaxTurns-1 {
		return &TurnRefusal{Reason: TurnRefusedTurnLimitReached, Message: fmt.Sprintf("turn limit reached (%d of %d turns used, including the initial turn)", TurnsUsed(parent), run.Spec.Enduring.MaxTurns)}
	}
	return nil
}
