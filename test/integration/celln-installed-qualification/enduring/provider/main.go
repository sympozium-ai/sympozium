// Deterministic test provider: not a real LLM. Never logs headers or credentials.
package main

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}
type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []message `json:"messages"`
	Tools     []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}
type event struct {
	Turn             int    `json:"turn"`
	Stage            string `json:"stage"`
	HistoryExchanges int    `json:"historyExchanges"`
	Accepted         bool   `json:"accepted"`
	Reason           string `json:"reason,omitempty"`
}
type fixture struct {
	mu             sync.Mutex
	prefix, bearer string
	Attempts       int     `json:"attempts"`
	Events         []event `json:"events"`
}

func text(m message) string { var s string; _ = json.Unmarshal(m.Content, &s); return s }
func (f *fixture) validate(q request) (event, string, string, error) {
	e := event{}
	fail := func(reason string) (event, string, string, error) {
		e.Reason = reason
		return e, "", "", fmt.Errorf("%s", reason)
	}
	if q.MaxTokens != 512 {
		return fail("output-bound")
	}
	if q.Model != "review-uppercase" || len(q.Tools) != 1 || q.Tools[0].Function.Name == "" {
		return fail("model-or-tool")
	}
	m := q.Messages
	if len(m) > 0 && m[0].Role == "system" {
		m = m[1:]
	}
	last := -1
	for i, v := range m {
		if v.Role == "user" {
			last = i
		}
	}
	if last < 0 {
		return fail("missing-user")
	}
	user := text(m[last])
	turn := 0
	for i := 1; i <= 3; i++ {
		if user == fmt.Sprintf("%s-turn-%d", f.prefix, i) {
			turn = i
		}
	}
	if turn == 0 {
		return fail("foreign-or-out-of-range-user")
	}
	e.Turn = turn
	e.HistoryExchanges = turn - 1
	if last != 2*(turn-1) {
		return fail("history-length")
	}
	for i := 1; i < turn; i++ {
		expected := fmt.Sprintf("%s-turn-%d", f.prefix, i)
		if m[2*(i-1)].Role != "user" || text(m[2*(i-1)]) != expected || m[2*(i-1)+1].Role != "assistant" || text(m[2*(i-1)+1]) != strings.ToUpper(expected) {
			return fail("history-content")
		}
	}
	if len(m) == last+1 {
		e.Stage = "tool-call"
		e.Accepted = true
		return e, user, "", nil
	}
	if len(m) != last+3 || m[last+1].Role != "assistant" || m[last+2].Role != "tool" {
		return fail("tool-round-shape")
	}
	var result struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(text(m[last+2])), &result) != nil || result.Text != strings.ToUpper(user) {
		return fail("tool-result")
	}
	e.Stage = "answer"
	e.Accepted = true
	return e, user, result.Text, nil
}
func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		w.WriteHeader(200)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/qualification-stats" {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Attempts++
	reject := func(e event) {
		f.Events = append(f.Events, e)
		http.Error(w, "deterministic qualification rejected: "+e.Reason, 400)
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(f.bearer)) != 1 {
		reject(event{Reason: "authorization"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 262145))
	var q request
	if err != nil || len(raw) > 262144 || json.Unmarshal(raw, &q) != nil {
		reject(event{Reason: "request-shape"})
		return
	}
	e, user, result, err := f.validate(q)
	if err != nil {
		reject(e)
		return
	}
	f.Events = append(f.Events, e)
	var response any
	finish := "stop"
	if result != "" {
		response = map[string]any{"role": "assistant", "content": result}
	} else {
		args, _ := json.Marshal(map[string]string{"text": user})
		finish = "tool_calls"
		response = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": fmt.Sprintf("enduring-tool-%d", e.Turn), "type": "function", "function": map[string]any{"name": q.Tools[0].Function.Name, "arguments": string(args)}}}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "deterministic-enduring-qualification", "object": "chat.completion", "model": q.Model, "choices": []any{map[string]any{"index": 0, "message": response, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 8, "total_tokens": 9}})
}
func main() {
	listen := flag.String("listen", ":8443", "TLS listener")
	cert := flag.String("cert", "", "certificate")
	key := flag.String("key", "", "private key path")
	token := flag.String("token-file", "", "generated fake credential path")
	prefix := flag.String("run-prefix", "", "exact public test sentinel prefix")
	flag.Parse()
	b, err := os.ReadFile(*token)
	if err != nil || len(strings.TrimSpace(string(b))) < 24 || *prefix == "" {
		log.Fatal("invalid deterministic fixture configuration")
	}
	f := &fixture{prefix: *prefix, bearer: "Bearer " + strings.TrimSpace(string(b)), Events: []event{}}
	s := &http.Server{Addr: *listen, Handler: f, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, MaxHeaderBytes: 16384, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	log.Print("deterministic enduring context fixture; not a real LLM; sanitized counters enabled")
	if s.ListenAndServeTLS(*cert, *key) != nil {
		log.Fatal("fixture stopped")
	}
}
