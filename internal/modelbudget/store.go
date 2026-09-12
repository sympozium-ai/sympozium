package modelbudget

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func New(pool *pgxpool.Pool) (*Store, error) {
	if pool == nil {
		return nil, reasonError(ReasonUnavailable, fmt.Errorf("postgres pool is required"))
	}
	return &Store{pool: pool, clock: time.Now}, nil
}

func (s *Store) RegisterRun(ctx context.Context, in RunRegistration) error {
	if err := validateRunRegistration(in); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	result, err := tx.Exec(ctx, `
		INSERT INTO celln_model_budgets (
			budget_id, cluster_id, namespace_uid, run_uid, decision_digest, route_digest,
			max_requests, max_output_tokens, max_turns, parent_deadline
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (budget_id) DO NOTHING`,
		in.BudgetID, in.ClusterID, in.NamespaceUID, in.RunUID, in.DecisionDigest, in.RouteDigest,
		in.MaxRequests, in.MaxOutputTokens, in.MaxTurns, in.ParentDeadline.UTC())
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if result.RowsAffected() == 0 {
		var got RunRegistration
		var closed bool
		err = tx.QueryRow(ctx, `
			SELECT budget_id, cluster_id, namespace_uid, run_uid, decision_digest, route_digest,
			       max_requests, max_output_tokens, max_turns, parent_deadline, closed
			FROM celln_model_budgets WHERE budget_id=$1 FOR UPDATE`, in.BudgetID).Scan(
			&got.BudgetID, &got.ClusterID, &got.NamespaceUID, &got.RunUID, &got.DecisionDigest, &got.RouteDigest,
			&got.MaxRequests, &got.MaxOutputTokens, &got.MaxTurns, &got.ParentDeadline, &closed)
		if err != nil {
			return reasonError(ReasonUnavailable, err)
		}
		if closed || !sameRunRegistration(got, in) {
			return reasonError(ReasonRegisterConflict, nil)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	return nil
}

func (s *Store) RegisterTurn(ctx context.Context, in TurnRegistration) error {
	if err := validateTurnRegistration(in); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var maxRequests, maxOutputTokens, maxTurns, turns int64
	var parentDeadline time.Time
	var closed bool
	err = tx.QueryRow(ctx, `
		SELECT max_requests, max_output_tokens, max_turns, turns_registered, parent_deadline, closed
		FROM celln_model_budgets WHERE budget_id=$1 FOR UPDATE`, in.BudgetID).Scan(
		&maxRequests, &maxOutputTokens, &maxTurns, &turns, &parentDeadline, &closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return reasonError(ReasonNotFound, err)
	}
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if closed {
		return reasonError(ReasonClosed, nil)
	}
	now := s.clock().UTC()
	if !now.Before(parentDeadline) || in.Deadline.After(parentDeadline) {
		return reasonError(ReasonDeadline, nil)
	}
	if in.MaxRequests > maxRequests || in.MaxOutputTokens > maxOutputTokens {
		return reasonError(ReasonRegisterConflict, nil)
	}

	var existing TurnRegistration
	var turnClosed bool
	err = tx.QueryRow(ctx, `
		SELECT budget_id, turn_id, decision_digest, max_requests, max_output_tokens, deadline, closed
		FROM celln_model_turn_budgets WHERE budget_id=$1 AND turn_id=$2`, in.BudgetID, in.TurnID).Scan(
		&existing.BudgetID, &existing.TurnID, &existing.DecisionDigest, &existing.MaxRequests,
		&existing.MaxOutputTokens, &existing.Deadline, &turnClosed)
	if err == nil {
		if turnClosed || !sameTurnRegistration(existing, in) {
			return reasonError(ReasonRegisterConflict, nil)
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return reasonError(ReasonUnavailable, err)
	}
	if turns >= maxTurns {
		return reasonError(ReasonExhausted, nil)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO celln_model_turn_budgets
			(budget_id, turn_id, decision_digest, max_requests, max_output_tokens, deadline)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		in.BudgetID, in.TurnID, in.DecisionDigest, in.MaxRequests, in.MaxOutputTokens, in.Deadline.UTC())
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	_, err = tx.Exec(ctx, `UPDATE celln_model_budgets
		SET turns_registered=turns_registered+1, updated_at=now()
		WHERE budget_id=$1`, in.BudgetID)
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	return nil
}

func (s *Store) Reserve(ctx context.Context, in ReservationRequest) (Reservation, error) {
	if err := validateReservationRequest(in); err != nil {
		return Reservation{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Reservation{}, reasonError(ReasonUnavailable, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var runMaxReq, runMaxOut, runReservedReq, runReservedOut int64
	var parentDeadline time.Time
	var runClosed bool
	err = tx.QueryRow(ctx, `
		SELECT max_requests, max_output_tokens, reserved_requests, reserved_output_tokens,
		       parent_deadline, closed
		FROM celln_model_budgets WHERE budget_id=$1 FOR UPDATE`, in.BudgetID).Scan(
		&runMaxReq, &runMaxOut, &runReservedReq, &runReservedOut, &parentDeadline, &runClosed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, reasonError(ReasonNotFound, err)
	}
	if err != nil {
		return Reservation{}, reasonError(ReasonUnavailable, err)
	}

	var turnMaxReq, turnMaxOut, turnReservedReq, turnReservedOut int64
	var turnDeadline time.Time
	var turnClosed bool
	err = tx.QueryRow(ctx, `
		SELECT max_requests, max_output_tokens, reserved_requests, reserved_output_tokens,
		       deadline, closed
		FROM celln_model_turn_budgets
		WHERE budget_id=$1 AND turn_id=$2 FOR UPDATE`, in.BudgetID, in.TurnID).Scan(
		&turnMaxReq, &turnMaxOut, &turnReservedReq, &turnReservedOut, &turnDeadline, &turnClosed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, reasonError(ReasonNotFound, err)
	}
	if err != nil {
		return Reservation{}, reasonError(ReasonUnavailable, err)
	}

	existing, found, err := loadReservation(ctx, tx, in.BudgetID, in.TurnID, in.RequestID)
	if err != nil {
		return Reservation{}, reasonError(ReasonUnavailable, err)
	}
	if found {
		if existing.RequestDigest != in.RequestDigest || existing.ReservedOutputTokens != in.ReservedOutputTokens {
			return Reservation{}, reasonError(ReasonRequestConflict, nil)
		}
		existing.Existing = true
		if err := tx.Commit(ctx); err != nil {
			return Reservation{}, reasonError(ReasonUnavailable, err)
		}
		return existing, nil
	}

	now := s.clock().UTC()
	if runClosed || turnClosed {
		return Reservation{}, reasonError(ReasonClosed, nil)
	}
	if !now.Before(parentDeadline) || !now.Before(turnDeadline) {
		return Reservation{}, reasonError(ReasonDeadline, nil)
	}
	if runReservedReq >= runMaxReq || turnReservedReq >= turnMaxReq ||
		in.ReservedOutputTokens > runMaxOut-runReservedOut ||
		in.ReservedOutputTokens > turnMaxOut-turnReservedOut {
		return Reservation{}, reasonError(ReasonExhausted, nil)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO celln_model_reservations
			(budget_id, turn_id, request_id, request_digest, reserved_output_tokens, state)
		VALUES ($1,$2,$3,$4,$5,'reserved')`,
		in.BudgetID, in.TurnID, in.RequestID, in.RequestDigest, in.ReservedOutputTokens)
	if err != nil {
		return Reservation{}, reasonError(ReasonUnavailable, err)
	}
	_, err = tx.Exec(ctx, `UPDATE celln_model_budgets SET
		reserved_requests=reserved_requests+1,
		reserved_output_tokens=reserved_output_tokens+$2,
		updated_at=now() WHERE budget_id=$1`, in.BudgetID, in.ReservedOutputTokens)
	if err != nil {
		return Reservation{}, reasonError(ReasonUnavailable, err)
	}
	_, err = tx.Exec(ctx, `UPDATE celln_model_turn_budgets SET
		reserved_requests=reserved_requests+1,
		reserved_output_tokens=reserved_output_tokens+$3,
		updated_at=now() WHERE budget_id=$1 AND turn_id=$2`, in.BudgetID, in.TurnID, in.ReservedOutputTokens)
	if err != nil {
		return Reservation{}, reasonError(ReasonUnavailable, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Reservation{}, reasonError(ReasonUnavailable, err)
	}
	return Reservation{
		BudgetID: in.BudgetID, TurnID: in.TurnID, RequestID: in.RequestID,
		RequestDigest: in.RequestDigest, ReservedOutputTokens: in.ReservedOutputTokens,
		State: "reserved",
	}, nil
}

func (s *Store) MarkInFlight(ctx context.Context, budgetID, turnID, requestID string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Reservation is not permission to start after a closing fence. Lock in
	// admission order and claim exactly once before contacting the provider.
	var closed bool
	var deadline time.Time
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`SELECT closed, parent_deadline FROM celln_model_budgets WHERE budget_id=$1 FOR UPDATE`, []any{budgetID}},
		{`SELECT closed, deadline FROM celln_model_turn_budgets WHERE budget_id=$1 AND turn_id=$2 FOR UPDATE`, []any{budgetID, turnID}},
	} {
		if err := tx.QueryRow(ctx, query.sql, query.args...).Scan(&closed, &deadline); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return reasonError(ReasonNotFound, nil)
			}
			return reasonError(ReasonUnavailable, err)
		}
		if closed {
			return reasonError(ReasonClosed, nil)
		}
		if !s.clock().UTC().Before(deadline) {
			return reasonError(ReasonDeadline, nil)
		}
	}
	result, err := tx.Exec(ctx, `UPDATE celln_model_reservations
		SET state='in_flight', provider_attempted=TRUE, updated_at=now()
		WHERE budget_id=$1 AND turn_id=$2 AND request_id=$3 AND state='reserved'`, budgetID, turnID, requestID)
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if result.RowsAffected() == 0 {
		return reasonError(ReasonRequestConflict, nil)
	}
	if err := tx.Commit(ctx); err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	return nil
}

func (s *Store) Reconcile(ctx context.Context, budgetID, turnID, requestID string, observedOutput int64, state, outcome string) error {
	if observedOutput < 0 || (state != "terminal" && state != "uncertain") {
		return reasonError(ReasonRegisterConflict, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Use the same run -> turn -> reservation ordering as Reserve. Serializing
	// reconciliation prevents duplicate delivery from counting observed usage
	// twice and avoids a reservation/run lock inversion with admission.
	var locked string
	if err := tx.QueryRow(ctx, `SELECT budget_id FROM celln_model_budgets WHERE budget_id=$1 FOR UPDATE`, budgetID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reasonError(ReasonNotFound, nil)
		}
		return reasonError(ReasonUnavailable, err)
	}
	if err := tx.QueryRow(ctx, `SELECT turn_id FROM celln_model_turn_budgets WHERE budget_id=$1 AND turn_id=$2 FOR UPDATE`, budgetID, turnID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reasonError(ReasonNotFound, nil)
		}
		return reasonError(ReasonUnavailable, err)
	}
	reservation, found, err := loadReservation(ctx, tx, budgetID, turnID, requestID)
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if !found {
		return reasonError(ReasonNotFound, nil)
	}
	if reservation.State == "terminal" || reservation.State == "uncertain" {
		if reservation.ObservedOutputTokens != nil && *reservation.ObservedOutputTokens == observedOutput && reservation.State == state && reservation.Outcome == outcome {
			return tx.Commit(ctx)
		}
		return reasonError(ReasonRequestConflict, nil)
	}

	_, err = tx.Exec(ctx, `UPDATE celln_model_reservations SET
		observed_output_tokens=$4, state=$5, outcome=$6, updated_at=now()
		WHERE budget_id=$1 AND turn_id=$2 AND request_id=$3`, budgetID, turnID, requestID, observedOutput, state, outcome)
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	_, err = tx.Exec(ctx, `UPDATE celln_model_budgets SET observed_output_tokens=observed_output_tokens+$2, updated_at=now() WHERE budget_id=$1`, budgetID, observedOutput)
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	_, err = tx.Exec(ctx, `UPDATE celln_model_turn_budgets SET observed_output_tokens=observed_output_tokens+$3, updated_at=now() WHERE budget_id=$1 AND turn_id=$2`, budgetID, turnID, observedOutput)
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if observedOutput > reservation.ReservedOutputTokens {
		return reasonError(ReasonUsageExceeded, nil)
	}
	return nil
}

func (s *Store) FenceTurn(ctx context.Context, budgetID, turnID string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.pool.Exec(ctx, `UPDATE celln_model_turn_budgets SET closed=TRUE, closed_at=COALESCE(closed_at, now()), updated_at=now()
		WHERE budget_id=$1 AND turn_id=$2`, budgetID, turnID)
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if result.RowsAffected() == 0 {
		return reasonError(ReasonNotFound, nil)
	}
	return nil
}

func (s *Store) FenceRun(ctx context.Context, budgetID string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	result, err := tx.Exec(ctx, `UPDATE celln_model_budgets SET closed=TRUE, closed_at=COALESCE(closed_at, now()), updated_at=now() WHERE budget_id=$1`, budgetID)
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	if result.RowsAffected() == 0 {
		return reasonError(ReasonNotFound, nil)
	}
	_, err = tx.Exec(ctx, `UPDATE celln_model_turn_budgets SET closed=TRUE, closed_at=COALESCE(closed_at, now()), updated_at=now() WHERE budget_id=$1`, budgetID)
	if err != nil {
		return reasonError(ReasonUnavailable, err)
	}
	return tx.Commit(ctx)
}

func (s *Store) Inspect(ctx context.Context, budgetID, turnID string) (Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out Usage
	err := s.pool.QueryRow(ctx, `SELECT
		r.reserved_requests, r.reserved_output_tokens, r.observed_output_tokens, r.closed,
		t.reserved_requests, t.reserved_output_tokens, t.observed_output_tokens, t.closed
		FROM celln_model_budgets r JOIN celln_model_turn_budgets t ON t.budget_id=r.budget_id
		WHERE r.budget_id=$1 AND t.turn_id=$2`, budgetID, turnID).Scan(
		&out.RunReservedRequests, &out.RunReservedOutputTokens, &out.RunObservedOutputTokens, &out.RunClosed,
		&out.TurnReservedRequests, &out.TurnReservedOutputTokens, &out.TurnObservedOutputTokens, &out.TurnClosed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Usage{}, reasonError(ReasonNotFound, err)
	}
	if err != nil {
		return Usage{}, reasonError(ReasonUnavailable, err)
	}
	return out, nil
}

func loadReservation(ctx context.Context, tx pgx.Tx, budgetID, turnID, requestID string) (Reservation, bool, error) {
	var out Reservation
	var observed *int64
	err := tx.QueryRow(ctx, `SELECT budget_id, turn_id, request_id, request_digest,
		reserved_output_tokens, observed_output_tokens, state, COALESCE(outcome,''), provider_attempted
		FROM celln_model_reservations WHERE budget_id=$1 AND turn_id=$2 AND request_id=$3`, budgetID, turnID, requestID).Scan(
		&out.BudgetID, &out.TurnID, &out.RequestID, &out.RequestDigest, &out.ReservedOutputTokens,
		&observed, &out.State, &out.Outcome, &out.ProviderAttempted)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, false, nil
	}
	if err != nil {
		return Reservation{}, false, err
	}
	out.ObservedOutputTokens = observed
	return out, true, nil
}

func validateRunRegistration(in RunRegistration) error {
	if in.BudgetID == "" || in.ClusterID == "" || in.NamespaceUID == "" || in.RunUID == "" ||
		in.DecisionDigest == "" || in.RouteDigest == "" || in.MaxRequests < 0 || in.MaxOutputTokens < 0 ||
		in.MaxTurns < 1 || in.ParentDeadline.IsZero() {
		return reasonError(ReasonRegisterConflict, nil)
	}
	return nil
}

func validateTurnRegistration(in TurnRegistration) error {
	if in.BudgetID == "" || in.TurnID == "" || in.DecisionDigest == "" || in.MaxRequests < 0 ||
		in.MaxOutputTokens < 0 || in.Deadline.IsZero() {
		return reasonError(ReasonRegisterConflict, nil)
	}
	return nil
}

func validateReservationRequest(in ReservationRequest) error {
	if in.BudgetID == "" || in.TurnID == "" || in.RequestID == "" || in.RequestDigest == "" || in.ReservedOutputTokens < 0 {
		return reasonError(ReasonRequestConflict, nil)
	}
	return nil
}

func sameRunRegistration(a, b RunRegistration) bool {
	return a.BudgetID == b.BudgetID && a.ClusterID == b.ClusterID && a.NamespaceUID == b.NamespaceUID &&
		a.RunUID == b.RunUID && a.DecisionDigest == b.DecisionDigest && a.RouteDigest == b.RouteDigest &&
		a.MaxRequests == b.MaxRequests && a.MaxOutputTokens == b.MaxOutputTokens && a.MaxTurns == b.MaxTurns &&
		a.ParentDeadline.UTC().Equal(b.ParentDeadline.UTC())
}

func sameTurnRegistration(a, b TurnRegistration) bool {
	return a.BudgetID == b.BudgetID && a.TurnID == b.TurnID && a.DecisionDigest == b.DecisionDigest &&
		a.MaxRequests == b.MaxRequests && a.MaxOutputTokens == b.MaxOutputTokens && a.Deadline.UTC().Equal(b.Deadline.UTC())
}
