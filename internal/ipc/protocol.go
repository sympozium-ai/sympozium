package ipc

import "encoding/json"

// Protocol types for IPC file-based communication.

// TaskInput is written to /ipc/input/task.json by the orchestrator.
type TaskInput struct {
	Task         string          `json:"task"`
	SystemPrompt string          `json:"systemPrompt,omitempty"`
	AgentID      string          `json:"agentId"`
	SessionKey   string          `json:"sessionKey"`
	Model        ModelConfig     `json:"model"`
	Tools        []string        `json:"tools,omitempty"`
	Context      json.RawMessage `json:"context,omitempty"`
}

// ModelConfig specifies the LLM configuration.
type ModelConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Thinking string `json:"thinking,omitempty"`
}

// AgentResult is written to /ipc/output/result.json by the agent on completion.
type AgentResult struct {
	Status   string `json:"status"` // "success" or "error"
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
	Metrics  struct {
		DurationMs     int64 `json:"durationMs"`
		InputTokens    int   `json:"inputTokens"`
		OutputTokens   int   `json:"outputTokens"`
		ToolCalls      int   `json:"toolCalls"`
		SubagentSpawns int   `json:"subagentSpawns"`
	} `json:"metrics"`
}

// StreamChunk is written to /ipc/output/stream-*.json for streaming responses.
type StreamChunk struct {
	Type    string `json:"type"` // "text", "thinking", "tool_use", "tool_result"
	Content string `json:"content"`
	ToolID  string `json:"toolId,omitempty"`
	Index   int    `json:"index"`
}

// SpawnRequest is written to /ipc/spawn/request-*.json to request sub-agent creation.
type SpawnRequest struct {
	// RequestID correlates this spawn request with the result delivered back
	// to the caller. The delegate tool blocks until result-{RequestID}.json appears.
	RequestID string `json:"requestId,omitempty"`

	Task         string   `json:"task"`
	SystemPrompt string   `json:"systemPrompt,omitempty"`
	AgentID      string   `json:"agentId"`
	Skills       []string `json:"skills,omitempty"`

	// TargetPersona enables persona-aware delegation. When set, the spawner
	// resolves this to the correct Agent via the Ensemble.
	TargetPersona string `json:"targetPersona,omitempty"`

	// PackName is the Ensemble containing both the source and target personas.
	// Required when TargetPersona is set.
	PackName string `json:"packName,omitempty"`
}

// DelegateResult is written to /ipc/spawn/result-{requestID}.json by the IPC
// bridge when a delegated child run completes. The delegate_to_persona tool
// polls for this file to deliver the result back to the LLM.
type DelegateResult struct {
	RequestID string `json:"requestId"`
	Status    string `json:"status"` // "success" or "error"
	Response  string `json:"response,omitempty"`
	Error     string `json:"error,omitempty"`
}

// SubagentTask defines a single sub-agent task within a batch spawn request.
type SubagentTask struct {
	// ID is a caller-assigned identifier for correlating results.
	ID string `json:"id"`

	// Task is the task description for the sub-agent.
	Task string `json:"task"`

	// SystemPrompt overrides the parent's system prompt. Empty = inherit parent.
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Timeout overrides the parent's run timeout (e.g. "5m"). Empty = inherit parent.
	Timeout string `json:"timeout,omitempty"`
}

// SubagentSpawnRequest is written to /ipc/spawn/subagent-request-{batchId}.json
// by the spawn_subagents tool. The IPC bridge forwards it to the SpawnRouter
// which creates child AgentRun CRs.
type SubagentSpawnRequest struct {
	// BatchID correlates the request with its result.
	BatchID string `json:"batchId"`

	// Strategy is "parallel" (all at once) or "sequential" (one after another).
	Strategy string `json:"strategy"`

	// FailurePolicy is "continue" (run all, report failures) or "fail-fast"
	// (stop on first failure). Defaults: continue for parallel, fail-fast for sequential.
	FailurePolicy string `json:"failurePolicy"`

	// Tasks is the list of sub-agent tasks to spawn.
	Tasks []SubagentTask `json:"tasks"`
}

// SubagentChildResult is a single child's outcome within a batch.
type SubagentChildResult struct {
	// ID matches the SubagentTask.ID from the request.
	ID string `json:"id"`

	// RunName is the name of the child AgentRun CR.
	RunName string `json:"runName"`

	// Status is "success" or "error".
	Status string `json:"status"`

	// Response is populated on success.
	Response string `json:"response,omitempty"`

	// Error is populated on failure.
	Error string `json:"error,omitempty"`
}

// SubagentBatchResult is written to /ipc/spawn/subagent-result-{batchId}.json
// by the IPC bridge when all children in a batch complete.
type SubagentBatchResult struct {
	// BatchID matches the SubagentSpawnRequest.BatchID.
	BatchID string `json:"batchId"`

	// Status is "success" (all succeeded), "partial" (some failed), or "error" (all failed or rejected).
	Status string `json:"status"`

	// Results contains one entry per task, ordered to match the request's Tasks array.
	Results []SubagentChildResult `json:"results"`
}

// ExecRequest is written to /ipc/tools/exec-request-*.json for sandbox execution.
type ExecRequest struct {
	ID      string            `json:"id"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	WorkDir string            `json:"workDir,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // seconds
	Stdin   string            `json:"stdin,omitempty"`
	Meta    map[string]string `json:"_meta,omitempty"`
}

// ExecResult is written to /ipc/tools/exec-result-*.json with execution results.
type ExecResult struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut,omitempty"`
}

// OutboundMessage is written to /ipc/messages/send-*.json for channel delivery.
// Field names align with channel.OutboundMessage so the bridge can relay the
// JSON directly without remapping.
type OutboundMessage struct {
	Channel  string          `json:"channel"`          // "telegram", "whatsapp", etc.
	ChatID   string          `json:"chatId,omitempty"` // Chat/group ID; empty = owner/self
	Text     string          `json:"text"`
	Format   string          `json:"format,omitempty"` // "plain", "markdown", "html"
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// StatusUpdate is written to /ipc/output/status.json for agent status.
type StatusUpdate struct {
	Phase   string `json:"phase"` // "thinking", "tool_use", "responding"
	Message string `json:"message,omitempty"`
	ToolID  string `json:"toolId,omitempty"`
}
