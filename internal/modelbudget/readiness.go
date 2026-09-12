package modelbudget

import (
	"context"
	"time"
)

// CheckReady fails closed when the durable ledger migration is absent or the
// database cannot be queried. Callers must not substitute in-memory accounting.
func (s *Store) CheckReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var runs, turns, reservations *string
	if err := s.pool.QueryRow(ctx, `SELECT
		to_regclass('celln_model_budgets')::text,
		to_regclass('celln_model_turn_budgets')::text,
		to_regclass('celln_model_reservations')::text`).Scan(&runs, &turns, &reservations); err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if runs == nil || turns == nil || reservations == nil {
		return reasonError(ReasonUnavailable, nil)
	}
	return nil
}
