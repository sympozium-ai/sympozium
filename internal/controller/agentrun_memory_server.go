package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// memoryStoreClient is a shared HTTP client for controller→memory-server calls.
var memoryStoreClient = &http.Client{Timeout: 5 * time.Second}

// memoryServerURLForRun builds the cluster-internal URL for the memory server
// that belongs to the given AgentRun's instance.
func memoryServerURLForRun(agentRun *sympoziumv1alpha1.AgentRun) string {
	return fmt.Sprintf("http://%s-memory.%s.svc:8080", agentRun.Spec.AgentRef, agentRun.Namespace)
}

// persistFailureMemory stores a structured failure record in the memory server
// so that subsequent runs can learn from past failures. It is fire-and-forget:
// errors are logged but never propagated to the caller.
func (r *AgentRunReconciler) persistFailureMemory(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun, reason string) {
	if !agentRunHasMemorySkill(agentRun) {
		return
	}

	task := agentRun.Spec.Task.GetPrompt()
	if len(task) > 500 {
		task = task[:500] + "..."
	}

	content := fmt.Sprintf(
		"## Failed AgentRun: %s\n**Task**: %s\n**Error**: %s\n**Timestamp**: %s\n**Instance**: %s",
		agentRun.Name,
		task,
		reason,
		time.Now().UTC().Format(time.RFC3339),
		agentRun.Spec.AgentRef,
	)

	body, _ := json.Marshal(map[string]any{
		"content": content,
		"tags":    []string{"failure", "agent-run", agentRun.Spec.AgentRef},
	})

	storeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	url := memoryServerURLForRun(agentRun) + "/store"
	req, err := http.NewRequestWithContext(storeCtx, "POST", url, bytes.NewReader(body))
	if err != nil {
		log.V(1).Info("failed to build memory store request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := memoryStoreClient.Do(req)
	if err != nil {
		log.V(1).Info("failed to persist failure memory", "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.V(1).Info("memory server returned non-200 for failure store", "status", resp.StatusCode)
		return
	}

	log.Info("Persisted failure memory to memory server", "agentrun", agentRun.Name)
}
