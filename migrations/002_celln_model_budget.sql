-- Durable Celln model-gateway accounting for #503.
-- Reservations are authority consumption and are intentionally non-refundable.
-- Provider execution is not transactionally coupled to PostgreSQL; ambiguous
-- outcomes remain reserved/uncertain and are never replayed automatically.

CREATE TABLE IF NOT EXISTS celln_model_budgets (
    budget_id TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL,
    namespace_uid TEXT NOT NULL,
    run_uid TEXT NOT NULL,
    decision_digest TEXT NOT NULL,
    route_digest TEXT NOT NULL,
    max_requests BIGINT NOT NULL CHECK (max_requests >= 0),
    max_output_tokens BIGINT NOT NULL CHECK (max_output_tokens >= 0),
    max_turns BIGINT NOT NULL CHECK (max_turns >= 1),
    parent_deadline TIMESTAMPTZ NOT NULL,
    reserved_requests BIGINT NOT NULL DEFAULT 0 CHECK (reserved_requests >= 0),
    reserved_output_tokens BIGINT NOT NULL DEFAULT 0 CHECK (reserved_output_tokens >= 0),
    observed_output_tokens BIGINT NOT NULL DEFAULT 0 CHECK (observed_output_tokens >= 0),
    turns_registered BIGINT NOT NULL DEFAULT 0 CHECK (turns_registered >= 0),
    closed BOOLEAN NOT NULL DEFAULT FALSE,
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, namespace_uid, run_uid, budget_id)
);

CREATE TABLE IF NOT EXISTS celln_model_turn_budgets (
    budget_id TEXT NOT NULL REFERENCES celln_model_budgets(budget_id) ON DELETE RESTRICT,
    turn_id TEXT NOT NULL,
    decision_digest TEXT NOT NULL,
    max_requests BIGINT NOT NULL CHECK (max_requests >= 0),
    max_output_tokens BIGINT NOT NULL CHECK (max_output_tokens >= 0),
    deadline TIMESTAMPTZ NOT NULL,
    reserved_requests BIGINT NOT NULL DEFAULT 0 CHECK (reserved_requests >= 0),
    reserved_output_tokens BIGINT NOT NULL DEFAULT 0 CHECK (reserved_output_tokens >= 0),
    observed_output_tokens BIGINT NOT NULL DEFAULT 0 CHECK (observed_output_tokens >= 0),
    closed BOOLEAN NOT NULL DEFAULT FALSE,
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (budget_id, turn_id)
);

CREATE TABLE IF NOT EXISTS celln_model_reservations (
    budget_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    request_digest TEXT NOT NULL,
    reserved_output_tokens BIGINT NOT NULL CHECK (reserved_output_tokens >= 0),
    observed_output_tokens BIGINT CHECK (observed_output_tokens IS NULL OR observed_output_tokens >= 0),
    state TEXT NOT NULL CHECK (state IN ('reserved', 'in_flight', 'terminal', 'uncertain')),
    outcome TEXT,
    provider_attempted BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (budget_id, turn_id, request_id),
    FOREIGN KEY (budget_id, turn_id)
        REFERENCES celln_model_turn_budgets(budget_id, turn_id)
        ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS idx_celln_model_budgets_owner
    ON celln_model_budgets(cluster_id, namespace_uid, run_uid);
CREATE INDEX IF NOT EXISTS idx_celln_model_reservations_state
    ON celln_model_reservations(budget_id, turn_id, state);
