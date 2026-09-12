package modelgateway

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
)

type failTurnRegistration struct{ BudgetStore }

func (s failTurnRegistration) RegisterTurn(context.Context, modelbudget.TurnRegistration) error {
	return fail(ReasonUnavailable, 503, nil)
}

type failAuthorityRegistration struct{ AuthorityStore }

func (s failAuthorityRegistration) RegisterAuthority(context.Context, Authority) error {
	return fail(ReasonUnavailable, 503, nil)
}

// Called by BOTH the PostgreSQL and live Kubernetes fixtures. Simulate process
// loss at each durable publication boundary, then recover through fresh pools.
func exerciseRegistrationRecovery(t *testing.T, g *Gateway, issuer *cap.Issuer, original cap.Decision) {
	t.Helper()
	ctx := context.Background()
	for _, stage := range []string{"after-run", "after-turn"} {
		t.Run(stage, func(t *testing.T) {
			d := original
			d.Run.UID = fmt.Sprintf("recovery-%s-%d", stage, time.Now().UnixNano())
			d.Budget.BudgetID = d.Run.UID
			raw, _, err := cap.CanonicalDecision(d)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := issuer.Issue(d, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: "execution.start"})
			if err != nil {
				t.Fatal(err)
			}
			model, err := issuer.Issue(d, cap.IssueRequest{Audience: cap.AudienceModelGateway, Operation: "model.invoke"})
			if err != nil {
				t.Fatal(err)
			}
			in := RegistrationRequest{Decision: raw, ExecutionToken: execution, ConnectionName: "model"}
			interrupted := *g
			if stage == "after-run" {
				interrupted.budgets = failTurnRegistration{g.budgets}
			} else {
				interrupted.authorities = failAuthorityRegistration{g.authorities}
			}
			if err := interrupted.Register(ctx, in); err == nil {
				t.Fatal("injected publication failure ignored")
			}
			pool, err := pgxpool.New(ctx, os.Getenv("CELLN_MODEL_BUDGET_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			budget, err := modelbudget.New(pool)
			if err != nil {
				t.Fatal(err)
			}
			authority, err := NewPostgresAuthorityStore(pool)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := New(g.config, g.verifier, g.k8s, budget, authority)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recovered.Invoke(ctx, model, InvokeRequest{Decision: raw, RequestID: "not-published", Request: []byte(`{"model":"m","messages":[],"max_tokens":1}`)}); err == nil {
				t.Fatal("partial registration authorized provider work")
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := recovered.Register(ctx, in); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			usage, err := budget.Inspect(ctx, d.Budget.BudgetID, turnID(d))
			if err != nil || usage.RunReservedRequests != 0 || usage.TurnReservedRequests != 0 {
				t.Fatalf("registration/recovery spent allowance: %+v %v", usage, err)
			}
			d.Budget.RunCap.Requests++
			raw, _, err = cap.CanonicalDecision(d)
			if err != nil {
				t.Fatal(err)
			}
			execution, err = issuer.Issue(d, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: "execution.start"})
			if err != nil {
				t.Fatal(err)
			}
			if err := recovered.Register(ctx, RegistrationRequest{Decision: raw, ExecutionToken: execution, ConnectionName: "model"}); err == nil {
				t.Fatal("changed recovery topped up original registration")
			}
		})
	}
}
