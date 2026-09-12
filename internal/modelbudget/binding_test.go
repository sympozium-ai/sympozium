package modelbudget

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPostgresReservationBindingBeforeChargeOrRecovery(t *testing.T) {
	store, cleanup := integrationStore(t)
	defer cleanup()
	other, cleanupOther := integrationStore(t)
	defer cleanupOther()
	ctx := context.Background()
	id := fmt.Sprintf("binding-%d", time.Now().UnixNano())
	deadline := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	mustRegisterRun(t, ctx, store, RunRegistration{BudgetID: id, ClusterID: "cluster", NamespaceUID: "namespace", RunUID: "run", DecisionDigest: "initial", RouteDigest: "route", MaxRequests: 2, MaxOutputTokens: 100, MaxTurns: 2, ParentDeadline: deadline})
	mustRegisterTurn(t, ctx, store, TurnRegistration{BudgetID: id, TurnID: "turn", DecisionDigest: "turn-decision", MaxRequests: 2, MaxOutputTokens: 100, Deadline: deadline})
	binding := ReservationBinding{ClusterID: "cluster", NamespaceUID: "namespace", RunUID: "run", RouteDigest: "route", TurnDecisionDigest: "turn-decision"}
	request := ReservationRequest{BudgetID: id, TurnID: "turn", RequestID: "request", RequestDigest: "body", ReservedOutputTokens: 50}
	for _, recoverExisting := range []bool{false, true} {
		if recoverExisting {
			if _, err := store.ReserveBound(ctx, request, binding); err != nil {
				t.Fatal(err)
			}
		}
		for _, field := range []string{"cluster", "namespace", "run", "route", "decision", "missing"} {
			bad := binding
			switch field {
			case "cluster":
				bad.ClusterID = "foreign"
			case "namespace":
				bad.NamespaceUID = "foreign"
			case "run":
				bad.RunUID = "foreign"
			case "route":
				bad.RouteDigest = "foreign"
			case "decision":
				bad.TurnDecisionDigest = "foreign"
			case "missing":
				bad = ReservationBinding{}
			}
			if _, err := other.ReserveBound(ctx, request, bad); Reason(err) != ReasonRegisterConflict {
				t.Fatalf("recovery=%v field=%s: %v", recoverExisting, field, err)
			}
		}
		usage, err := other.Inspect(ctx, id, "turn")
		if err != nil {
			t.Fatal(err)
		}
		var expected int64
		if recoverExisting {
			expected = 1
		}
		if usage.RunReservedRequests != expected || usage.RunReservedOutputTokens != 50*expected {
			t.Fatalf("foreign binding changed allowance: %+v", usage)
		}
	}
	recovered, err := other.ReserveBound(ctx, request, binding)
	if err != nil || !recovered.Existing {
		t.Fatalf("identical bound recovery failed: %+v %v", recovered, err)
	}
}
