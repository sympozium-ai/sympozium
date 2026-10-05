// Command fake-model is a deterministic OpenAI-compatible chat provider for
// the Kind integration lane (issue #471). It is not an LLM and is never a
// production provider.
//
// The lane exists to prove Sympozium's persistence, restart, resume,
// cancellation and cleanup behaviour. A 1B local model made those proofs
// flaky by misremembering an exact token even when the persisted history was
// intact. This fixture answers only from the request it is sent, so a recall
// succeeds exactly when the harness supplied the stored conversation and
// fails, with a distinct sentinel, when that history was lost:
//
//   - a final user turn asking "What exact token did I ask you to remember"
//     returns the most recent token from an earlier
//     "Remember this exact token for the next turn: <token>" turn in the same
//     request, or NoTokenSentinel when no earlier turn carries one;
//   - a final user turn containing that remember phrase returns "STORED";
//   - a final user turn containing SLOW:<seconds> streams one byte per second
//     for that long (or until the caller disconnects), so client-disconnect
//     cancellation can be exercised without racing a fast answer;
//   - "Reply with <WORD> only" returns WORD; anything else returns "ACK".
//
// GET /v1/models advertises a 128K context window because Hermes Agent
// refuses models that report less than 64K.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// NoTokenSentinel is returned to a recall request whose history carries
	// no remembered token. It never contains a real token, so a harness that
	// lost the stored conversation fails the exact-token assertion.
	NoTokenSentinel = "FIXTURE-NO-TOKEN-IN-HISTORY"
	// ContextWindow is advertised from /v1/models.
	ContextWindow = 131072
	maxSlow       = 10 * time.Minute
	maxBody       = 4 << 20
	recallPhrase  = "what exact token did i ask you to remember"
	// hermesTurnPrefix is how the Hermes session adapter flattens a stored
	// transcript into one user message ("User: ...\nAssistant: ...").
	hermesTurnPrefix = "\nUser: "
)

var (
	rememberPattern = regexp.MustCompile(`(?i)remember this exact token for the next turn:\s*([A-Za-z0-9][A-Za-z0-9_-]*[A-Za-z0-9])`)
	slowPattern     = regexp.MustCompile(`\bSLOW:(\d{1,4})\b`)
	replyPattern    = regexp.MustCompile(`(?i)reply with ([A-Za-z0-9_-]+) only`)
)

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatRequest struct {
	Model         string    `json:"model"`
	Messages      []message `json:"messages"`
	Stream        bool      `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// Reply is the fixture's deterministic answer to one request.
type Reply struct {
	Text string
	Slow time.Duration
}

// text returns a message's textual content, accepting both the string form
// and the array-of-parts form of the chat completions API.
func (m message) text() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// split separates the final user prompt from everything that precedes it in
// the request. Hermes flattens its stored transcript into the last user
// message, so the final prompt is the text after its last "User: " marker.
func split(messages []message) (history, final string) {
	last := -1
	for i, m := range messages {
		if m.Role == "user" {
			last = i
		}
	}
	if last < 0 {
		return "", ""
	}
	var b strings.Builder
	for _, m := range messages[:last] {
		b.WriteString(m.text())
		b.WriteString("\n")
	}
	content := messages[last].text()
	if i := strings.LastIndex(content, hermesTurnPrefix); i >= 0 {
		b.WriteString(content[:i])
		content = content[i+len(hermesTurnPrefix):]
		content = strings.TrimSuffix(strings.TrimRight(content, " \n"), "\nAssistant:")
	}
	// Messages after the last user turn (none in practice) are not history.
	return b.String(), strings.TrimSpace(content)
}

// Respond computes the deterministic answer for a conversation.
func Respond(messages []message) Reply {
	history, final := split(messages)
	lower := strings.ToLower(final)
	switch {
	case strings.Contains(lower, recallPhrase):
		matches := rememberPattern.FindAllStringSubmatch(history, -1)
		if len(matches) == 0 {
			return Reply{Text: NoTokenSentinel}
		}
		return Reply{Text: matches[len(matches)-1][1]}
	case rememberPattern.MatchString(final):
		return Reply{Text: "STORED"}
	}
	if m := slowPattern.FindStringSubmatch(final); m != nil {
		seconds, _ := strconv.Atoi(m[1])
		d := time.Duration(seconds) * time.Second
		if d > maxSlow {
			d = maxSlow
		}
		return Reply{Text: "SLOW-DONE", Slow: d}
	}
	if m := replyPattern.FindStringSubmatch(final); m != nil {
		return Reply{Text: m[1]}
	}
	return Reply{Text: "ACK"}
}

type server struct {
	requests, cancelled, recallMisses atomic.Int64
	tick                              time.Duration
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /models", s.models)
	mux.HandleFunc("POST /v1/chat/completions", s.chat)
	mux.HandleFunc("POST /chat/completions", s.chat)
	mux.HandleFunc("GET /fixture/stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]int64{"requests": s.requests.Load(), "cancelled": s.cancelled.Load(), "recallMisses": s.recallMisses.Load()})
	})
	return mux
}

func (s *server) models(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("model")
	if id == "" {
		id = "fake-model"
	}
	writeJSON(w, map[string]any{"object": "list", "data": []map[string]any{{
		"id": id, "object": "model", "owned_by": "sympozium-fixture",
		"context_length": ContextWindow, "max_model_len": ContextWindow,
	}}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "invalid request: " + err.Error(), "type": "invalid_request_error"}})
		return
	}
	n := s.requests.Add(1)
	reply := Respond(req.Messages)
	if reply.Text == NoTokenSentinel {
		s.recallMisses.Add(1)
	}
	log.Printf("request=%d stream=%t messages=%d reply=%q slow=%s", n, req.Stream, len(req.Messages), reply.Text, reply.Slow)
	model := req.Model
	if model == "" {
		model = "fake-model"
	}
	id := fmt.Sprintf("chatcmpl-fixture-%d", n)
	created := time.Now().Unix()
	usage := map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}

	if !req.Stream {
		if reply.Slow > 0 && !s.wait(r, reply.Slow, nil) {
			s.cancel(n)
			return
		}
		writeJSON(w, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": reply.Text}, "finish_reason": "stop"}},
			"usage":   usage,
		})
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk := func(delta map[string]string, finish any) map[string]any {
		return map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	send(chunk(map[string]string{"role": "assistant", "content": ""}, nil))
	if reply.Slow > 0 && !s.wait(r, reply.Slow, func() { send(chunk(map[string]string{"content": "."}, nil)) }) {
		s.cancel(n)
		return
	}
	send(chunk(map[string]string{"content": reply.Text}, nil))
	send(chunk(map[string]string{}, "stop"))
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		send(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{}, "usage": usage})
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// wait holds a slow request open for d, calling each tick between intervals.
// It returns false when the caller disconnected first.
func (s *server) wait(r *http.Request, d time.Duration, each func()) bool {
	tick := s.tick
	if tick <= 0 {
		tick = time.Second
	}
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return false
		case <-deadline.C:
			return true
		case <-ticker.C:
			if each != nil {
				each()
			}
		}
	}
}

func (s *server) cancel(n int64) {
	s.cancelled.Add(1)
	log.Printf("request=%d cancelled by caller before completion", n)
}

func main() {
	listen := flag.String("listen", ":8080", "listen address")
	flag.Parse()
	srv := &http.Server{Addr: *listen, Handler: (&server{}).routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("fake-model listening on %s", *listen)
	log.Fatal(srv.ListenAndServe())
}
