package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
)

type watchedStore struct {
	BudgetStore
	unavailable atomic.Bool
	closed      atomic.Bool
}

func (s *watchedStore) Inspect(ctx context.Context, _, _ string) (modelbudget.Usage, error) {
	if s.unavailable.Load() {
		return modelbudget.Usage{}, errors.New("accounting unavailable")
	}
	return modelbudget.Usage{RunClosed: s.closed.Load()}, nil
}
func TestBudgetWatcherFailsClosed(t *testing.T) {
	for _, outage := range []bool{false, true} {
		t.Run(fmt.Sprint(outage), func(t *testing.T) {
			store := &watchedStore{}
			g := &Gateway{budgets: store}
			ctx, stop, err := g.watchBudget(context.Background(), "b", "t")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			if outage {
				store.unavailable.Store(true)
			} else {
				store.closed.Store(true)
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("active context survived fence/outage")
			}
			if _, _, err := g.watchBudget(context.Background(), "b", "t"); err == nil {
				t.Fatal("initial unavailable/closed authority admitted")
			}
		})
	}
}

func TestPostgresFenceCancelsActiveProviderAcrossPools(t *testing.T) {
	db := os.Getenv("CELLN_MODEL_BUDGET_DATABASE_URL")
	if db == "" {
		t.Skip("explicit PostgreSQL integration tier")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p1, err := pgxpool.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer p1.Close()
	p2, err := pgxpool.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	raw, err := os.ReadFile("../../migrations/002_celln_model_budget.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p1.Exec(ctx, string(raw)); err != nil {
		t.Fatal(err)
	}
	first, _ := modelbudget.New(p1)
	other, _ := modelbudget.New(p2)
	for _, turnOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(turnOnly), func(t *testing.T) {
			id := fmt.Sprintf("cancel-%d", time.Now().UnixNano())
			deadline := time.Now().Add(time.Minute).Truncate(time.Microsecond)
			if err := first.RegisterRun(ctx, modelbudget.RunRegistration{BudgetID: id, ClusterID: "c", NamespaceUID: "n", RunUID: "r", DecisionDigest: "d", RouteDigest: "route", MaxRequests: 1, MaxOutputTokens: 512, MaxTurns: 1, ParentDeadline: deadline}); err != nil {
				t.Fatal(err)
			}
			if err := first.RegisterTurn(ctx, modelbudget.TurnRegistration{BudgetID: id, TurnID: "t", DecisionDigest: "d", MaxRequests: 1, MaxOutputTokens: 512, Deadline: deadline}); err != nil {
				t.Fatal(err)
			}
			if _, err := first.Reserve(ctx, modelbudget.ReservationRequest{BudgetID: id, TurnID: "t", RequestID: "q", RequestDigest: "body", ReservedOutputTokens: 512}); err != nil {
				t.Fatal(err)
			}
			g := &Gateway{budgets: first}
			active, stop, err := g.watchBudget(ctx, id, "t")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			arrived := make(chan struct{})
			cancelled := make(chan struct{})
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(arrived); <-r.Context().Done(); close(cancelled) }))
			defer provider.Close()
			if err := first.MarkInFlight(active, id, "t", "q"); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				req, _ := http.NewRequestWithContext(active, "POST", provider.URL, nil)
				resp, err := provider.Client().Do(req)
				if resp != nil {
					resp.Body.Close()
				}
				result <- err
			}()
			select {
			case <-arrived:
			case <-ctx.Done():
				t.Fatal("provider not reached")
			}
			if turnOnly {
				err = other.FenceTurn(ctx, id, "t")
			} else {
				err = other.FenceRun(ctx, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-cancelled:
			case <-ctx.Done():
				t.Fatal("provider connection not cancelled")
			}
			if err := <-result; err == nil {
				t.Fatal("request unexpectedly succeeded")
			}
			usage, err := other.Inspect(ctx, id, "t")
			if err != nil {
				t.Fatal(err)
			}
			if usage.RunReservedRequests != 1 || usage.RunReservedOutputTokens != 512 {
				t.Fatal("cancellation refunded allowance")
			}
		})
	}
}
