package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
)

func TestNormalizeThinking(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "off": "", "OFF": "", "none": "", "disabled": "", "false": "",
		"minimal": "minimal", "low": "low", " Medium ": "medium", "high": "high",
		"extreme": "", // unknown: off, not a failed run
	} {
		if got := normalizeThinking(in); got != want {
			t.Errorf("normalizeThinking(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseMaxTokens(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "4096": 4096, " 12000 ": 12000, "0": 0, "-5": 0, "abc": 0} {
		if got := parseMaxTokens(in); got != want {
			t.Errorf("parseMaxTokens(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseTemperature(t *testing.T) {
	for in, want := range map[string]float64{"0": 0, "0.2": 0.2, " 1.5 ": 1.5} {
		if got := parseTemperature(in); got != want {
			t.Errorf("parseTemperature(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"", "abc", "-0.1", "NaN", "Inf"} {
		if got := parseTemperature(in); !math.IsNaN(got) {
			t.Errorf("parseTemperature(%q) = %v, want NaN (provider default)", in, got)
		}
	}
}

// wireJSON marshals request params the way the SDK sends them.
func wireJSON(t *testing.T, params any) map[string]any {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	return m
}

func samplingOf(thinking string, maxTokens int64, temperature float64) samplingConfig {
	return samplingConfig{thinking: thinking, maxTokens: maxTokens, temperature: temperature}
}

func TestAnthropicApplySampling(t *testing.T) {
	nan := math.NaN()
	cases := []struct {
		name          string
		sampling      samplingConfig
		wantMaxTokens float64
		wantBudget    float64 // 0 = no thinking block
		wantTemp      *float64
	}{
		{name: "defaults", sampling: samplingOf("", 0, nan), wantMaxTokens: 8192},
		{name: "high grows default max_tokens for headroom", sampling: samplingOf("high", 0, nan), wantMaxTokens: 8192 + 4096, wantBudget: 8192},
		{name: "low fits in default", sampling: samplingOf("low", 0, nan), wantMaxTokens: 8192, wantBudget: 2048},
		{name: "explicit max_tokens wins, budget halves", sampling: samplingOf("high", 6000, nan), wantMaxTokens: 6000, wantBudget: 3000},
		{name: "explicit max_tokens too small disables thinking", sampling: samplingOf("low", 1500, nan), wantMaxTokens: 1500},
		{name: "temperature without thinking", sampling: samplingOf("", 0, 0.2), wantMaxTokens: 8192, wantTemp: ptrFloat(0.2)},
		{name: "temperature dropped with thinking", sampling: samplingOf("medium", 0, 0.2), wantMaxTokens: 8192, wantBudget: 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &anthropicProvider{sampling: tc.sampling}
			params := anthropic.MessageNewParams{Model: "claude-test"}
			p.applySampling(&params)
			body := wireJSON(t, params)

			if got := body["max_tokens"]; got != tc.wantMaxTokens {
				t.Errorf("max_tokens = %v, want %v", got, tc.wantMaxTokens)
			}
			thinking, hasThinking := body["thinking"].(map[string]any)
			switch {
			case tc.wantBudget == 0 && hasThinking:
				t.Errorf("thinking = %v, want absent", thinking)
			case tc.wantBudget > 0 && (!hasThinking || thinking["type"] != "enabled" || thinking["budget_tokens"] != tc.wantBudget):
				t.Errorf("thinking = %v, want enabled with budget %v", body["thinking"], tc.wantBudget)
			}
			if budget, ok := thinking["budget_tokens"].(float64); ok && budget >= body["max_tokens"].(float64) {
				t.Errorf("budget_tokens %v must be below max_tokens %v", budget, body["max_tokens"])
			}
			temp, hasTemp := body["temperature"]
			switch {
			case tc.wantTemp == nil && hasTemp:
				t.Errorf("temperature = %v, want absent", temp)
			case tc.wantTemp != nil && temp != *tc.wantTemp:
				t.Errorf("temperature = %v, want %v", temp, *tc.wantTemp)
			}
		})
	}
}

func TestOpenAIApplySampling(t *testing.T) {
	nan := math.NaN()
	cases := []struct {
		name     string
		provider string
		sampling samplingConfig
		latched  bool
		want     map[string]any
		absent   []string
	}{
		{name: "defaults send nothing", provider: "openai", sampling: samplingOf("", 0, nan),
			absent: []string{"reasoning_effort", "max_tokens", "max_completion_tokens", "temperature"}},
		{name: "hosted openai uses max_completion_tokens only", provider: "openai", sampling: samplingOf("high", 4000, 0.2),
			want:   map[string]any{"reasoning_effort": "high", "max_completion_tokens": 4000.0, "temperature": 0.2},
			absent: []string{"max_tokens"}},
		{name: "compatible servers use max_tokens only", provider: "ollama", sampling: samplingOf("", 4000, nan),
			want: map[string]any{"max_tokens": 4000.0}, absent: []string{"max_completion_tokens"}},
		{name: "azure uses max_tokens", provider: "azure-openai", sampling: samplingOf("", 4000, nan),
			want: map[string]any{"max_tokens": 4000.0}, absent: []string{"max_completion_tokens"}},
		{name: "latched run stops sending reasoning_effort", provider: "openai", sampling: samplingOf("high", 0, nan), latched: true,
			absent: []string{"reasoning_effort"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &openaiProvider{provider: tc.provider, sampling: tc.sampling, reasoningEffortUnsupported: tc.latched}
			params := openai.ChatCompletionNewParams{Model: "gpt-test"}
			p.applySampling(&params)
			body := wireJSON(t, params)
			for k, want := range tc.want {
				if got := body[k]; got != want {
					t.Errorf("%s = %v, want %v", k, got, want)
				}
			}
			for _, k := range tc.absent {
				if v, ok := body[k]; ok {
					t.Errorf("%s = %v, want absent", k, v)
				}
			}
		})
	}
}

// With extended thinking on, Anthropic rejects a tool-result follow-up unless
// the assistant turn it answers is sent back with its thinking block (and
// signature) intact, ahead of the tool_use block.
func TestCallAnthropic_ThinkingBlocksRoundTrip(t *testing.T) {
	t.Setenv("THINKING_MODE", "high")

	var mu sync.Mutex
	var bodies []map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		call := len(bodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test",
				"content": []map[string]any{
					{"type": "thinking", "thinking": "I should read the file first.", "signature": "sig-abc"},
					{"type": "tool_use", "id": "toolu_1", "name": "read_file", "input": map[string]string{"path": "PLACEHOLDER"}},
				},
				"stop_reason": "tool_use",
				"usage":       map[string]int{"input_tokens": 10, "output_tokens": 20},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_2", "type": "message", "role": "assistant", "model": "claude-test",
			"content":     []map[string]any{{"type": "text", "text": "done"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 30, "output_tokens": 5},
		})
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := []ToolDef{{
		Name: "read_file", Description: "Read a file",
		Parameters: map[string]any{
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
	}}

	text, _, _, _, err := callAnthropic(t.Context(), "key", srv.URL, "claude-test", "sys", "read "+file, tools, nil)
	if err != nil {
		t.Fatalf("callAnthropic: %v", err)
	}
	if text != "done" {
		t.Fatalf("text = %q, want done", text)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}

	first := bodies[0]
	thinking, _ := first["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != 8192.0 || first["max_tokens"] != 12288.0 {
		t.Fatalf("first request thinking=%v max_tokens=%v, want enabled/8192 with max_tokens 12288", first["thinking"], first["max_tokens"])
	}

	messages, _ := bodies[1]["messages"].([]any)
	if len(messages) < 3 {
		t.Fatalf("follow-up has %d messages, want user + assistant + tool_result", len(messages))
	}
	assistant, _ := messages[1].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("assistant turn has %d blocks, want thinking + tool_use: %v", len(blocks), blocks)
	}
	thinkingBlock, _ := blocks[0].(map[string]any)
	if thinkingBlock["type"] != "thinking" || thinkingBlock["signature"] != "sig-abc" || thinkingBlock["thinking"] != "I should read the file first." {
		t.Fatalf("first assistant block = %v, want the thinking block with its signature", thinkingBlock)
	}
	if toolUse, _ := blocks[1].(map[string]any); toolUse["type"] != "tool_use" {
		t.Fatalf("second assistant block = %v, want tool_use", toolUse)
	}
}

// A backend that rejects reasoning_effort costs one extra request, once per
// run: the provider retries without it and stops sending it afterwards.
func TestOpenAI_ReasoningEffortRejectedDegradesOncePerRun(t *testing.T) {
	rejections := map[string]map[string]string{
		"structured param": {"message": "Unsupported parameter: 'reasoning_effort' is not supported with this model.", "param": "reasoning_effort", "type": "invalid_request_error"},
		"message only":     {"message": "reasoning_effort is only supported with function tools on /v1/responses", "param": "", "type": "invalid_request_error"},
	}
	for name, rejection := range rejections {
		t.Run(name, func(t *testing.T) {
			t.Setenv("THINKING_MODE", "high")
			var mu sync.Mutex
			var sentEffort []bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				_, withEffort := body["reasoning_effort"]
				mu.Lock()
				sentEffort = append(sentEffort, withEffort)
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				if withEffort {
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]any{"error": rejection})
					return
				}
				json.NewEncoder(w).Encode(openAITextCompletion("ok"))
			}))
			defer srv.Close()

			p, err := newOpenAIProvider("openai", "key", srv.URL, "gpt-test", "sys", "task", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			for turn := 1; turn <= 2; turn++ {
				if res, err := p.Chat(t.Context()); err != nil || res.Text != "ok" {
					t.Fatalf("turn %d: text %q err %v, want ok", turn, res.Text, err)
				}
			}
			want := []bool{true, false, false} // rejected, retried without, never sent again
			if len(sentEffort) != len(want) {
				t.Fatalf("requests sent reasoning_effort = %v, want %v", sentEffort, want)
			}
			for i := range want {
				if sentEffort[i] != want[i] {
					t.Fatalf("requests sent reasoning_effort = %v, want %v", sentEffort, want)
				}
			}
		})
	}
}

// Only a reasoning_effort rejection is absorbed; any other 400 still fails
// the call after one request.
func TestOpenAI_OtherBadRequestIsNotRetried(t *testing.T) {
	t.Setenv("THINKING_MODE", "high")
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"message": "Invalid 'messages[1].content': string too long.", "param": "messages", "type": "invalid_request_error",
		}})
	}))
	defer srv.Close()

	p, err := newOpenAIProvider("openai", "key", srv.URL, "gpt-test", "sys", "task", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Chat(t.Context()); err == nil {
		t.Fatal("Chat succeeded on a 400, want error")
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want 1 (no retry for unrelated 400s)", calls)
	}
	if p.reasoningEffortUnsupported {
		t.Fatal("an unrelated 400 latched reasoning_effort off")
	}
}

func openAITextCompletion(text string) map[string]any {
	return map[string]any{
		"id": "chatcmpl-test", "object": "chat.completion", "created": 1, "model": "gpt-test",
		"choices": []map[string]any{{
			"index": 0, "finish_reason": "stop",
			"message": map[string]string{"role": "assistant", "content": text},
		}},
		"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
}

func ptrFloat(f float64) *float64 { return &f }
