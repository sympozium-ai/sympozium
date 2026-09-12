-- Durable, secret-free model gateway route authority. Provider credentials are
-- deliberately absent and are re-read from Kubernetes for every admission.
CREATE TABLE IF NOT EXISTS celln_model_authorities (
    budget_id TEXT NOT NULL REFERENCES celln_model_budgets(budget_id),
    turn_id TEXT NOT NULL,
    authority_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (budget_id, turn_id),
    FOREIGN KEY (budget_id, turn_id)
        REFERENCES celln_model_turn_budgets(budget_id, turn_id)
);

COMMENT ON TABLE celln_model_authorities IS
    'Immutable secret-free route and Kubernetes object identity bindings for mediated Celln model calls';
