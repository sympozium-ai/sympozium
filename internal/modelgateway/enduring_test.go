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
	closeUnregistered := func(decision cap.Decision) error {
		decision.Operation = "execution.cleanup"
		raw, _, err := cap.CanonicalDecision(decision)
		if err != nil {
			return err
		}
		token, err := issuer.Issue(decision, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: "execution.cleanup"})
		if err != nil {
			return err
		}
		return g.Close(ctx, token, CloseRequest{Decision: raw})
	}
	widened := third
	widened.Budget.RunCap.Requests++
	if err := closeUnregistered(widened); Reason(err) != ReasonForbidden {
		t.Fatalf("cleanup accepted changed parent authority: %v", err)
	}
	if _, err := g.budgets.Inspect(ctx, d.Budget.BudgetID, thirdID); modelbudget.Reason(err) != modelbudget.ReasonNotFound {
		t.Fatal("unauthorised cleanup published a turn fence")
	}
	if err := closeUnregistered(third); err != nil {
		t.Fatalf("unregistered turn cleanup: %v", err)
	}
	if err := closeUnregistered(third); err != nil {
		t.Fatalf("unregistered turn cleanup replay: %v", err)
	}
	fenced, err := g.budgets.Inspect(ctx, d.Budget.BudgetID, thirdID)
	if err != nil || !fenced.TurnClosed || fenced.RunClosed || fenced.RunReservedRequests != 2 || fenced.RunReservedOutputTokens != 1024 || fenced.TurnReservedRequests != 0 {
		t.Fatalf("no-start fence changed original accounting: %+v %v", fenced, err)
	}
	if err := register(third); modelbudget.Reason(err) != modelbudget.ReasonRegisterConflict {
		t.Fatalf("fenced registration restarted: %v", err)
	}
	next.Budget.RunCap.Requests++
	if err := register(next); err == nil {
		t.Fatal("fresh turn token topped up original parent")
	}

	// A run whose registration never happened (refused, or interrupted before
	// its authority was published) must still be closable, or its cleanup and
	// deletion hang; the fence then refuses any late registration.
	never := d
	never.Run.UID = fmt.Sprintf("never-registered-%d", time.Now().UnixNano())
	never.Budget.BudgetID = fixtureDigest("budget/" + never.Run.UID)
	never.Subject.UID = never.Run.UID
	never.Parent = &cap.ParentBinding{Incarnation: fixtureIncarnation(never.Run.UID)}
	if err := closeUnregistered(never); err != nil {
		t.Fatalf("unregistered run cleanup: %v", err)
	}
	if err := closeUnregistered(never); err != nil {
		t.Fatalf("unregistered run cleanup replay: %v", err)
	}
	if err := register(never); modelbudget.Reason(err) != modelbudget.ReasonRegisterConflict {
		t.Fatalf("fenced run registered late: %v", err)
	}
}
