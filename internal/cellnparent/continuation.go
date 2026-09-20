package cellnparent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A continued conversation: when an enduring parent's live context is lost,
// or an operator asks for a restart, a new run takes over on any node with
// capacity, seeded with the exchanges recorded so far. The seed is text the
// model already answered, never instructions; it travels in the new run's
// spec so the previous run can be gone by the time the parent starts.

// ResumeMessage is the first turn of a continued run: it makes the new
// parent show its memory rather than silently pretending nothing happened.
const ResumeMessage = "This conversation continues on a new node. In one short sentence, say what we were discussing so far; do not use tools."

// ContinuedFromAnnotation marks a run created to continue another.
const ContinuedFromAnnotation = "sympozium.ai/continued-from"

// ContinuationOriginAnnotation records who created a continuation: the
// controller after a lost parent (automatic) or a user through the API
// (requested). Only the controller's own continuations are held to the
// no-progress rule in AutomaticContinuationStalled.
const (
	ContinuationOriginAnnotation = "sympozium.ai/continuation-origin"
	ContinuationOriginAutomatic  = "automatic"
	ContinuationOriginRequested  = "requested"
)

// StalledContinuationMessage is the stable failure for an automatic
// continuation whose parent was lost before it did any work of its own.
const StalledContinuationMessage = "Celln parent lost again on its continuation; not continuing automatically — start a new conversation"

// The parent carries history and the next message inside one bounded worker
// task; a seed must leave room for that message. These mirror the host's own
// bounds so a plan the controller builds is never refused there.
//
// The task bound depends on the starter package the fleet runs: a current
// package takes 16384 bytes, an older one 2048, and an older guest refuses a
// larger seed outright, failing parent creation. The host cannot tell a
// guest's age before its first turn, so the only evidence is the runtime
// profile the installer published from that package's catalogue
// (limits.taskBytes). Anything else gets the old budget: a short memory is
// recoverable, a parent that cannot be created is not.
const (
	maxSeedExchanges  = 16
	seedHeadroomBytes = 512
	// LegacySeedBytes is the seed budget every starter package accepts.
	LegacySeedBytes = 2048 - seedHeadroomBytes
)

// SeedBudget is the most bytes an encoded seed may take for a worker whose
// runtime profile reports taskBytes. Only a profile that states the current
// task bound (or more) widens it.
func SeedBudget(taskBytes int64) int {
	if taskBytes >= api.MaxWorkerTaskBytes {
		return int(taskBytes) - seedHeadroomBytes
	}
	return LegacySeedBytes
}

// SeedBudgetFor resolves the seed budget for a run that continues run: the
// taskBytes of the cluster runtime profile behind run's namespaced runtime
// wrapper, at the revision the wrapper pins. A run without a shared-profile
// wrapper, or any lookup that fails, gets LegacySeedBytes.
func SeedBudgetFor(ctx context.Context, reader client.Reader, run *api.AgentRun) int {
	name := ""
	if run.Spec.CellnSelection != nil {
		name = run.Spec.CellnSelection.RuntimeRef
	}
	if name == "" && run.Spec.AgentRef != "" {
		var agent api.Agent
		if reader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.AgentRef}, &agent) == nil {
			name = agent.Spec.RuntimeRef
		}
	}
	if name == "" {
		return LegacySeedBytes
	}
	var wrapper api.AgentRuntime
	if reader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, &wrapper) != nil || wrapper.Spec.CellnProfileRef == nil {
		return LegacySeedBytes
	}
	var profile api.CellnRuntimeProfile
	if reader.Get(ctx, client.ObjectKey{Name: wrapper.Spec.CellnProfileRef.Name}, &profile) != nil || profile.Spec.Revision != wrapper.Spec.CellnProfileRef.Revision {
		return LegacySeedBytes
	}
	return SeedBudget(profile.Spec.Limits.TaskBytes)
}

// Transcript gathers the committed exchanges of a run, oldest first: the
// seed it started with, its initial turn, then every succeeded follow-up
// turn. Failed turns are not part of the conversation's memory. It is trimmed
// to budget (see SeedBudget).
func Transcript(ctx context.Context, reader client.Reader, run *api.AgentRun, budget int) ([]api.ConversationExchange, error) {
	var exchanges []api.ConversationExchange
	if run.Spec.Conversation != nil {
		exchanges = append(exchanges, run.Spec.Conversation.Seed...)
	}
	if run.Status.CellnParent != nil && run.Status.CellnParent.InitialTurn != nil && run.Status.CellnParent.InitialTurn.Result != nil && run.Status.CellnParent.InitialTurn.Result.Succeeded && run.Spec.Task != nil && run.Spec.Task.IsString() {
		exchanges = append(exchanges, api.ConversationExchange{User: run.Spec.Task.GetPrompt(), Assistant: run.Status.CellnParent.InitialTurn.Result.Answer})
	}
	var list api.AgentRunTurnList
	if err := reader.List(ctx, &list, client.InNamespace(run.Namespace)); err != nil {
		return nil, err
	}
	turns := make([]api.AgentRunTurn, 0, len(list.Items))
	for _, turn := range list.Items {
		if turn.Spec.RunName == run.Name && turn.Spec.RunUID == string(run.UID) && turn.Status.Execution != nil && turn.Status.Execution.Result != nil && turn.Status.Execution.Result.Succeeded {
			turns = append(turns, turn)
		}
	}
	sort.Slice(turns, func(i, j int) bool {
		if turns[i].CreationTimestamp.Equal(&turns[j].CreationTimestamp) {
			return turns[i].Name < turns[j].Name
		}
		return turns[i].CreationTimestamp.Before(&turns[j].CreationTimestamp)
	})
	for _, turn := range turns {
		exchanges = append(exchanges, api.ConversationExchange{User: turn.Spec.Message, Assistant: turn.Status.Execution.Result.Answer})
	}
	return TrimSeed(exchanges, budget), nil
}

// TrimSeed keeps the newest exchanges that fit the host's seed bounds. A
// conversation longer than the bound keeps its recent memory, not its start.
func TrimSeed(exchanges []api.ConversationExchange, budget int) []api.ConversationExchange {
	kept := make([]api.ConversationExchange, 0, len(exchanges))
	for _, exchange := range exchanges {
		if strings.TrimSpace(exchange.User) == "" || strings.ContainsRune(exchange.User, 0) || strings.ContainsRune(exchange.Assistant, 0) {
			continue
		}
		kept = append(kept, exchange)
	}
	for len(kept) > 0 && !SeedFits(kept, budget) {
		kept = kept[1:]
	}
	return kept
}

// SeedFits reports whether a seed is within the host's bounds for budget.
func SeedFits(exchanges []api.ConversationExchange, budget int) bool {
	if len(exchanges) > maxSeedExchanges {
		return false
	}
	raw, err := json.Marshal(map[string]any{"history": exchanges, "message": ""})
	return err == nil && len(raw) <= budget
}

// IsAutomaticContinuation reports whether run was created by the controller
// to continue a lost conversation. A continuation without a recorded origin
// (created before the origin annotation existed) counts as automatic, so a
// loop already running stops on upgrade; only an explicit API restart is
// exempt.
func IsAutomaticContinuation(run *api.AgentRun) bool {
	if run.Spec.Conversation == nil || run.Spec.Conversation.ContinuesFrom == "" {
		return false
	}
	return run.Annotations[ContinuationOriginAnnotation] != ContinuationOriginRequested
}

// AutomaticContinuationStalled reports whether a lost run must not be
// continued automatically again.
//
// Rule: an automatic continuation is itself continued only if it made
// progress of its own, i.e. accepted or committed at least one follow-up
// turn beyond its seeded resume turn. One lost before that would otherwise be
// re-created forever, each copy only repeating the resume message and
// burning model requests without adding to the conversation. A continuation
// that did carry the conversation on may be continued again (still bounded by
// MaxContinuationDepth). Original runs and user-requested restarts are never
// withheld here.
func AutomaticContinuationStalled(ctx context.Context, reader client.Reader, run *api.AgentRun) (bool, error) {
	if !IsAutomaticContinuation(run) {
		return false, nil
	}
	if run.Status.CellnParent != nil && run.Status.CellnParent.AcceptedTurns > 0 {
		return false, nil
	}
	var list api.AgentRunTurnList
	if err := reader.List(ctx, &list, client.InNamespace(run.Namespace)); err != nil {
		return false, err
	}
	for _, turn := range list.Items {
		if turn.Spec.RunName == run.Name && turn.Spec.RunUID == string(run.UID) && turn.Status.Execution != nil && turn.Status.Execution.Result != nil {
			return false, nil
		}
	}
	return true, nil
}

// Continuation builds the run that carries a conversation on from previous:
// the same Agent, model, selection and limits, the resume message as its
// initial turn, and the transcript as its seed. origin (automatic or
// requested) is recorded in ContinuationOriginAnnotation. budget is the seed
// budget of the fleet the new parent starts on. It is not created here.
func Continuation(previous *api.AgentRun, seed []api.ConversationExchange, origin string, budget int) (*api.AgentRun, error) {
	if origin != ContinuationOriginAutomatic && origin != ContinuationOriginRequested {
		return nil, fmt.Errorf("unknown continuation origin %q", origin)
	}
	if previous.Spec.ExecutionLifecycle != "enduring" || previous.Spec.Enduring == nil {
		return nil, fmt.Errorf("only an enduring run can be continued")
	}
	depth := int32(1)
	continuation := "automatic"
	if previous.Spec.Conversation != nil {
		depth = previous.Spec.Conversation.Depth + 1
		if previous.Spec.Conversation.Continuation != "" {
			continuation = previous.Spec.Conversation.Continuation
		}
	}
	if depth > api.MaxContinuationDepth {
		return nil, fmt.Errorf("conversation continued %d times; not continuing again", depth-1)
	}
	if !SeedFits(seed, budget) {
		return nil, fmt.Errorf("seed exceeds the parent's context bound")
	}
	spec := previous.Spec.DeepCopy()
	spec.Task = api.NewStringTask(ResumeMessage)
	spec.Conversation = &api.ConversationSpec{Continuation: continuation, ContinuesFrom: previous.Name, Depth: depth, Seed: seed}
	labels := map[string]string{}
	for key, value := range previous.Labels {
		labels[key] = value
	}
	annotations := map[string]string{ContinuedFromAnnotation: previous.Name, ContinuationOriginAnnotation: origin}
	return &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{GenerateName: previous.Spec.AgentRef + "-", Namespace: previous.Namespace, Labels: labels, Annotations: annotations},
		Spec:       *spec,
	}, nil
}
