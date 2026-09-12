package modelbudget

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestValidationFailsClosedWithoutDatabase(t *testing.T) {
	if _, err := New(nil); Reason(err) != ReasonUnavailable {
		t.Fatalf("nil store: expected %s, got %s", ReasonUnavailable, Reason(err))
	}
	if err := validateRunRegistration(RunRegistration{}); Reason(err) != ReasonRegisterConflict {
		t.Fatalf("empty run registration: got %s", Reason(err))
	}
	if err := validateTurnRegistration(TurnRegistration{}); Reason(err) != ReasonRegisterConflict {
		t.Fatalf("empty turn registration: got %s", Reason(err))
	}
	if err := validateReservationRequest(ReservationRequest{}); Reason(err) != ReasonRequestConflict {
		t.Fatalf("empty reservation: got %s", Reason(err))
	}
}

func TestRegistrationEqualityIncludesAuthorityAndDeadlines(t *testing.T) {
	deadline := time.Unix(1_790_000_600, 0).UTC()
	a := RunRegistration{BudgetID: "b", ClusterID: "c", NamespaceUID: "n", RunUID: "r", DecisionDigest: "d", RouteDigest: "route", MaxRequests: 3, MaxOutputTokens: 1024, MaxTurns: 2, ParentDeadline: deadline}
	if !sameRunRegistration(a, a) {
		t.Fatal("identical run registration should compare equal")
	}
	b := a
	b.RouteDigest = "other"
	if sameRunRegistration(a, b) {
		t.Fatal("route change must conflict")
	}
	turn := TurnRegistration{BudgetID: "b", TurnID: "t", DecisionDigest: "d", MaxRequests: 2, MaxOutputTokens: 512, Deadline: deadline}
	if !sameTurnRegistration(turn, turn) {
		t.Fatal("identical turn registration should compare equal")
	}
	turn2 := turn
	turn2.Deadline = deadline.Add(time.Second)
	if sameTurnRegistration(turn, turn2) {
		t.Fatal("deadline extension must conflict")
	}
}

func TestPostgresAtomicReservations(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	store.clock = func() time.Time { return now }
	budgetID := fmt.Sprintf("budget-%d", now.UnixNano())

	mustRegisterRun(t, ctx, store, RunRegistration{
		BudgetID: budgetID, ClusterID: "cluster-a", NamespaceUID: "ns-a", RunUID: "run-a",
		DecisionDigest: "sha256:decision", RouteDigest: "sha256:route",
		MaxRequests: 3, MaxOutputTokens: 1024, MaxTurns: 2, ParentDeadline: now.Add(time.Hour),
	})
	mustRegisterTurn(t, ctx, store, TurnRegistration{
		BudgetID: budgetID, TurnID: "turn-1", DecisionDigest: "sha256:turn-1",
		MaxRequests: 3, MaxOutputTokens: 1024, Deadline: now.Add(10 * time.Minute),
	})

	var accepted atomic.Int64
	var exhausted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Reserve(ctx, ReservationRequest{
				BudgetID: budgetID, TurnID: "turn-1",
				RequestID: fmt.Sprintf("request-%02d", i), RequestDigest: fmt.Sprintf("sha256:req-%02d", i),
				ReservedOutputTokens: 512,
			})
			switch Reason(err) {
			case "":
				accepted.Add(1)
			case ReasonExhausted:
				exhausted.Add(1)
			default:
				t.Errorf("reservation %d: unexpected error %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 2 || exhausted.Load() != 8 {
		t.Fatalf("expected 2 accepted / 8 exhausted, got %d / %d", accepted.Load(), exhausted.Load())
	}
	usage, err := store.Inspect(ctx, budgetID, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if usage.RunReservedRequests != 2 || usage.RunReservedOutputTokens != 1024 || usage.TurnReservedRequests != 2 || usage.TurnReservedOutputTokens != 1024 {
		t.Fatalf("unexpected reservation totals: %#v", usage)
	}

	// A fresh turn and fresh credential identity cannot refill the run budget.
	mustRegisterTurn(t, ctx, store, TurnRegistration{
		BudgetID: budgetID, TurnID: "turn-2", DecisionDigest: "sha256:turn-2",
		MaxRequests: 1, MaxOutputTokens: 512, Deadline: now.Add(10 * time.Minute),
	})
	if _, err := store.Reserve(ctx, ReservationRequest{BudgetID: budgetID, TurnID: "turn-2", RequestID: "later", RequestDigest: "sha256:later", ReservedOutputTokens: 512}); Reason(err) != ReasonExhausted {
		t.Fatalf("new turn refilled run budget: got %v", err)
	}
}

func TestPostgresDuplicateRecoveryAndConflict(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	store.clock = func() time.Time { return now }
	budgetID := fmt.Sprintf("duplicate-%d", now.UnixNano())
	mustRegisterRun(t, ctx, store, RunRegistration{
		BudgetID: budgetID, ClusterID: "cluster-a", NamespaceUID: "ns-a", RunUID: "run-a",
		DecisionDigest: "sha256:decision", RouteDigest: "sha256:route",
		MaxRequests: 2, MaxOutputTokens: 4096, MaxTurns: 1, ParentDeadline: now.Add(time.Hour),
	})
	mustRegisterTurn(t, ctx, store, TurnRegistration{BudgetID: budgetID, TurnID: "turn-1", DecisionDigest: "sha256:turn", MaxRequests: 2, MaxOutputTokens: 4096, Deadline: now.Add(time.Minute)})

	var created atomic.Int64
	var recovered atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reservation, err := store.Reserve(ctx, ReservationRequest{BudgetID: budgetID, TurnID: "turn-1", RequestID: "stable-request", RequestDigest: "sha256:body", ReservedOutputTokens: 512})
			if err != nil {
				t.Errorf("duplicate reserve: %v", err)
				return
			}
			if reservation.Existing {
				recovered.Add(1)
			} else {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 || recovered.Load() != 19 {
		t.Fatalf("expected one creation + 19 recoveries, got %d + %d", created.Load(), recovered.Load())
	}
	if _, err := store.Reserve(ctx, ReservationRequest{BudgetID: budgetID, TurnID: "turn-1", RequestID: "stable-request", RequestDigest: "sha256:changed", ReservedOutputTokens: 512}); Reason(err) != ReasonRequestConflict {
		t.Fatalf("changed duplicate did not conflict: %v", err)
	}
}

func TestPostgresFenceAndNonRefundableReconcile(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	store.clock = func() time.Time { return now }
	budgetID := fmt.Sprintf("fence-%d", now.UnixNano())
	mustRegisterRun(t, ctx, store, RunRegistration{BudgetID: budgetID, ClusterID: "c", NamespaceUID: "n", RunUID: "r", DecisionDigest: "d", RouteDigest: "route", MaxRequests: 2, MaxOutputTokens: 1000, MaxTurns: 1, ParentDeadline: now.Add(time.Hour)})
	mustRegisterTurn(t, ctx, store, TurnRegistration{BudgetID: budgetID, TurnID: "t", DecisionDigest: "td", MaxRequests: 2, MaxOutputTokens: 1000, Deadline: now.Add(time.Minute)})
	reservation, err := store.Reserve(ctx, ReservationRequest{BudgetID: budgetID, TurnID: "t", RequestID: "q", RequestDigest: "digest", ReservedOutputTokens: 600})
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Existing {
		t.Fatal("first reservation marked existing")
	}
	if err := store.MarkInFlight(ctx, budgetID, "t", "q"); err != nil {
		t.Fatal(err)
	}
	if err := store.Reconcile(ctx, budgetID, "t", "q", 100, "terminal", "ok"); err != nil {
		t.Fatal(err)
	}
	usage, err := store.Inspect(ctx, budgetID, "t")
	if err != nil {
		t.Fatal(err)
	}
	if usage.RunReservedOutputTokens != 600 || usage.RunObservedOutputTokens != 100 {
		t.Fatalf("reconcile refunded authority: %#v", usage)
	}
	if err := store.FenceRun(ctx, budgetID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, ReservationRequest{BudgetID: budgetID, TurnID: "t", RequestID: "after-close", RequestDigest: "x", ReservedOutputTokens: 1}); Reason(err) != ReasonClosed {
		t.Fatalf("closed budget accepted reservation: %v", err)
	}
}

func integrationStore(t *testing.T) (*Store, func()) {
	t.Helper()
	url := os.Getenv("CELLN_MODEL_BUDGET_DATABASE_URL")
	if url == "" {
		t.Skip("CELLN_MODEL_BUDGET_DATABASE_URL not set; PostgreSQL integration proof is an explicit test tier")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "002_celln_model_budget.sql"))
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		pool.Close()
		t.Fatalf("apply model-budget migration: %v", err)
	}
	store, err := New(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return store, pool.Close
}

func mustRegisterRun(t *testing.T, ctx context.Context, store *Store, in RunRegistration) {
	t.Helper()
	if err := store.RegisterRun(ctx, in); err != nil {
		t.Fatal(err)
	}
}

func mustRegisterTurn(t *testing.T, ctx context.Context, store *Store, in TurnRegistration) {
	t.Helper()
	if err := store.RegisterTurn(ctx, in); err != nil {
		t.Fatal(err)
	}
}
