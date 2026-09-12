# Durable Celln model budget ledger

`internal/modelbudget` is the production PostgreSQL accounting boundary for issue #503.

The ledger is monotonic: request/output allowance is reserved before provider work and is never refunded automatically, including provider timeout, cancellation, malformed usage, or crash uncertainty. Duplicate `(budget, turn, request)` delivery with the same digest recovers the existing reservation; the same identity with different bytes is a conflict.

## Migration

Apply `migrations/002_celln_model_budget.sql` after the existing initial migration. `Store.CheckReady` fails closed if the three ledger tables are absent. Mediated model access must not fall back to an in-memory counter when PostgreSQL is unavailable.

## Integration proof

The deterministic PostgreSQL tests are explicit rather than silently skipped as release evidence:

```bash
CELLN_MODEL_BUDGET_DATABASE_URL='postgres://...' \
  go test -race ./internal/modelbudget -run Postgres -count=1 -v
```

They cover concurrent exhaustion, duplicate suppression/conflict, cross-turn aggregate ceilings, non-refundable reconciliation, and terminal fencing. Normal unit CI still exercises validation without requiring a database; the final installed release gate must run the PostgreSQL tier.
