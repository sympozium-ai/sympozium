package modelgateway

import (
	"context"
	"time"

	"github.com/sympozium-ai/sympozium/internal/modelbudget"
)

// watchBudget observes fences committed by any replica. Polling is bounded,
// not instantaneous revocation: an already admitted HTTP request may reach the
// provider before a fence is observed. Cancellation does not refund allowance.
// Authority/accounting outages cancel locally instead of using stale approval.
func (g *Gateway) watchBudget(parent context.Context, budgetID, turnID string) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(parent)
	check := func() error {
		query, done := context.WithTimeout(ctx, time.Second)
		defer done()
		usage, err := g.budgets.Inspect(query, budgetID, turnID)
		if err != nil {
			return err
		}
		if usage.RunClosed || usage.TurnClosed {
			return &modelbudget.Error{Reason: modelbudget.ReasonClosed}
		}
		return nil
	}
	if err := check(); err != nil {
		cancel()
		return nil, nil, err
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if check() != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(); <-finished }, nil
}
