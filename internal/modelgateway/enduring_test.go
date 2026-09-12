package modelgateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
)

func exerciseEnduringLedger(t *testing.T, g *Gateway, issuer *cap.Issuer, original cap.Decision, received <-chan string, key string) {
	t.Helper()
	ctx := context.Background()
	d := original
	d.Run.UID = fmt.Sprintf("enduring-%d", time.Now().UnixNano())
	d.Budget.BudgetID = fixtureDigest("budget/" + d.Run.UID)
	d.Subject.UID = d.Run.UID
	d.Lifecycle = "enduring-initial"
	d.Parent = &cap.ParentBinding{Incarnation: fixtureIncarnation(d.Run.UID)}
	d.Budget.MaxTurns = 2
	d.Budget.RunCap = cap.Cap{Requests: 2, OutputTokens: 1024}
	d.Budget.TurnCap = cap.Cap{Requests: 1, OutputTokens: 512}
	d.Budget.ParentDeadlineUnix = d.Budget.TurnDeadlineUnix
	register := func(decision cap.Decision) error {
		raw, _, err := cap.CanonicalDecision(decision)
		if err != nil {
			return err
		}
		token, err := issuer.Issue(decision, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: decision.Operation})
		if err != nil {
			return err
		}
		return g.Register(ctx, RegistrationRequest{Decision: raw, ExecutionToken: token, ConnectionName: "model"})
	}
	invoke := func(decision cap.Decision) error {
		raw, _, err := cap.CanonicalDecision(decision)
		if err != nil {
			return err
		}
		token, err := issuer.Issue(decision, cap.IssueRequest{Audience: cap.AudienceModelGateway, Operation: "model.invoke"})
		if err != nil {
			return err
		}
		_, err = g.Invoke(ctx, token, InvokeRequest{Decision: raw, RequestID: "same-friendly-request-id", Request: []byte(`{"model":"m","messages":[],"max_tokens":512}`)})
		return err
	}
	if err := register(d); err != nil {
		t.Fatal(err)
	}
	if err := invoke(d); err != nil {
		t.Fatal(err)
	}
	if got := <-received; got != "Bearer "+key {
		t.Fatal("initial turn used foreign credential")
	}
	// The controller/owner closes the completed initial turn before continuing.
	// Budget closure is not itself evidence of actual child teardown.
	cleanup := d
	initialID := turnID(d)
	cleanup.Parent = &cap.ParentBinding{Incarnation: d.Parent.Incarnation, TurnID: &initialID}
	cleanup.Operation = "execution.cleanup"
	cleanupRaw, _, err := cap.CanonicalDecision(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	cleanupToken, err := issuer.Issue(cleanup, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: "execution.cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Close(ctx, cleanupToken, CloseRequest{Decision: cleanupRaw}); err != nil {
		t.Fatal(err)
	}
	initialUsage, err := g.budgets.Inspect(ctx, d.Budget.BudgetID, initialID)
	if err != nil || !initialUsage.TurnClosed || initialUsage.RunClosed {
		t.Fatalf("child cleanup widened to parent: %+v %v", initialUsage, err)
	}
	next := d
	tid := "follow-up"
	next.Parent = &cap.ParentBinding{Incarnation: d.Parent.Incarnation, TurnID: &tid}
	next.Operation = "execution.turn"
	next.Lifecycle = "enduring-turn"
	next.RequestDigest = fixtureDigest("follow-up-input")
	if err := register(next); err != nil {
		t.Fatal(err)
	}
	if err := register(next); err != nil {
		t.Fatalf("turn registration recovery: %v", err)
	}
	if err := invoke(next); err != nil {
		t.Fatal(err)
	}
	if got := <-received; got != "Bearer "+key {
		t.Fatal("follow-up used foreign credential")
	}
	usage, err := g.budgets.Inspect(ctx, d.Budget.BudgetID, tid)
	if err != nil || usage.RunReservedRequests != 2 || usage.RunReservedOutputTokens != 1024 || usage.RunObservedOutputTokens != 2 || usage.TurnReservedRequests != 1 {
		t.Fatalf("initial/follow-up accounting drift: %+v %v", usage, err)
	}
	if err := invoke(d); err == nil {
		t.Fatal("closed initial turn restarted model work")
	}
	third := next
	thirdID := "third"
	third.Parent = &cap.ParentBinding{Incarnation: d.Parent.Incarnation, TurnID: &thirdID}
	if err := register(third); modelbudget.Reason(err) != modelbudget.ReasonExhausted {
		t.Fatalf("initial task did not consume maxTurns: %v", err)
	}
	next.Budget.RunCap.Requests++
	if err := register(next); err == nil {
		t.Fatal("fresh turn token topped up original parent")
	}
}
