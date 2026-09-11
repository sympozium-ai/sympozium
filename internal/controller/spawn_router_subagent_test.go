package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
	"github.com/sympozium-ai/sympozium/internal/orchestrator"
)

// subagentBatchFixture wires a parent run holding the subagents skill, its
// Agent, and a live spawner, so handleSubagentRequest spawns children through
// the same code path production uses — including the sr.pending store sites.
func subagentBatchFixture(t *testing.T) (*SpawnRouter, *recordingEventBus) {
	t.Helper()

	parentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "parent-run", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   "my-agent",
			SessionKey: "sess",
			Skills:     []sympoziumv1alpha1.SkillRef{{SkillPackRef: "subagents"}},
		},
	}
	inst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
	}

	sr := newTestSpawnRouter(t, parentRun, inst)
	bus := &recordingEventBus{}
	sr.EventBus = bus
	sr.spawner = orchestrator.Spawner{Client: sr.Client, Log: logr.Discard()}
	return sr, bus
}

// batchChildren returns the spawned child run names for a batch ordered by
// task index. Children not yet spawned (sequential) have an empty name.
func batchChildren(t *testing.T, sr *SpawnRouter, batchID string) []string {
	t.Helper()
	val, ok := sr.batches.Load(batchID)
	if !ok {
		t.Fatalf("batch %s is not tracked", batchID)
	}
	b := val.(*pendingBatch)
	b.mu.Lock()
	defer b.mu.Unlock()
	names := make([]string, len(b.tasks))
	for name, idx := range b.childToIndex {
		names[idx] = name
	}
	return names
}

// decodeSubagentBatchResult finds the batch result published for parent-run.
func decodeSubagentBatchResult(t *testing.T, bus *recordingEventBus, batchID string) (ipc.SubagentBatchResult, bool) {
	t.Helper()
	want := eventbus.TopicAgentSubagentResult + ".parent-run"
	for _, ev := range bus.published {
		if ev.Topic != want {
			continue
		}
		var res ipc.SubagentBatchResult
		if err := json.Unmarshal(ev.Event.Data, &res); err != nil {
			t.Fatalf("batch result is not decodable: %v", err)
		}
		if res.BatchID == batchID {
			return res, true
		}
	}
	return ipc.SubagentBatchResult{}, false
}

func subagentEvent(runID, data string) *eventbus.Event {
	return &eventbus.Event{
		Metadata: map[string]string{"agentRunID": runID, "namespace": "default"},
		Data:     json.RawMessage(data),
	}
}

// Regression test for the sr.pending value/pointer seam: batch children must
// settle through handleChildCompleted/handleChildFailed exactly as stored by
// handleSubagentRequest. A writer that stores pendingDelegation by value while
// the handlers assert *pendingDelegation panics the router goroutine here.
func TestSubagentBatch_ParallelChildrenSettle(t *testing.T) {
	sr, bus := subagentBatchFixture(t)
	ctx := context.Background()

	reqData, err := json.Marshal(ipc.SubagentSpawnRequest{
		BatchID:  "batch-1",
		Strategy: "parallel",
		Tasks:    []ipc.SubagentTask{{ID: "a", Task: "task a"}, {ID: "b", Task: "task b"}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	sr.handleSubagentRequest(ctx, subagentEvent("parent-run", string(reqData)))

	children := batchChildren(t, sr, "batch-1")
	if len(children) != 2 || children[0] == "" || children[1] == "" {
		t.Fatalf("expected 2 spawned children, got %v", children)
	}

	// One child succeeds and one fails, exercising both retrieval sites.
	sr.handleChildCompleted(ctx, subagentEvent(children[0], `{"status":"success","response":"done-a"}`))
	sr.handleChildFailed(ctx, subagentEvent(children[1], `{"error":"boom"}`))

	res, ok := decodeSubagentBatchResult(t, bus, "batch-1")
	if !ok {
		t.Fatal("no batch result published; the parent's spawn_subagents tool would block to its deadline")
	}
	if res.Status != "partial" {
		t.Errorf("status = %q, want partial", res.Status)
	}
	if res.Results[0].Status != "success" || res.Results[0].Response != "done-a" {
		t.Errorf("results[0] = %+v, want success/done-a", res.Results[0])
	}
	if res.Results[1].Status != "error" || res.Results[1].Error != "boom" {
		t.Errorf("results[1] = %+v, want error/boom", res.Results[1])
	}
}

// Sequential batches store the first child in handleSubagentRequest and every
// later one in spawnSequentialChild — this drives both writers through
// completion.
func TestSubagentBatch_SequentialChildrenSettle(t *testing.T) {
	sr, bus := subagentBatchFixture(t)
	ctx := context.Background()

	reqData, err := json.Marshal(ipc.SubagentSpawnRequest{
		BatchID:  "batch-2",
		Strategy: "sequential",
		Tasks:    []ipc.SubagentTask{{ID: "a", Task: "task a"}, {ID: "b", Task: "task b"}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	sr.handleSubagentRequest(ctx, subagentEvent("parent-run", string(reqData)))

	first := batchChildren(t, sr, "batch-2")[0]
	if first == "" {
		t.Fatal("sequential batch did not spawn its first child")
	}
	sr.handleChildCompleted(ctx, subagentEvent(first, `{"status":"success","response":"done-a"}`))

	// Completing the first task spawns the second child.
	second := batchChildren(t, sr, "batch-2")[1]
	if second == "" {
		t.Fatal("completing the first task did not spawn the second child")
	}
	sr.handleChildCompleted(ctx, subagentEvent(second, `{"status":"success","response":"done-b"}`))

	res, ok := decodeSubagentBatchResult(t, bus, "batch-2")
	if !ok {
		t.Fatal("no batch result published; the parent's spawn_subagents tool would block to its deadline")
	}
	if res.Status != "success" {
		t.Errorf("status = %q, want success", res.Status)
	}
	if res.Results[1].Response != "done-b" {
		t.Errorf("results[1] = %+v, want done-b", res.Results[1])
	}
}

func TestSubagentBatch_ChildrenTaggedWithBatchID(t *testing.T) {
	sr, _ := subagentBatchFixture(t)
	ctx := context.Background()

	reqData, err := json.Marshal(ipc.SubagentSpawnRequest{
		BatchID:  "batch-tag",
		Strategy: "parallel",
		Tasks:    []ipc.SubagentTask{{ID: "a", Task: "task a"}, {ID: "b", Task: "task b"}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	sr.handleSubagentRequest(ctx, subagentEvent("parent-run", string(reqData)))

	children := batchChildren(t, sr, "batch-tag")
	for _, childName := range children {
		if childName == "" {
			t.Fatalf("expected every child to be spawned, got %v", children)
		}
		var child sympoziumv1alpha1.AgentRun
		if err := sr.Client.Get(ctx, types.NamespacedName{Name: childName, Namespace: "default"}, &child); err != nil {
			t.Fatalf("get child %s: %v", childName, err)
		}
		if got := child.Labels["sympozium.ai/subagent-batch-id"]; got != "batch-tag" {
			t.Fatalf("child %s batch label = %q, want %q", childName, got, "batch-tag")
		}
	}
}
