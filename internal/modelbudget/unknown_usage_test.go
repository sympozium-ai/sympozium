package modelbudget

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPostgresUnknownUsageRemainsUnknownAcrossRecovery(t *testing.T) {
	first, cleanup := integrationStore(t)
	defer cleanup()
	second, cleanupSecond := integrationStore(t)
	defer cleanupSecond()
	ctx := context.Background()
	id := fmt.Sprintf("unknown-%d", time.Now().UnixNano())
	deadline := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	mustRegisterRun(t, ctx, first, RunRegistration{BudgetID: id, ClusterID: "c", NamespaceUID: "n", RunUID: "r", DecisionDigest: "d", RouteDigest: "route", MaxRequests: 1, MaxOutputTokens: 100, MaxTurns: 1, ParentDeadline: deadline})
	mustRegisterTurn(t, ctx, first, TurnRegistration{BudgetID: id, TurnID: "t", DecisionDigest: "d", MaxRequests: 1, MaxOutputTokens: 100, Deadline: deadline})
	request := ReservationRequest{BudgetID: id, TurnID: "t", RequestID: "q", RequestDigest: "body", ReservedOutputTokens: 100}
	if _, err := first.Reserve(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := first.MarkInFlight(ctx, id, "t", "q"); err != nil {
		t.Fatal(err)
	}
	for _, store := range []*Store{first, second} {
		if err := store.ReconcileUnknown(ctx, id, "t", "q", "uncertain", "lost-response"); err != nil {
			t.Fatal(err)
		}
	}
	recovered, err := second.Reserve(ctx, request)
	if err != nil || recovered.ObservedOutputTokens != nil || !recovered.ProviderAttempted || recovered.State != "uncertain" {
		t.Fatalf("unknown usage became measured zero: %+v %v", recovered, err)
	}
	if err := second.Reconcile(ctx, id, "t", "q", 0, "uncertain", "lost-response"); Reason(err) != ReasonRequestConflict {
		t.Fatalf("zero substituted for unknown: %v", err)
	}
	usage, err := second.Inspect(ctx, id, "t")
	if err != nil || usage.RunReservedRequests != 1 || usage.RunReservedOutputTokens != 100 {
		t.Fatalf("unknown usage refunded: %+v %v", usage, err)
	}
}
