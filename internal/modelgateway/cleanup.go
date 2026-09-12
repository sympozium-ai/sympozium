package modelgateway

import (
	"context"
	"encoding/json"

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
	if _, err = g.verifier.Verify(token, in.Decision, verify); err != nil {
		return fail(ReasonUnauthorized, 401, nil)
	}
	initial, err := g.authorities.Authority(ctx, decision.Budget.BudgetID, decision.Run.UID)
	if err != nil {
		return err
	}
	original := initial.Decision
	if original.ClusterID != decision.ClusterID || original.Run != decision.Run || original.Budget.BudgetID != decision.Budget.BudgetID {
		return fail(ReasonForbidden, 403, nil)
	}
	if (original.Parent == nil) != (decision.Parent == nil) || (original.Parent != nil && original.Parent.Incarnation != decision.Parent.Incarnation) {
		return fail(ReasonForbidden, 403, nil)
	}
	return g.budgets.FenceRun(ctx, decision.Budget.BudgetID)
}
