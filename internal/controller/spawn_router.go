// Package controller contains the spawn router which handles delegation
// spawn requests from agents. When an agent calls the delegate_to_persona tool,
// the IPC bridge publishes an agent.spawn.request event to NATS. This router
// subscribes to those events, creates child AgentRun CRs via the Spawner, and
// delivers the child's result back to the parent agent when it completes.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
	"github.com/sympozium-ai/sympozium/internal/orchestrator"
)

// SpawnRouter subscribes to agent.spawn.request and agent.subagent.request
// events from the IPC bridge, creates child AgentRun CRs for delegated tasks
// and ad-hoc subagent batches, and delivers results back to the parent.
type SpawnRouter struct {
	Client   client.Client
	EventBus eventbus.EventBus
	Log      logr.Logger

	spawner    orchestrator.Spawner
	pending    sync.Map // childRunName -> pendingDelegation
	batches    sync.Map // batchID -> *pendingBatch
	childBatch sync.Map // childRunName -> batchID (reverse lookup)
}

// pendingDelegation tracks the mapping from a child run to the parent that
// requested it and the request ID for result correlation.
type pendingDelegation struct {
	RequestID   string
	ParentRunID string
}

// pendingBatch tracks the state of an in-flight subagent batch spawn.
type pendingBatch struct {
	batchID       string
	parentRunID   string
	namespace     string
	strategy      string
	failurePolicy string
	tasks         []ipc.SubagentTask
	results       []ipc.SubagentChildResult // ordered, same index as tasks
	completed     int
	failed        int
	nextIndex     int  // for sequential: index of next task to spawn
	aborted       bool // set when fail-fast triggers
	mu            sync.Mutex
	// childToIndex maps childRunName to its index in tasks/results.
	childToIndex map[string]int
}

// Start begins listening for spawn request and child completion events.
// It blocks until ctx is cancelled.
func (sr *SpawnRouter) Start(ctx context.Context) error {
	sr.Log.Info("Starting spawn router")

	sr.spawner = orchestrator.Spawner{
		Client: sr.Client,
		Log:    sr.Log.WithName("spawner"),
	}

	spawnCh, err := sr.EventBus.Subscribe(ctx, eventbus.TopicAgentSpawnRequest)
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", eventbus.TopicAgentSpawnRequest, err)
	}

	subagentCh, err := sr.EventBus.Subscribe(ctx, eventbus.TopicAgentSubagentRequest)
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", eventbus.TopicAgentSubagentRequest, err)
	}

	completedCh, err := sr.EventBus.Subscribe(ctx, eventbus.TopicAgentRunCompleted)
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", eventbus.TopicAgentRunCompleted, err)
	}

	failedCh, err := sr.EventBus.Subscribe(ctx, eventbus.TopicAgentRunFailed)
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", eventbus.TopicAgentRunFailed, err)
	}

	for {
		select {
		case <-ctx.Done():
			sr.Log.Info("Spawn router shutting down")
			return nil
		case event := <-spawnCh:
			sr.handleSpawnRequest(ctx, event)
		case event := <-subagentCh:
			sr.handleSubagentRequest(ctx, event)
		case event := <-completedCh:
			sr.handleChildCompleted(ctx, event)
		case event := <-failedCh:
			sr.handleChildFailed(ctx, event)
		}
	}
}

// handleSpawnRequest creates a child AgentRun for a delegation request.
func (sr *SpawnRouter) handleSpawnRequest(ctx context.Context, event *eventbus.Event) {
	parentRunID := event.Metadata["agentRunID"]
	instanceName := event.Metadata["instanceName"]

	var req ipc.SpawnRequest
	if err := json.Unmarshal(event.Data, &req); err != nil {
		sr.Log.Error(err, "failed to unmarshal spawn request")
		return
	}

	if req.TargetPersona == "" || req.PackName == "" {
		sr.Log.Info("Ignoring spawn request without persona/pack context",
			"parentRun", parentRunID,
		)
		return
	}

	sr.Log.Info("Processing delegation spawn request",
		"parentRun", parentRunID,
		"instance", instanceName,
		"targetPersona", req.TargetPersona,
		"pack", req.PackName,
		"requestID", req.RequestID,
	)

	// Check circuit breaker before spawning.
	if err := sr.checkCircuitBreaker(ctx, req.PackName, parentRunID); err != nil {
		sr.Log.Info("Circuit breaker is open, rejecting spawn",
			"parentRun", parentRunID,
			"pack", req.PackName,
			"error", err.Error(),
		)
		sr.publishDelegateResult(ctx, parentRunID, req.RequestID, "", err.Error())
		return
	}

	// Look up the parent AgentRun to get namespace, model, session key, depth.
	var parentRun sympoziumv1alpha1.AgentRun
	if err := sr.Client.Get(ctx, types.NamespacedName{Name: parentRunID, Namespace: "default"}, &parentRun); err != nil {
		// Try to find the namespace from the instance.
		var inst sympoziumv1alpha1.Agent
		if err2 := sr.Client.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: "default"}, &inst); err2 == nil {
			if err3 := sr.Client.Get(ctx, types.NamespacedName{Name: parentRunID, Namespace: inst.Namespace}, &parentRun); err3 != nil {
				sr.Log.Error(err3, "failed to look up parent AgentRun", "name", parentRunID)
				return
			}
		} else {
			sr.Log.Error(err, "failed to look up parent AgentRun", "name", parentRunID)
			return
		}
	}

	depth := 0
	sessionKey := parentRun.Spec.SessionKey
	if parentRun.Spec.Parent != nil {
		depth = parentRun.Spec.Parent.SpawnDepth
	}

	spawnReq := orchestrator.SpawnRequest{
		ParentRunName:    parentRunID,
		ParentSessionKey: sessionKey,
		InstanceName:     instanceName,
		Namespace:        parentRun.Namespace,
		Task:             req.Task,
		SystemPrompt:     req.SystemPrompt,
		AgentID:          req.AgentID,
		CurrentDepth:     depth,
		Model:            parentRun.Spec.Model,
		TargetPersona:    req.TargetPersona,
		PackName:         req.PackName,
		ImagePullSecrets: parentRun.Spec.ImagePullSecrets,
		Volumes:          parentRun.Spec.Volumes,
		VolumeMounts:     parentRun.Spec.VolumeMounts,
	}

	result, err := sr.spawner.Spawn(ctx, spawnReq)
	if err != nil {
		sr.Log.Error(err, "failed to spawn delegate",
			"parentRun", parentRunID,
			"targetPersona", req.TargetPersona,
		)
		// Deliver error back to the parent so the blocking tool can unblock.
		sr.publishDelegateResult(ctx, parentRunID, req.RequestID, "", fmt.Sprintf("spawn failed: %v", err))
		return
	}

	sr.Log.Info("Created delegate child run",
		"childRun", result.RunName,
		"parentRun", parentRunID,
		"targetPersona", req.TargetPersona,
	)

	// Track the child → parent mapping for result delivery.
	sr.pending.Store(result.RunName, pendingDelegation{
		RequestID:   req.RequestID,
		ParentRunID: parentRunID,
	})

	// Patch parent run to AwaitingDelegate and populate DelegateStatus.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var parent sympoziumv1alpha1.AgentRun
		if err := sr.Client.Get(ctx, types.NamespacedName{
			Name:      parentRunID,
			Namespace: parentRun.Namespace,
		}, &parent); err != nil {
			return err
		}
		parent.Status.Phase = sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate
		parent.Status.Delegates = append(parent.Status.Delegates, sympoziumv1alpha1.DelegateStatus{
			ChildRunName:  result.RunName,
			TargetPersona: req.TargetPersona,
			Phase:         sympoziumv1alpha1.AgentRunPhasePending,
		})
		return sr.Client.Status().Update(ctx, &parent)
	}); err != nil {
		sr.Log.Error(err, "failed to update parent status to AwaitingDelegate",
			"parentRun", parentRunID,
		)
	}
}

// handleChildCompleted checks if a completed run is a delegation child and
// delivers the result back to the parent agent.
func (sr *SpawnRouter) handleChildCompleted(ctx context.Context, event *eventbus.Event) {
	childRunID := event.Metadata["agentRunID"]
	val, ok := sr.pending.LoadAndDelete(childRunID)
	if !ok {
		return // Not a delegation child.
	}
	pd := val.(pendingDelegation)

	// Extract the response from the completion event data.
	var data struct {
		Response string `json:"response"`
		Status   string `json:"status"`
	}
	_ = json.Unmarshal(event.Data, &data)

	sr.Log.Info("Delegate child completed",
		"childRun", childRunID,
		"parentRun", pd.ParentRunID,
		"responseLen", len(data.Response),
	)

	// If the event didn't carry the response, try reading it from the AgentRun status.
	response := data.Response
	if response == "" {
		var childRun sympoziumv1alpha1.AgentRun
		if err := sr.Client.Get(ctx, types.NamespacedName{Name: childRunID, Namespace: "default"}, &childRun); err == nil {
			response = childRun.Status.Result
		}
	}

	sr.publishDelegateResult(ctx, pd.ParentRunID, pd.RequestID, response, "")
	sr.updateParentDelegateStatus(ctx, pd.ParentRunID, childRunID, sympoziumv1alpha1.AgentRunPhaseSucceeded, response, "")
	sr.resetCircuitBreaker(ctx, pd.ParentRunID)

	// Check if this child belongs to a subagent batch.
	sr.handleBatchChildDone(ctx, childRunID, response, "")
}

// handleChildFailed checks if a failed run is a delegation child and
// delivers the error back to the parent agent.
func (sr *SpawnRouter) handleChildFailed(ctx context.Context, event *eventbus.Event) {
	childRunID := event.Metadata["agentRunID"]
	val, ok := sr.pending.LoadAndDelete(childRunID)
	if !ok {
		return // Not a delegation child.
	}
	pd := val.(pendingDelegation)

	var data struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(event.Data, &data)

	errMsg := data.Error
	if errMsg == "" {
		errMsg = "delegate child run failed"
	}

	sr.Log.Info("Delegate child failed",
		"childRun", childRunID,
		"parentRun", pd.ParentRunID,
		"error", errMsg,
	)

	sr.publishDelegateResult(ctx, pd.ParentRunID, pd.RequestID, "", errMsg)
	sr.updateParentDelegateStatus(ctx, pd.ParentRunID, childRunID, sympoziumv1alpha1.AgentRunPhaseFailed, "", errMsg)
	sr.incrementCircuitBreaker(ctx, pd.ParentRunID)

	// Check if this child belongs to a subagent batch.
	sr.handleBatchChildDone(ctx, childRunID, "", errMsg)
}

// handleSubagentRequest creates child AgentRun CRs for an ad-hoc subagent
// batch request. Children inherit the parent's config and are tracked as a batch.
func (sr *SpawnRouter) handleSubagentRequest(ctx context.Context, event *eventbus.Event) {
	parentRunID := event.Metadata["agentRunID"]
	instanceName := event.Metadata["instanceName"]

	var req ipc.SubagentSpawnRequest
	if err := json.Unmarshal(event.Data, &req); err != nil {
		sr.Log.Error(err, "failed to unmarshal subagent spawn request")
		return
	}

	if len(req.Tasks) == 0 {
		sr.Log.Info("Ignoring subagent spawn request with no tasks", "parentRun", parentRunID)
		return
	}

	sr.Log.Info("Processing subagent spawn request",
		"parentRun", parentRunID,
		"batchID", req.BatchID,
		"strategy", req.Strategy,
		"taskCount", len(req.Tasks),
	)

	// Look up the parent AgentRun.
	var parentRun sympoziumv1alpha1.AgentRun
	if err := sr.Client.Get(ctx, types.NamespacedName{Name: parentRunID, Namespace: "default"}, &parentRun); err != nil {
		var inst sympoziumv1alpha1.Agent
		if err2 := sr.Client.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: "default"}, &inst); err2 == nil {
			if err3 := sr.Client.Get(ctx, types.NamespacedName{Name: parentRunID, Namespace: inst.Namespace}, &parentRun); err3 != nil {
				sr.Log.Error(err3, "failed to look up parent AgentRun for subagent request", "name", parentRunID)
				sr.publishSubagentBatchError(ctx, parentRunID, req.BatchID, fmt.Sprintf("parent run not found: %v", err3))
				return
			}
		} else {
			sr.Log.Error(err, "failed to look up parent AgentRun for subagent request", "name", parentRunID)
			sr.publishSubagentBatchError(ctx, parentRunID, req.BatchID, fmt.Sprintf("parent run not found: %v", err))
			return
		}
	}

	// Look up the Agent to get SubagentsSpec limits.
	var inst sympoziumv1alpha1.Agent
	if err := sr.Client.Get(ctx, client.ObjectKey{Namespace: parentRun.Namespace, Name: parentRun.Spec.AgentRef}, &inst); err != nil {
		sr.Log.Error(err, "failed to look up Agent for subagent limits")
		sr.publishSubagentBatchError(ctx, parentRunID, req.BatchID, fmt.Sprintf("agent not found: %v", err))
		return
	}

	// Validate that the "subagents" skill is attached to the parent run.
	hasSkill := false
	for _, s := range parentRun.Spec.Skills {
		if s.SkillPackRef == "subagents" {
			hasSkill = true
			break
		}
	}
	if !hasSkill {
		sr.publishSubagentBatchError(ctx, parentRunID, req.BatchID, "subagents skill not attached to this agent")
		return
	}

	// Read optional limits from SubagentsSpec on the Agent, falling back to defaults.
	maxChildren, maxDepth := 3, 2
	if sub := inst.Spec.Agents.Default.Subagents; sub != nil {
		if sub.MaxChildrenPerAgent > 0 {
			maxChildren = sub.MaxChildrenPerAgent
		}
		if sub.MaxDepth > 0 {
			maxDepth = sub.MaxDepth
		}
	}

	if len(req.Tasks) > maxChildren {
		sr.publishSubagentBatchError(ctx, parentRunID, req.BatchID,
			fmt.Sprintf("batch size %d exceeds MaxChildrenPerAgent limit of %d", len(req.Tasks), maxChildren))
		return
	}
	depth := 0
	if parentRun.Spec.Parent != nil {
		depth = parentRun.Spec.Parent.SpawnDepth
	}
	if depth+1 > maxDepth {
		sr.publishSubagentBatchError(ctx, parentRunID, req.BatchID,
			fmt.Sprintf("spawn depth %d would exceed MaxDepth limit of %d", depth+1, maxDepth))
		return
	}

	// Apply default failure policy based on strategy.
	failurePolicy := req.FailurePolicy
	if failurePolicy == "" {
		if req.Strategy == "sequential" {
			failurePolicy = "fail-fast"
		} else {
			failurePolicy = "continue"
		}
	}

	// Create batch tracker.
	batch := &pendingBatch{
		batchID:       req.BatchID,
		parentRunID:   parentRunID,
		namespace:     parentRun.Namespace,
		strategy:      req.Strategy,
		failurePolicy: failurePolicy,
		tasks:         req.Tasks,
		results:       make([]ipc.SubagentChildResult, len(req.Tasks)),
		childToIndex:  make(map[string]int, len(req.Tasks)),
	}

	sr.batches.Store(req.BatchID, batch)

	// Determine how many to spawn initially.
	spawnCount := len(req.Tasks)
	if req.Strategy == "sequential" {
		spawnCount = 1
	}

	sessionKey := parentRun.Spec.SessionKey

	var delegates []sympoziumv1alpha1.DelegateStatus
	for i := 0; i < spawnCount; i++ {
		task := req.Tasks[i]
		spawnReq := orchestrator.SpawnRequest{
			ParentRunName:    parentRunID,
			ParentSessionKey: sessionKey,
			InstanceName:     parentRun.Spec.AgentRef,
			Namespace:        parentRun.Namespace,
			Task:             task.Task,
			SystemPrompt:     task.SystemPrompt,
			AgentID:          parentRun.Spec.AgentID,
			CurrentDepth:     depth,
			Model:            parentRun.Spec.Model,
			Skills:           parentRun.Spec.Skills,
			ImagePullSecrets: parentRun.Spec.ImagePullSecrets,
			Volumes:          parentRun.Spec.Volumes,
			VolumeMounts:     parentRun.Spec.VolumeMounts,
			ChildIndex:       i + 1,
		}

		result, err := sr.spawner.Spawn(ctx, spawnReq)
		if err != nil {
			sr.Log.Error(err, "failed to spawn subagent child",
				"batchID", req.BatchID,
				"taskID", task.ID,
				"childIndex", i,
			)
			batch.mu.Lock()
			batch.results[i] = ipc.SubagentChildResult{
				ID:     task.ID,
				Status: "error",
				Error:  fmt.Sprintf("spawn failed: %v", err),
			}
			batch.completed++
			batch.failed++
			batch.mu.Unlock()
			continue
		}

		sr.Log.Info("Created subagent child run",
			"childRun", result.RunName,
			"parentRun", parentRunID,
			"batchID", req.BatchID,
			"taskID", task.ID,
		)

		// Track child -> parent for result delivery (reuse existing pending map).
		sr.pending.Store(result.RunName, pendingDelegation{
			RequestID:   req.BatchID,
			ParentRunID: parentRunID,
		})

		// Track child -> batch for batch result aggregation.
		batch.mu.Lock()
		batch.childToIndex[result.RunName] = i
		batch.results[i] = ipc.SubagentChildResult{
			ID:      task.ID,
			RunName: result.RunName,
		}
		batch.mu.Unlock()
		sr.childBatch.Store(result.RunName, req.BatchID)

		delegates = append(delegates, sympoziumv1alpha1.DelegateStatus{
			ChildRunName: result.RunName,
			BatchID:      req.BatchID,
			TaskID:       task.ID,
			Phase:        sympoziumv1alpha1.AgentRunPhasePending,
		})
	}

	if req.Strategy == "sequential" {
		batch.mu.Lock()
		batch.nextIndex = 1
		batch.mu.Unlock()
	}

	// Check if all spawns failed immediately.
	batch.mu.Lock()
	allDone := batch.completed == len(batch.tasks)
	batch.mu.Unlock()
	if allDone {
		sr.finalizeBatch(ctx, batch)
		return
	}

	// Patch parent status to AwaitingDelegate.
	if len(delegates) > 0 {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var parent sympoziumv1alpha1.AgentRun
			if err := sr.Client.Get(ctx, types.NamespacedName{
				Name:      parentRunID,
				Namespace: parentRun.Namespace,
			}, &parent); err != nil {
				return err
			}
			parent.Status.Phase = sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate
			parent.Status.Delegates = append(parent.Status.Delegates, delegates...)
			return sr.Client.Status().Update(ctx, &parent)
		}); err != nil {
			sr.Log.Error(err, "failed to update parent status for subagent batch",
				"parentRun", parentRunID,
				"batchID", req.BatchID,
			)
		}
	}
}

// handleBatchChildDone checks if a completed/failed child belongs to a subagent
// batch and updates the batch state. For sequential batches, it spawns the next
// child. When all children are done, it publishes the aggregated result.
func (sr *SpawnRouter) handleBatchChildDone(ctx context.Context, childRunID, response, errMsg string) {
	batchIDVal, ok := sr.childBatch.LoadAndDelete(childRunID)
	if !ok {
		return // Not a batch child.
	}
	batchID := batchIDVal.(string)

	val, ok := sr.batches.Load(batchID)
	if !ok {
		sr.Log.Info("Batch not found for child (already finalized?)", "batchID", batchID, "childRun", childRunID)
		return
	}
	batch := val.(*pendingBatch)

	batch.mu.Lock()
	defer batch.mu.Unlock()

	idx, ok := batch.childToIndex[childRunID]
	if !ok {
		return
	}

	if errMsg != "" {
		batch.results[idx].Status = "error"
		batch.results[idx].Error = errMsg
		batch.failed++
	} else {
		batch.results[idx].Status = "success"
		batch.results[idx].Response = response
	}
	batch.completed++

	sr.Log.Info("Subagent batch child done",
		"batchID", batchID,
		"childRun", childRunID,
		"completed", batch.completed,
		"total", len(batch.tasks),
		"failed", batch.failed,
	)

	// For sequential strategy: handle next task or fail-fast.
	if batch.strategy == "sequential" && !batch.aborted {
		if errMsg != "" && batch.failurePolicy == "fail-fast" {
			batch.aborted = true
			sr.Log.Info("Subagent batch fail-fast triggered",
				"batchID", batchID,
				"failedChild", childRunID,
			)
			// Unlock before finalize (finalize acquires no lock, reads committed state).
			batch.mu.Unlock()
			sr.finalizeBatch(ctx, batch)
			batch.mu.Lock() // re-lock for deferred unlock
			return
		}

		// Spawn next task if more remain.
		if batch.nextIndex < len(batch.tasks) {
			nextIdx := batch.nextIndex
			batch.nextIndex++
			// Unlock while spawning (may take time).
			batch.mu.Unlock()
			sr.spawnSequentialChild(ctx, batch, nextIdx)
			batch.mu.Lock() // re-lock for deferred unlock
			return
		}
	}

	// Check if all tasks are done.
	if batch.completed >= len(batch.tasks) || batch.aborted {
		batch.mu.Unlock()
		sr.finalizeBatch(ctx, batch)
		batch.mu.Lock() // re-lock for deferred unlock
	}
}

// spawnSequentialChild spawns the next child in a sequential batch.
func (sr *SpawnRouter) spawnSequentialChild(ctx context.Context, batch *pendingBatch, idx int) {
	task := batch.tasks[idx]

	// Look up parent for spawn context.
	var parentRun sympoziumv1alpha1.AgentRun
	if err := sr.Client.Get(ctx, types.NamespacedName{Name: batch.parentRunID, Namespace: batch.namespace}, &parentRun); err != nil {
		sr.Log.Error(err, "failed to look up parent for sequential spawn")
		batch.mu.Lock()
		batch.results[idx] = ipc.SubagentChildResult{
			ID:     task.ID,
			Status: "error",
			Error:  fmt.Sprintf("parent lookup failed: %v", err),
		}
		batch.completed++
		batch.failed++
		batch.mu.Unlock()
		return
	}

	depth := 0
	if parentRun.Spec.Parent != nil {
		depth = parentRun.Spec.Parent.SpawnDepth
	}

	spawnReq := orchestrator.SpawnRequest{
		ParentRunName:    batch.parentRunID,
		ParentSessionKey: parentRun.Spec.SessionKey,
		InstanceName:     parentRun.Spec.AgentRef,
		Namespace:        batch.namespace,
		Task:             task.Task,
		SystemPrompt:     task.SystemPrompt,
		AgentID:          parentRun.Spec.AgentID,
		CurrentDepth:     depth,
		Model:            parentRun.Spec.Model,
		Skills:           parentRun.Spec.Skills,
		ImagePullSecrets: parentRun.Spec.ImagePullSecrets,
		Volumes:          parentRun.Spec.Volumes,
		VolumeMounts:     parentRun.Spec.VolumeMounts,
		ChildIndex:       idx + 1,
	}

	result, err := sr.spawner.Spawn(ctx, spawnReq)
	if err != nil {
		sr.Log.Error(err, "failed to spawn sequential subagent child",
			"batchID", batch.batchID,
			"taskID", task.ID,
		)
		batch.mu.Lock()
		batch.results[idx] = ipc.SubagentChildResult{
			ID:     task.ID,
			Status: "error",
			Error:  fmt.Sprintf("spawn failed: %v", err),
		}
		batch.completed++
		batch.failed++
		batch.mu.Unlock()
		return
	}

	sr.Log.Info("Created sequential subagent child",
		"childRun", result.RunName,
		"batchID", batch.batchID,
		"taskID", task.ID,
		"index", idx,
	)

	sr.pending.Store(result.RunName, pendingDelegation{
		RequestID:   batch.batchID,
		ParentRunID: batch.parentRunID,
	})

	batch.mu.Lock()
	batch.childToIndex[result.RunName] = idx
	batch.results[idx] = ipc.SubagentChildResult{
		ID:      task.ID,
		RunName: result.RunName,
	}
	batch.mu.Unlock()
	sr.childBatch.Store(result.RunName, batch.batchID)

	// Update parent's DelegateStatus with the new child.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var parent sympoziumv1alpha1.AgentRun
		if err := sr.Client.Get(ctx, types.NamespacedName{
			Name:      batch.parentRunID,
			Namespace: batch.namespace,
		}, &parent); err != nil {
			return err
		}
		parent.Status.Delegates = append(parent.Status.Delegates, sympoziumv1alpha1.DelegateStatus{
			ChildRunName: result.RunName,
			BatchID:      batch.batchID,
			TaskID:       task.ID,
			Phase:        sympoziumv1alpha1.AgentRunPhasePending,
		})
		return sr.Client.Status().Update(ctx, &parent)
	}); err != nil {
		sr.Log.Error(err, "failed to update parent with sequential child",
			"parentRun", batch.parentRunID,
			"childRun", result.RunName,
		)
	}
}

// finalizeBatch publishes the aggregated batch result to the parent's IPC bridge.
func (sr *SpawnRouter) finalizeBatch(ctx context.Context, batch *pendingBatch) {
	sr.batches.Delete(batch.batchID)

	// Determine overall status.
	status := "success"
	batch.mu.Lock()
	if batch.failed > 0 && batch.failed < len(batch.tasks) {
		status = "partial"
	} else if batch.failed == len(batch.tasks) || (batch.aborted && batch.failed > 0) {
		status = "error"
	}
	results := make([]ipc.SubagentChildResult, len(batch.results))
	copy(results, batch.results)
	batch.mu.Unlock()

	batchResult := ipc.SubagentBatchResult{
		BatchID: batch.batchID,
		Status:  status,
		Results: results,
	}

	topic := fmt.Sprintf("%s.%s", eventbus.TopicAgentSubagentResult, batch.parentRunID)
	evt, err := eventbus.NewEvent(topic, map[string]string{
		"agentRunID": batch.parentRunID,
		"batchId":    batch.batchID,
	}, batchResult)
	if err != nil {
		sr.Log.Error(err, "failed to create subagent batch result event")
		return
	}
	if err := sr.EventBus.Publish(ctx, topic, evt); err != nil {
		sr.Log.Error(err, "failed to publish subagent batch result",
			"parentRun", batch.parentRunID,
			"batchID", batch.batchID,
		)
	}

	sr.Log.Info("Published subagent batch result",
		"parentRun", batch.parentRunID,
		"batchID", batch.batchID,
		"status", status,
		"completed", batch.completed,
		"failed", batch.failed,
	)
}

// publishSubagentBatchError publishes an immediate error result for a batch
// that was rejected before any children were spawned (e.g. limit violation).
func (sr *SpawnRouter) publishSubagentBatchError(ctx context.Context, parentRunID, batchID, errMsg string) {
	batchResult := ipc.SubagentBatchResult{
		BatchID: batchID,
		Status:  "error",
		Results: []ipc.SubagentChildResult{
			{
				ID:     "_batch",
				Status: "error",
				Error:  errMsg,
			},
		},
	}

	topic := fmt.Sprintf("%s.%s", eventbus.TopicAgentSubagentResult, parentRunID)
	evt, err := eventbus.NewEvent(topic, map[string]string{
		"agentRunID": parentRunID,
		"batchId":    batchID,
	}, batchResult)
	if err != nil {
		sr.Log.Error(err, "failed to create batch error event")
		return
	}
	if err := sr.EventBus.Publish(ctx, topic, evt); err != nil {
		sr.Log.Error(err, "failed to publish batch error",
			"parentRun", parentRunID,
			"batchID", batchID,
		)
	}
}

// publishDelegateResult sends the child's result to the parent's IPC bridge
// via the event bus so the blocking delegate tool can pick it up.
func (sr *SpawnRouter) publishDelegateResult(ctx context.Context, parentRunID, requestID, response, errMsg string) {
	result := ipc.DelegateResult{
		RequestID: requestID,
		Status:    "success",
		Response:  response,
	}
	if errMsg != "" {
		result.Status = "error"
		result.Error = errMsg
		result.Response = ""
	}

	topic := fmt.Sprintf("%s.%s", eventbus.TopicAgentDelegateResult, parentRunID)
	evt, err := eventbus.NewEvent(topic, map[string]string{
		"agentRunID": parentRunID,
		"requestId":  requestID,
	}, result)
	if err != nil {
		sr.Log.Error(err, "failed to create delegate result event")
		return
	}
	if err := sr.EventBus.Publish(ctx, topic, evt); err != nil {
		sr.Log.Error(err, "failed to publish delegate result",
			"parentRun", parentRunID,
			"requestId", requestID,
		)
	}
}

// updateParentDelegateStatus patches the parent's DelegateStatus entry
// for the completed child and transitions the parent back to Running.
func (sr *SpawnRouter) updateParentDelegateStatus(ctx context.Context, parentRunID, childRunID string, phase sympoziumv1alpha1.AgentRunPhase, result, errMsg string) {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var parent sympoziumv1alpha1.AgentRun
		if err := sr.Client.Get(ctx, types.NamespacedName{Name: parentRunID, Namespace: "default"}, &parent); err != nil {
			return err
		}

		// Update the matching delegate entry.
		for i := range parent.Status.Delegates {
			if parent.Status.Delegates[i].ChildRunName == childRunID {
				parent.Status.Delegates[i].Phase = phase
				parent.Status.Delegates[i].Result = result
				parent.Status.Delegates[i].Error = errMsg
				break
			}
		}

		// Check if all delegates are now terminal.
		allDone := true
		for _, d := range parent.Status.Delegates {
			if d.Phase != sympoziumv1alpha1.AgentRunPhaseSucceeded &&
				d.Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
				allDone = false
				break
			}
		}

		// Transition parent back to Running so the controller resumes
		// timeout checking and the agent pod can continue.
		if allDone {
			parent.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
		}

		return sr.Client.Status().Update(ctx, &parent)
	}); err != nil {
		sr.Log.Error(err, "failed to update parent delegate status",
			"parentRun", parentRunID,
			"childRun", childRunID,
		)
	}
}

// checkCircuitBreaker returns an error if the circuit breaker is open for the
// given ensemble. The circuit breaker trips after consecutive delegate failures
// exceed the configured threshold.
func (sr *SpawnRouter) checkCircuitBreaker(ctx context.Context, packName, parentRunID string) error {
	if packName == "" {
		return nil
	}
	pack, err := sr.getEnsembleForRun(ctx, parentRunID, packName)
	if err != nil || pack == nil {
		return nil
	}
	if pack.Status.CircuitBreakerOpen {
		return fmt.Errorf("circuit breaker is open for ensemble %q: %d consecutive delegation failures",
			packName, pack.Status.ConsecutiveDelegateFailures)
	}
	return nil
}

// incrementCircuitBreaker increments the consecutive failure counter and
// opens the circuit breaker if the threshold is crossed.
func (sr *SpawnRouter) incrementCircuitBreaker(ctx context.Context, parentRunID string) {
	pack, err := sr.getEnsembleForRunByParent(ctx, parentRunID)
	if err != nil || pack == nil {
		return
	}
	if pack.Spec.SharedMemory == nil || pack.Spec.SharedMemory.Membrane == nil ||
		pack.Spec.SharedMemory.Membrane.CircuitBreaker == nil {
		return
	}
	cb := pack.Spec.SharedMemory.Membrane.CircuitBreaker
	threshold := cb.ConsecutiveFailures
	if threshold <= 0 {
		threshold = 3
	}

	patch := client.MergeFrom(pack.DeepCopy())
	pack.Status.ConsecutiveDelegateFailures++
	if pack.Status.ConsecutiveDelegateFailures >= threshold {
		pack.Status.CircuitBreakerOpen = true
		sr.Log.Info("Circuit breaker OPEN",
			"ensemble", pack.Name,
			"failures", pack.Status.ConsecutiveDelegateFailures,
			"threshold", threshold,
		)
	}
	if err := sr.Client.Status().Patch(ctx, pack, patch); err != nil {
		sr.Log.Error(err, "failed to update circuit breaker status")
	}
}

// resetCircuitBreaker resets the consecutive failure counter on a successful
// delegate completion.
func (sr *SpawnRouter) resetCircuitBreaker(ctx context.Context, parentRunID string) {
	pack, err := sr.getEnsembleForRunByParent(ctx, parentRunID)
	if err != nil || pack == nil {
		return
	}
	if pack.Status.ConsecutiveDelegateFailures == 0 && !pack.Status.CircuitBreakerOpen {
		return
	}

	patch := client.MergeFrom(pack.DeepCopy())
	pack.Status.ConsecutiveDelegateFailures = 0
	pack.Status.CircuitBreakerOpen = false
	if err := sr.Client.Status().Patch(ctx, pack, patch); err != nil {
		sr.Log.Error(err, "failed to reset circuit breaker status")
	}
}

// getEnsembleForRun looks up the ensemble by name, resolving the namespace
// from the parent AgentRun.
func (sr *SpawnRouter) getEnsembleForRun(ctx context.Context, parentRunID, packName string) (*sympoziumv1alpha1.Ensemble, error) {
	var parentRun sympoziumv1alpha1.AgentRun
	if err := sr.Client.Get(ctx, types.NamespacedName{Name: parentRunID, Namespace: "default"}, &parentRun); err != nil {
		return nil, err
	}
	var pack sympoziumv1alpha1.Ensemble
	if err := sr.Client.Get(ctx, types.NamespacedName{Name: packName, Namespace: parentRun.Namespace}, &pack); err != nil {
		return nil, err
	}
	return &pack, nil
}

// getEnsembleForRunByParent resolves the ensemble from a parent AgentRun's labels.
func (sr *SpawnRouter) getEnsembleForRunByParent(ctx context.Context, parentRunID string) (*sympoziumv1alpha1.Ensemble, error) {
	var parentRun sympoziumv1alpha1.AgentRun
	if err := sr.Client.Get(ctx, types.NamespacedName{Name: parentRunID, Namespace: "default"}, &parentRun); err != nil {
		return nil, err
	}
	packName := parentRun.Labels["sympozium.ai/ensemble"]
	if packName == "" {
		return nil, nil
	}
	var pack sympoziumv1alpha1.Ensemble
	if err := sr.Client.Get(ctx, types.NamespacedName{Name: packName, Namespace: parentRun.Namespace}, &pack); err != nil {
		return nil, err
	}
	return &pack, nil
}
