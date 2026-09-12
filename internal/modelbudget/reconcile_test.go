package modelbudget

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func TestPostgresConcurrentReconcileAcrossPools(t *testing.T) {
	first, closeFirst := integrationStore(t)
	defer closeFirst()
	second, closeSecond := integrationStore(t)
	defer closeSecond()
	ctx := context.Background()
	budget := fmt.Sprintf("reconcile-race-%d", time.Now().UnixNano())
	deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	mustRegisterRun(t, ctx, first, RunRegistration{BudgetID: budget, ClusterID: "c", NamespaceUID: "n", RunUID: "r", DecisionDigest: "d", RouteDigest: "route", MaxRequests: 2, MaxOutputTokens: 1024, MaxTurns: 1, ParentDeadline: deadline})
	mustRegisterTurn(t, ctx, first, TurnRegistration{BudgetID: budget, TurnID: "t", DecisionDigest: "d", MaxRequests: 2, MaxOutputTokens: 1024, Deadline: deadline})
	_, err := first.Reserve(ctx, ReservationRequest{BudgetID: budget, TurnID: "t", RequestID: "q", RequestDigest: "body", ReservedOutputTokens: 512})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s := first
			if i%2 == 1 {
				s = second
			}
			if err := s.Reconcile(ctx, budget, "t", "q", 100, "terminal", "ok"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	usage, err := second.Inspect(ctx, budget, "t")
	if err != nil {
		t.Fatal(err)
	}
	if usage.RunObservedOutputTokens != 100 || usage.TurnObservedOutputTokens != 100 || usage.RunReservedOutputTokens != 512 {
		t.Fatalf("duplicate reconciliation changed totals: %+v", usage)
	}
	if err := second.Reconcile(ctx, budget, "t", "q", 101, "terminal", "ok"); Reason(err) != ReasonRequestConflict {
		t.Fatalf("changed reconciliation: %v", err)
	}
	// Addition-based checks wrap 512 + MaxInt64 into a negative integer.
	if _, err := first.Reserve(ctx, ReservationRequest{BudgetID: budget, TurnID: "t", RequestID: "overflow", RequestDigest: "other", ReservedOutputTokens: math.MaxInt64}); Reason(err) != ReasonExhausted {
		t.Fatalf("overflow must exhaust, got %v", err)
	}
}
