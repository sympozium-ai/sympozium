package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// promptServiceDeps groups the inputs needed to build a fresh LLMProvider
// instance for the prompt service loop. Each handleRequest call constructs a
// new provider because Prompt() is destructive of the underlying messages
// slice when the call is stateless; reusing a single provider across
// concurrent prompts would race the slice. For the typical sidecar-driven
// pattern the sidecar issues prompts sequentially, so per-call construction
// is acceptable. When the run has already-completed message history (a
// server-mode run, for example) the sidecar should set appendContext=true so
// the prompt is recorded against history.
type promptServiceDeps struct {
	Ctx             context.Context
	ProviderName    string
	APIKey          string
	BaseURL         string
	Model           string
	SystemPrompt    string
	Task            string
	Tools           []ToolDef
	ProviderHeaders map[string]string
}

// runPromptServiceLoop keeps the agent-runner alive after the main agent
// loop completes so sidecars can drive individual LLM calls via the
// /ipc/prompts IPC channel. It also honors /ipc/context/clear-* IPC to reset
// conversation history between independent units of work.
//
// Behaviour:
//   - /ipc/prompts/request-{id}.json: parse, call provider.Prompt, then
//     write /ipc/prompts/result-{id}.json with the answer. With
//     USE_CONTEXT=true the prompt is appended to the provider's running
//     history; with USE_CONTEXT=false each prompt is stateless.
//   - /ipc/context/clear-{id}.json: call provider.ResetContext. The next
//     request served by the same loop will start from a fresh history. No
//     result file is written (fire-and-forget).
//
// Exits when /ipc/done is written or when the context is cancelled.
func runPromptServiceLoop(deps promptServiceDeps) error {
	promptsDir := "/ipc/prompts"
	contextsDir := "/ipc/context"
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", promptsDir, err)
	}
	if err := os.MkdirAll(contextsDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", contextsDir, err)
	}

	useContext := true
	if v := os.Getenv("USE_CONTEXT"); v != "" {
		useContext = strings.EqualFold(v, "true") || v == "1"
	}
	log.Printf("prompt service starting — useContext=%v provider=%s model=%s", useContext, deps.ProviderName, deps.Model)

	p, cleanup, err := buildLLMProvider(deps)
	if err != nil {
		return fmt.Errorf("build provider: %w", err)
	}
	defer cleanup()

	var mu sync.Mutex

	processRequest := func(req ipcPromptRequest) {
		mu.Lock()
		defer mu.Unlock()

		log.Printf("prompt service: requestId=%s schema?=%v promptLen=%d",
			req.RequestID, len(req.Schema) > 0, len(req.Prompt))

		text, parsed, inTok, outTok, err := p.Prompt(deps.Ctx, req.Prompt, useContext, req.Schema)
		result := ipcPromptResult{
			RequestID: req.RequestID,
			Status:    "success",
			Content:   text,
			Parsed:    parsed,
		}
		result.Metrics.InputTokens = inTok
		result.Metrics.OutputTokens = outTok
		if err != nil {
			result.Status = "error"
			result.Error = err.Error()
		}
		writePromptResult(promptsDir, req.RequestID, result)
		log.Printf("prompt service: requestId=%s status=%s inTok=%d outTok=%d",
			req.RequestID, result.Status, result.Metrics.InputTokens, result.Metrics.OutputTokens)
	}

	processContextClear := func(req ipcClearContextRequest) {
		mu.Lock()
		defer mu.Unlock()
		p.ResetContext()
		log.Printf("prompt service: context cleared (requestId=%s reason=%q)", req.RequestID, req.Reason)
	}

	knownRequests := map[string]bool{}
	knownClears := map[string]bool{}

	poll := time.NewTicker(150 * time.Millisecond)
	defer poll.Stop()

	doneCh := make(chan struct{})
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-deps.Ctx.Done():
				return
			case <-t.C:
				if _, err := os.Stat("/ipc/done"); err == nil {
					// Block on the send so the outer select reliably observes
					// doneCh before this goroutine exits. The non-blocking
					// variant with `default:` races with the outer loop's
					// poll.C case: if the outer loop is busy in processRequest
					// when we signal done, the signal is dropped, the outer
					// loop never sees /ipc/done, and the agent-runner hangs
					// after the agent loop finishes (defer promptWg.Wait
					// blocks forever).
					doneCh <- struct{}{}
					return
				}
			}
		}
	}()

	for {
		select {
		case <-deps.Ctx.Done():
			return deps.Ctx.Err()
		case <-doneCh:
			log.Println("prompt service: /ipc/done observed, exiting")
			return nil
		case <-poll.C:
			entries, err := os.ReadDir(promptsDir)
			if err == nil {
				for _, e := range entries {
					name := e.Name()
					if !strings.HasPrefix(name, "request-") || knownRequests[name] {
						continue
					}
					knownRequests[name] = true
					path := filepath.Join(promptsDir, name)
					data, err := os.ReadFile(path)
					if err != nil {
						continue
					}
					var req ipcPromptRequest
					if err := json.Unmarshal(data, &req); err != nil {
						log.Printf("prompt service: dropping invalid request %s: %v", name, err)
						continue
					}
					processRequest(req)
				}
			}

			entries, err = os.ReadDir(contextsDir)
			if err == nil {
				for _, e := range entries {
					name := e.Name()
					if !strings.HasPrefix(name, "clear-") || knownClears[name] {
						continue
					}
					knownClears[name] = true
					path := filepath.Join(contextsDir, name)
					data, err := os.ReadFile(path)
					if err != nil {
						continue
					}
					var req ipcClearContextRequest
					if err := json.Unmarshal(data, &req); err != nil {
						log.Printf("prompt service: dropping invalid clear request %s: %v", name, err)
						continue
					}
					processContextClear(req)
				}
			}
		}
	}
}

// ipcPromptRequest mirrors ipc.PromptRequest locally so this package can
// stay independent of the bridge import. Field names match the bridge
// protocol so a JSON dropped by a sidecar written against the bridge types
// is parsed verbatim.
type ipcPromptRequest struct {
	RequestID string          `json:"requestId"`
	Prompt    string          `json:"prompt"`
	Schema    json.RawMessage `json:"schema,omitempty"`
}

// ipcClearContextRequest mirrors ipc.ClearContextRequest locally.
type ipcClearContextRequest struct {
	RequestID string `json:"requestId"`
	Reason    string `json:"reason,omitempty"`
}

// ipcPromptResult mirrors ipc.PromptResult with the same field names.
type ipcPromptResult struct {
	RequestID string          `json:"requestId"`
	Status    string          `json:"status"`
	Content   string          `json:"content,omitempty"`
	Parsed    json.RawMessage `json:"parsed,omitempty"`
	Error     string          `json:"error,omitempty"`
	Metrics   struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"metrics,omitempty"`
}

func promptErrorResult(requestID string, err error) ipcPromptResult {
	return ipcPromptResult{
		RequestID: requestID,
		Status:    "error",
		Error:     err.Error(),
	}
}

func writePromptResult(dir, requestID string, result ipcPromptResult) {
	if requestID == "" {
		return
	}
	path := filepath.Join(dir, "result-"+requestID+".json")
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Printf("prompt service: failed to marshal result: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("prompt service: failed to write %s: %v", path, err)
	}
}

// buildLLMProvider constructs a fresh LLMProvider for the prompt service
// loop. The cleanup function returned by the helper holds any per-provider
// resources that need explicit teardown (none today, but the seam is here
// for future providers).
func buildLLMProvider(deps promptServiceDeps) (LLMProvider, func(), error) {
	switch strings.ToLower(deps.ProviderName) {
	case "anthropic":
		return newAnthropicProvider(deps.APIKey, deps.BaseURL, deps.Model, deps.SystemPrompt, deps.Task, deps.Tools, deps.ProviderHeaders), func() {}, nil
	case "bedrock":
		p, err := newBedrockProvider(deps.Ctx, deps.Model, deps.SystemPrompt, deps.Task, deps.Tools)
		if err != nil {
			return nil, nil, err
		}
		return p, func() {}, nil
	default:
		p, err := newOpenAIProvider(deps.ProviderName, deps.APIKey, deps.BaseURL, deps.Model, deps.SystemPrompt, deps.Task, deps.Tools, deps.ProviderHeaders)
		if err != nil {
			return nil, nil, err
		}
		return p, func() {}, nil
	}
}
