package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresAuthorityStore struct{ pool *pgxpool.Pool }

func NewPostgresAuthorityStore(pool *pgxpool.Pool) (*PostgresAuthorityStore, error) {
	if pool == nil {
		return nil, fail(ReasonUnavailable, 503, fmt.Errorf("postgres pool is required"))
	}
	return &PostgresAuthorityStore{pool: pool}, nil
}

func (s *PostgresAuthorityStore) RegisterAuthority(ctx context.Context, in Authority) error {
	if in.BudgetID == "" || in.TurnID == "" || in.DecisionDigest == "" || in.ConnectionName == "" {
		return fail(ReasonMalformed, 400, nil)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return fail(ReasonMalformed, 400, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.pool.Exec(ctx, `INSERT INTO celln_model_authorities (budget_id, turn_id, authority_json)
		VALUES ($1,$2,$3) ON CONFLICT (budget_id, turn_id) DO NOTHING`, in.BudgetID, in.TurnID, raw)
	if err != nil {
		return fail(ReasonUnavailable, 503, err)
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	existing, err := s.Authority(ctx, in.BudgetID, in.TurnID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(existing, in) {
		return fail(ReasonRequestConflict, 409, nil)
	}
	return nil
}

func (s *PostgresAuthorityStore) Authority(ctx context.Context, budgetID, turnID string) (Authority, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT authority_json FROM celln_model_authorities WHERE budget_id=$1 AND turn_id=$2`, budgetID, turnID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Authority{}, fail(ReasonForbidden, 403, err)
	}
	if err != nil {
		return Authority{}, fail(ReasonUnavailable, 503, err)
	}
	var out Authority
	if err := decodeStrict(raw, &out); err != nil {
		return Authority{}, fail(ReasonUnavailable, 503, err)
	}
	return out, nil
}

func (s *PostgresAuthorityStore) CheckReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var table *string
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('celln_model_authorities')::text`).Scan(&table); err != nil || table == nil {
		return fail(ReasonUnavailable, 503, err)
	}
	return nil
}
