package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func msg(role, s string) message { b, _ := json.Marshal(s); return message{role, b} }
func valid(turn int, answer bool) request {
	var q request
	q.Model = "review-uppercase"
	q.MaxTokens = 512
	_ = json.Unmarshal([]byte(`[{"function":{"name":"uppercase"}}]`), &q.Tools)
	for i := 1; i < turn; i++ {
		s := fmt.Sprintf("tenant-a-unique-turn-%d", i)
		q.Messages = append(q.Messages, msg("user", s), msg("assistant", strings.ToUpper(s)))
	}
	s := fmt.Sprintf("tenant-a-unique-turn-%d", turn)
	q.Messages = append(q.Messages, msg("user", s))
	if answer {
		b, _ := json.Marshal(map[string]string{"text": strings.ToUpper(s)})
		q.Messages = append(q.Messages, msg("assistant", ""), msg("tool", string(b)))
	}
	return q
}
func TestHistory(t *testing.T) {
	f := fixture{prefix: "tenant-a-unique"}
	for i := 1; i <= 3; i++ {
		for _, a := range []bool{false, true} {
			e, _, _, err := f.validate(valid(i, a))
			if err != nil || !e.Accepted || e.HistoryExchanges != i-1 {
				t.Fatal(e, err)
			}
		}
	}
}
func TestRejectHistory(t *testing.T) {
	f := fixture{prefix: "tenant-a-unique"}
	for _, alter := range []func(*request){func(q *request) { q.Messages = q.Messages[2:] }, func(q *request) { q.Messages[1] = msg("assistant", "wrong") }, func(q *request) { q.Messages[0] = msg("user", "tenant-b-unique-turn-1") }, func(q *request) { q.MaxTokens = 513 }} {
		q := valid(3, false)
		alter(&q)
		if _, _, _, err := f.validate(q); err == nil {
			t.Fatal("accepted invalid history/budget")
		}
	}
}
func TestIndependentCountersSanitized(t *testing.T) {
	f := fixture{prefix: "tenant-a-unique", bearer: "Bearer secret-never-output"}
	q := valid(1, false)
	b, _ := json.Marshal(q)
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(b)))
	r.Header.Set("Authorization", f.bearer)
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	if w.Code != 200 || f.Attempts != 1 {
		t.Fatal(w.Code, f.Attempts)
	}
	r = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("bad"))
	f.ServeHTTP(httptest.NewRecorder(), r)
	w = httptest.NewRecorder()
	f.ServeHTTP(w, httptest.NewRequest("GET", "/qualification-stats", nil))
	if f.Attempts != 2 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "Bearer") {
		t.Fatal("counter/sanitization failure")
	}
}
