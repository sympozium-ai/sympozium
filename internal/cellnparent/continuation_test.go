package cellnparent

import (
	"context"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func enduringRun() *api.AgentRun {
	return &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "celln-agent-abc", Namespace: "tenant", UID: types.UID("uid-1"), Labels: map[string]string{"sympozium.ai/instance": "celln-agent"}},
		Spec: api.AgentRunSpec{
			AgentRef: "celln-agent", Backend: "celln", ExecutionLifecycle: "enduring",
			Task:           api.NewStringTask("Remember the word saffron."),
			Enduring:       &api.EnduringRunSpec{LeaseSeconds: 3600, MaxTurns: 8, MaxModelRequests: 24, MaxOutputTokens: 4096},
			CellnSelection: &api.CellnCatalogueSelection{RuntimeRef: "celln-native", ToolRefs: []api.CellnCatalogueToolRef{}},
		},
		Status: api.AgentRunStatus{CellnParent: &api.CellnParentStatus{InitialTurn: &api.CellnParentTurnStatus{Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "Noted: saffron."}}}},
	}
}

func TestTranscriptOrdersCommittedExchangesAndSkipsFailures(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	run := enduringRun()
	store := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "t2", Namespace: "tenant", CreationTimestamp: metav1.NewTime(metav1.Now().Add(2e9))}, Spec: api.AgentRunTurnSpec{RunName: run.Name, RunUID: "uid-1", Message: "and the number seven"}, Status: api.AgentRunTurnStatus{Execution: &api.CellnParentTurnStatus{Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "Seven, noted."}}}},
		&api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "tenant", CreationTimestamp: metav1.NewTime(metav1.Now().Add(1e9))}, Spec: api.AgentRunTurnSpec{RunName: run.Name, RunUID: "uid-1", Message: "what colour?"}, Status: api.AgentRunTurnStatus{Execution: &api.CellnParentTurnStatus{Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "Violet."}}}},
		&api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "t3", Namespace: "tenant", CreationTimestamp: metav1.NewTime(metav1.Now().Add(3e9))}, Spec: api.AgentRunTurnSpec{RunName: run.Name, RunUID: "uid-1", Message: "failed one"}, Status: api.AgentRunTurnStatus{Execution: &api.CellnParentTurnStatus{Result: &api.CellnParentTurnResult{Succeeded: false, Answer: "tool crashed"}}}},
		&api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "tenant"}, Spec: api.AgentRunTurnSpec{RunName: "other-run", RunUID: "uid-9", Message: "not ours"}, Status: api.AgentRunTurnStatus{Execution: &api.CellnParentTurnStatus{Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "x"}}}},
	).Build()
	got, err := Transcript(context.Background(), store, run, LegacySeedBytes)
	if err != nil {
		t.Fatal(err)
	}
	want := []api.ConversationExchange{
		{User: "Remember the word saffron.", Assistant: "Noted: saffron."},
		{User: "what colour?", Assistant: "Violet."},
		{User: "and the number seven", Assistant: "Seven, noted."},
	}
	if len(got) != len(want) {
		t.Fatalf("transcript: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("exchange %d: %+v want %+v", i, got[i], want[i])
		}
	}
	// A continued run's own seed comes first.
	run.Spec.Conversation = &api.ConversationSpec{ContinuesFrom: "earlier", Depth: 1, Seed: []api.ConversationExchange{{User: "first ever", Assistant: "hello"}}}
	got, _ = Transcript(context.Background(), store, run, LegacySeedBytes)
	if len(got) != 4 || got[0].User != "first ever" {
		t.Fatalf("seed must lead the transcript: %+v", got)
	}
}

func TestTrimSeedKeepsTheNewestThatFit(t *testing.T) {
	var long []api.ConversationExchange
	for i := 0; i < 20; i++ {
		long = append(long, api.ConversationExchange{User: strings.Repeat("u", 100), Assistant: strings.Repeat("a", 100)})
	}
	long = append(long, api.ConversationExchange{User: "last", Assistant: "kept"})
	trimmed := TrimSeed(long, LegacySeedBytes)
	if !SeedFits(trimmed, LegacySeedBytes) || trimmed[len(trimmed)-1].User != "last" || len(trimmed) >= 16 {
		t.Fatalf("trim must keep the newest within bounds: %d %+v", len(trimmed), trimmed[len(trimmed)-1])
	}
	if got := TrimSeed([]api.ConversationExchange{{User: " ", Assistant: "x"}, {User: "ok", Assistant: "y\x00"}, {User: "fine", Assistant: "z"}}, LegacySeedBytes); len(got) != 1 || got[0].User != "fine" {
		t.Fatalf("blank or NUL exchanges are dropped: %+v", got)
	}
}

func TestContinuationCarriesSpecSeedAndDepth(t *testing.T) {
	previous := enduringRun()
	seed := []api.ConversationExchange{{User: "Remember the word saffron.", Assistant: "Noted: saffron."}}
	next, err := Continuation(previous, seed, ContinuationOriginAutomatic, LegacySeedBytes)
	if err != nil {
		t.Fatal(err)
	}
	if next.GenerateName != "celln-agent-" || next.Namespace != "tenant" || next.Labels["sympozium.ai/instance"] != "celln-agent" || next.Annotations[ContinuedFromAnnotation] != "celln-agent-abc" {
		t.Fatalf("metadata: %+v", next.ObjectMeta)
	}
	if next.Spec.Task.GetPrompt() != ResumeMessage || next.Spec.Conversation == nil || next.Spec.Conversation.ContinuesFrom != "celln-agent-abc" || next.Spec.Conversation.Depth != 1 || next.Spec.Conversation.Continuation != "automatic" || len(next.Spec.Conversation.Seed) != 1 || next.Spec.ExecutionLifecycle != "enduring" || next.Spec.Enduring.MaxTurns != 8 || next.Spec.CellnSelection.RuntimeRef != "celln-native" {
		t.Fatalf("spec: %+v", next.Spec)
	}
	if !next.Spec.ContinuesOnLoss() {
		t.Fatal("a continued run continues on loss like its predecessor")
	}
	// Depth accumulates and is bounded; a run told not to continue stays so.
	next.Name = "celln-agent-def"
	next.Spec.Conversation.Depth = api.MaxContinuationDepth
	if _, err := Continuation(next, seed, ContinuationOriginAutomatic, LegacySeedBytes); err == nil {
		t.Fatal("depth bound must stop the chain")
	}
	previous.Spec.Conversation = &api.ConversationSpec{Continuation: "none"}
	if !previous.Spec.ContinuesOnLoss() == false {
		t.Fatal("continuation none must be honoured")
	}
	oneShot := enduringRun()
	oneShot.Spec.ExecutionLifecycle = "one-shot"
	if _, err := Continuation(oneShot, nil, ContinuationOriginAutomatic, LegacySeedBytes); err == nil {
		t.Fatal("only enduring runs continue")
	}
	if _, err := Continuation(enduringRun(), []api.ConversationExchange{{User: strings.Repeat("u", 2000), Assistant: strings.Repeat("a", 2000)}}, ContinuationOriginAutomatic, LegacySeedBytes); err == nil {
		t.Fatal("an oversized seed is refused, not trimmed silently here")
	}
	if _, err := Continuation(enduringRun(), seed, "", LegacySeedBytes); err == nil {
		t.Fatal("a continuation must name its origin")
	}
}

func TestContinuationRecordsOrigin(t *testing.T) {
	seed := []api.ConversationExchange{{User: "Remember the word saffron.", Assistant: "Noted: saffron."}}
	automatic, err := Continuation(enduringRun(), seed, ContinuationOriginAutomatic, LegacySeedBytes)
	if err != nil {
		t.Fatal(err)
	}
	requested, err := Continuation(enduringRun(), seed, ContinuationOriginRequested, LegacySeedBytes)
	if err != nil {
		t.Fatal(err)
	}
	if automatic.Annotations[ContinuationOriginAnnotation] != ContinuationOriginAutomatic || !IsAutomaticContinuation(automatic) {
		t.Fatalf("automatic origin: %+v", automatic.Annotations)
	}
	if requested.Annotations[ContinuationOriginAnnotation] != ContinuationOriginRequested || IsAutomaticContinuation(requested) {
		t.Fatalf("requested origin: %+v", requested.Annotations)
	}
	if IsAutomaticContinuation(enduringRun()) {
		t.Fatal("an original run is not a continuation")
	}
	// A continuation from before origins were recorded is treated as automatic
	// so a loop already running stops on upgrade.
	legacy := automatic.DeepCopy()
	delete(legacy.Annotations, ContinuationOriginAnnotation)
	if !IsAutomaticContinuation(legacy) {
		t.Fatal("an unmarked continuation must count as automatic")
	}
}

func TestAutomaticContinuationStalledOnlyWithoutFollowUp(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	ctx := context.Background()
	seed := []api.ConversationExchange{{User: "Remember the word saffron.", Assistant: "Noted: saffron."}}
	continuation := func(origin string) *api.AgentRun {
		next, err := Continuation(enduringRun(), seed, origin, LegacySeedBytes)
		if err != nil {
			t.Fatal(err)
		}
		next.Name, next.UID = "celln-agent-cont", types.UID("uid-cont")
		next.Status.CellnParent = &api.CellnParentStatus{InitialTurn: &api.CellnParentTurnStatus{Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "We were on saffron."}}}
		return next
	}
	turn := func(name, runName, runUID string, result *api.CellnParentTurnResult) *api.AgentRunTurn {
		return &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tenant"}, Spec: api.AgentRunTurnSpec{RunName: runName, RunUID: runUID, Message: "next"}, Status: api.AgentRunTurnStatus{Execution: &api.CellnParentTurnStatus{Attempted: result != nil, Result: result}}}
	}
	cases := []struct {
		name    string
		run     *api.AgentRun
		objects []*api.AgentRunTurn
		stalled bool
	}{
		{name: "original run is continued", run: enduringRun(), stalled: false},
		{name: "requested restart is not withheld", run: continuation(ContinuationOriginRequested), stalled: false},
		{name: "automatic continuation lost before any follow-up", run: continuation(ContinuationOriginAutomatic), stalled: true},
		{name: "only other runs' turns and uncommitted turns", run: continuation(ContinuationOriginAutomatic), objects: []*api.AgentRunTurn{
			turn("other", "celln-agent-abc", "uid-1", &api.CellnParentTurnResult{Succeeded: true, Answer: "x"}),
			turn("reused-name", "celln-agent-cont", "uid-old", &api.CellnParentTurnResult{Succeeded: true, Answer: "x"}),
			turn("pending", "celln-agent-cont", "uid-cont", nil),
		}, stalled: true},
		{name: "committed follow-up allows one more continuation", run: continuation(ContinuationOriginAutomatic), objects: []*api.AgentRunTurn{
			turn("mine", "celln-agent-cont", "uid-cont", &api.CellnParentTurnResult{Succeeded: true, Answer: "Seven."}),
		}, stalled: false},
		{name: "failed follow-up is still progress", run: continuation(ContinuationOriginAutomatic), objects: []*api.AgentRunTurn{
			turn("mine", "celln-agent-cont", "uid-cont", &api.CellnParentTurnResult{Succeeded: false, Answer: "tool crashed"}),
		}, stalled: false},
	}
	accepted := continuation(ContinuationOriginAutomatic)
	accepted.Status.CellnParent.AcceptedTurns = 1
	cases = append(cases, struct {
		name    string
		run     *api.AgentRun
		objects []*api.AgentRunTurn
		stalled bool
	}{name: "accepted follow-up allows one more continuation", run: accepted, stalled: false})
	// The seeded resume turn is never follow-up work, whatever its outcome: a
	// continuation whose resume turn FAILED and was then lost added nothing,
	// so it must not be re-created. It stays open to the user while its parent
	// is Ready (TurnReadiness); the message they send is the progress.
	failedResume := continuation(ContinuationOriginAutomatic)
	failedResume.Status.CellnParent.InitialTurn.Result = &api.CellnParentTurnResult{Succeeded: false, Answer: "Turn failed; no result committed"}
	failedResumeThenMessage := failedResume.DeepCopy()
	failedResumeThenMessage.Status.CellnParent.AcceptedTurns = 1
	for _, extra := range []struct {
		name    string
		run     *api.AgentRun
		stalled bool
	}{
		{"failed resume turn is not follow-up work", failedResume, true},
		{"message accepted after a failed resume turn is progress", failedResumeThenMessage, false},
	} {
		cases = append(cases, struct {
			name    string
			run     *api.AgentRun
			objects []*api.AgentRunTurn
			stalled bool
		}{name: extra.name, run: extra.run, stalled: extra.stalled})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			for _, object := range tc.objects {
				builder = builder.WithObjects(object)
			}
			got, err := AutomaticContinuationStalled(ctx, builder.Build(), tc.run)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.stalled {
				t.Fatalf("stalled=%t want %t", got, tc.stalled)
			}
		})
	}
}
