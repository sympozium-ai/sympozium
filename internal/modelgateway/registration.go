package modelgateway

import (
	"reflect"

	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
)

// retainRunAuthority prevents a fresh turn decision from becoming a fresh run
// registration. The original digest, ceilings and parent incarnation remain
// immutable; only the externally authorised turn's identity/window can change.
func retainRunAuthority(original, next cap.Decision) error {
	if original.Operation != "execution.start" || original.Lifecycle != "enduring-initial" || next.Operation != "execution.turn" || next.Lifecycle != "enduring-turn" {
		return fail(ReasonForbidden, 403, nil)
	}
	if original.Parent == nil || next.Parent == nil || original.Parent.Incarnation != next.Parent.Incarnation || next.Parent.TurnID == nil || *next.Parent.TurnID == "" {
		return fail(ReasonForbidden, 403, nil)
	}
	if original.ClusterID != next.ClusterID || original.Run != next.Run || original.Runtime != next.Runtime || original.Agent != next.Agent || original.Policy != next.Policy ||
		!reflect.DeepEqual(original.Tools, next.Tools) || !reflect.DeepEqual(original.Route, next.Route) {
		return fail(ReasonForbidden, 403, nil)
	}
	a, b := original.Budget, next.Budget
	if a.BudgetID != b.BudgetID || a.RunCap != b.RunCap || a.MaxTurns != b.MaxTurns || a.ParentDeadlineUnix != b.ParentDeadlineUnix ||
		b.TurnDeadlineUnix > b.ParentDeadlineUnix || b.TurnCap.Requests > a.TurnCap.Requests || b.TurnCap.OutputTokens > a.TurnCap.OutputTokens || b.TurnCap.Requests < 1 || b.TurnCap.OutputTokens < 1 {
		return fail(ReasonForbidden, 403, nil)
	}
	return nil
}
