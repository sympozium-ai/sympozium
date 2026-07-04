package controller

import (
	"context"
	"sync"
	"testing"

	"github.com/go-logr/logr"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
)

// TestFinalizeBatch_Idempotent verifies that finalizeBatch publishes the batch
// result exactly once even when called multiple times for the same batch — the
// guard that lets the sequential-spawn-failure recovery path call finalize
// without risking a duplicate result being delivered to the parent agent.
func TestFinalizeBatch_Idempotent(t *testing.T) {
	bus := &recordingEventBus{}
	sr := &SpawnRouter{
		EventBus: bus,
		Log:      logr.Discard(),
	}

	batch := &pendingBatch{
		batchID:     "batch-xyz",
		parentRunID: "parent-1",
		namespace:   "default",
		tasks:       []ipc.SubagentTask{{ID: "a"}},
		results:     []ipc.SubagentChildResult{{ID: "a", Status: "success"}},
		completed:   1,
	}
	sr.batches.Store(batch.batchID, batch)

	ctx := context.Background()
	sr.finalizeBatch(ctx, batch)
	sr.finalizeBatch(ctx, batch) // second call must be a no-op
	sr.finalizeBatch(ctx, batch)

	var count int
	for _, e := range bus.published {
		if e.Event != nil && e.Event.Metadata["batchId"] == "batch-xyz" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("finalizeBatch published %d batch results, want exactly 1", count)
	}

	// The batch must be removed from the in-memory map so it does not leak.
	if _, ok := sr.batches.Load(batch.batchID); ok {
		t.Error("batch was not deleted from sr.batches after finalize")
	}
}

// TestFinalizeBatch_ConcurrentIsSingle exercises the LoadAndDelete guard under
// concurrent finalize calls (e.g. a completion event and a spawn-failure
// advance racing) and asserts only one result is published.
func TestFinalizeBatch_ConcurrentIsSingle(t *testing.T) {
	bus := &recordingEventBus{}
	sr := &SpawnRouter{EventBus: bus, Log: logr.Discard()}

	batch := &pendingBatch{
		batchID:     "batch-conc",
		parentRunID: "parent-2",
		namespace:   "default",
		tasks:       []ipc.SubagentTask{{ID: "a"}, {ID: "b"}},
		results:     []ipc.SubagentChildResult{{ID: "a", Status: "success"}, {ID: "b", Status: "success"}},
		completed:   2,
	}
	sr.batches.Store(batch.batchID, batch)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sr.finalizeBatch(context.Background(), batch)
		}()
	}
	wg.Wait()

	var count int
	for _, e := range bus.published {
		if e.Event != nil && e.Event.Metadata["batchId"] == "batch-conc" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("concurrent finalizeBatch published %d results, want exactly 1", count)
	}
}

var _ = eventbus.EventBus(&recordingEventBus{})
