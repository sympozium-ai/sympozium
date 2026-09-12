package modelgateway

import (
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"testing"
)

func TestTurnRetainsOriginalAuthority(t *testing.T) {
	turn := "turn-2"
	original := cap.Decision{Operation: "execution.start", Lifecycle: "enduring-initial", ClusterID: "cluster", Run: cap.RunBinding{UID: "run"}, Parent: &cap.ParentBinding{Incarnation: "parent"}, Budget: cap.BudgetBinding{BudgetID: "budget", RunCap: cap.Cap{Requests: 3, OutputTokens: 1536}, TurnCap: cap.Cap{Requests: 1, OutputTokens: 512}, MaxTurns: 3, ParentDeadlineUnix: 300, TurnDeadlineUnix: 100}}
	next := original
	next.Operation = "execution.turn"
	next.Lifecycle = "enduring-turn"
	next.Parent = &cap.ParentBinding{Incarnation: "parent", TurnID: &turn}
	next.Budget.TurnDeadlineUnix = 200
	if err := retainRunAuthority(original, next); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*cap.Decision){
		func(d *cap.Decision) { d.Budget.RunCap.Requests++ },
		func(d *cap.Decision) { d.Budget.ParentDeadlineUnix++ },
		func(d *cap.Decision) { d.Run.UID = "other" },
		func(d *cap.Decision) { d.Budget.BudgetID = "new" },
		func(d *cap.Decision) { d.Runtime.Revision = "other" },
		func(d *cap.Decision) { d.Route.Model = "other" },
		func(d *cap.Decision) { d.Parent = &cap.ParentBinding{Incarnation: "other", TurnID: &turn} },
	} {
		changed := next
		mutate(&changed)
		if err := retainRunAuthority(original, changed); err == nil {
			t.Fatal("turn widened or changed original authority")
		}
	}
}
