package modelbudget

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresDispatchClaimHonoursFenceAndIsUnique(t *testing.T) {
	first, cleanup := integrationStore(t)
	defer cleanup()
	second, cleanupSecond := integrationStore(t)
	defer cleanupSecond()
	for _, scenario := range []string{"run-fenced", "turn-fenced", "expired", "concurrent"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC()
			deadline := now.Add(time.Hour).Truncate(time.Microsecond)
			id := fmt.Sprintf("dispatch-%s-%d", scenario, now.UnixNano())
			mustRegisterRun(t, ctx, first, RunRegistration{BudgetID: id, ClusterID: "c", NamespaceUID: "n", RunUID: "r", DecisionDigest: "d", RouteDigest: "route", MaxRequests: 1, MaxOutputTokens: 512, MaxTurns: 1, ParentDeadline: deadline})
			mustRegisterTurn(t, ctx, first, TurnRegistration{BudgetID: id, TurnID: "t", DecisionDigest: "d", MaxRequests: 1, MaxOutputTokens: 512, Deadline: deadline})
			if _, err := first.Reserve(ctx, ReservationRequest{BudgetID: id, TurnID: "t", RequestID: "q", RequestDigest: "d", ReservedOutputTokens: 512}); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "run-fenced":
				if err := first.FenceRun(ctx, id); err != nil {
					t.Fatal(err)
				}
			case "turn-fenced":
				if err := first.FenceTurn(ctx, id, "t"); err != nil {
					t.Fatal(err)
				}
			case "expired":
				first.clock = func() time.Time { return deadline }
				defer func() { first.clock = time.Now }()
			}
			if scenario != "concurrent" {
				expected := ReasonClosed
				if scenario == "expired" {
					expected = ReasonDeadline
				}
				if err := first.MarkInFlight(ctx, id, "t", "q"); Reason(err) != expected {
					t.Fatalf("expected %s got %v", expected, err)
				}
			} else {
				var accepted atomic.Int32
				var wg sync.WaitGroup
				for i := 0; i < 20; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						store := first
						if i%2 == 1 {
							store = second
						}
						err := store.MarkInFlight(ctx, id, "t", "q")
						if err == nil {
							accepted.Add(1)
						} else if Reason(err) != ReasonRequestConflict {
							t.Error(err)
						}
					}(i)
				}
				wg.Wait()
				if accepted.Load() != 1 {
					t.Fatalf("dispatch claims=%d want 1", accepted.Load())
				}
			}
			usage, err := first.Inspect(ctx, id, "t")
			if err != nil {
				t.Fatal(err)
			}
			if usage.RunReservedRequests != 1 || usage.RunReservedOutputTokens != 512 {
				t.Fatalf("dispatch refunded reservation: %+v", usage)
			}
		})
	}
}
