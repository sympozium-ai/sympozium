package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"

	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
)

type CloseRequest struct {
	Decision json.RawMessage `json:"decision"`
}

// Close fences the original budget using separately scoped owner cleanup
// authority. It never reads current policy, route or Secret data: withdrawn
// execution permission and deleted credentials must not prevent cleanup.
func (g *Gateway) Close(ctx context.Context, token cap.Token, in CloseRequest) error {
	decision, err := decodeDecision(in.Decision)
	if err != nil {
		return fail(ReasonMalformed, 400, nil)
	}
	verify := contextFor(decision, "execution.cleanup")
	verify.ClusterID = g.config.ClusterID
	verified, err := g.verifier.Verify(token, in.Decision, verify)
	if err != nil {
		return fail(ReasonUnauthorized, 401, nil)
	}
	initial, err := g.authorities.Authority(ctx, decision.Budget.BudgetID, decision.Run.UID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) && (decision.Parent == nil || decision.Parent.TurnID == nil) {
			// The run never registered (or registration was refused or
			// interrupted): nothing was served under it. Missing authority is
			// not proof of absence, so publish a closed budget that refuses
			// any late registration, and the run's cleanup can complete.
			return g.fenceUnregisteredRun(ctx, decision, verified.DecisionDigest)
		}
		return err
	}
	original := initial.Decision
	if original.ClusterID != decision.ClusterID || original.Run != decision.Run || original.Budget.BudgetID != decision.Budget.BudgetID {
		return fail(ReasonForbidden, 403, nil)
	}
	if (original.Parent == nil) != (decision.Parent == nil) || (original.Parent != nil && original.Parent.Incarnation != decision.Parent.Incarnation) {
		return fail(ReasonForbidden, 403, nil)
	}
	if decision.Parent != nil && decision.Parent.TurnID != nil {
		// A child-scoped cleanup permit must never widen into parent closure.
		tid := *decision.Parent.TurnID
		turn, err := g.authorities.Authority(ctx, decision.Budget.BudgetID, tid)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// Missing authority is not proof of absence: registration may be
			// racing or partially committed. Verify the unchanged original
			// parent authority, then atomically fence the exact budget row.
			candidate := decision
			candidate.Operation = "execution.turn"
			if err := retainRunAuthority(original, candidate); err != nil {
				return err
			}
			return g.budgets.FenceTurnRegistration(ctx, modelbudget.TurnRegistration{
				BudgetID: decision.Budget.BudgetID, TurnID: tid, DecisionDigest: verified.DecisionDigest,
				MaxRequests: decision.Budget.TurnCap.Requests, MaxOutputTokens: decision.Budget.TurnCap.OutputTokens,
				Deadline: time.Unix(decision.Budget.TurnDeadlineUnix, 0).UTC(),
			})
		}
		if turn.Decision.Run != original.Run || turn.Decision.ClusterID != original.ClusterID || turn.Decision.Parent == nil || turn.Decision.Parent.Incarnation != original.Parent.Incarnation || turn.TurnID != tid {
			return fail(ReasonForbidden, 403, nil)
		}
		return g.budgets.FenceTurn(ctx, decision.Budget.BudgetID, tid)
	}
	return g.budgets.FenceRun(ctx, decision.Budget.BudgetID)
}

// fenceUnregisteredRun closes the budget of a run with no registered authority,
// from its own verified cleanup decision. Fencing only ever withdraws service.
func (g *Gateway) fenceUnregisteredRun(ctx context.Context, decision cap.Decision, digest string) error {
	routeDigest, err := digestJSON(decision.Route)
	if err != nil {
		return fail(ReasonMalformed, 400, err)
	}
	deadline := time.Unix(decision.Budget.ParentDeadlineUnix, 0).UTC()
	if decision.Budget.ParentDeadlineUnix == 0 {
		deadline = time.Unix(decision.Budget.TurnDeadlineUnix, 0).UTC()
	}
	return g.budgets.FenceRunRegistration(ctx, modelbudget.RunRegistration{
		BudgetID: decision.Budget.BudgetID, ClusterID: decision.ClusterID, NamespaceUID: decision.Run.NamespaceUID,
		RunUID: decision.Run.UID, DecisionDigest: digest, RouteDigest: routeDigest,
		MaxRequests: decision.Budget.RunCap.Requests, MaxOutputTokens: decision.Budget.RunCap.OutputTokens,
		MaxTurns: decision.Budget.MaxTurns, ParentDeadline: deadline,
	})
}
