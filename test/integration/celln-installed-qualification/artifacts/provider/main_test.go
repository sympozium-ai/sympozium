package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func artifactRequest(t *testing.T, prefix string, turn int, result string) request {
	t.Helper()
	messages := []map[string]any{{"role": "system", "content": "fixture"}}
	for i := 1; i < turn; i++ {
		messages = append(messages, map[string]any{"role": "user", "content": fmt.Sprintf("%s-turn-%d", prefix, i)}, map[string]any{"role": "assistant", "content": prefix})
	}
	messages = append(messages, map[string]any{"role": "user", "content": fmt.Sprintf("%s-turn-%d", prefix, turn)})
	if result != "" {
		messages = append(messages, map[string]any{"role": "assistant", "content": nil}, map[string]any{"role": "tool", "content": result})
	}
	raw, err := json.Marshal(map[string]any{"model": "review-uppercase", "max_tokens": 512, "messages": messages, "tools": []any{map[string]any{"function": map[string]any{"name": "workspace-read"}}, map[string]any{"function": map[string]any{"name": "workspace-write"}}}})
	if err != nil {
		t.Fatal(err)
	}
	var q request
	if err = json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	return q
}

func TestArtifactFixtureRequiresActualToolReplies(t *testing.T) {
	f := &fixture{prefix: "tenant-a-sentinel"}
	for _, tc := range []struct {
		turn     int
		result   string
		accepted bool
	}{
		{1, `{"revision":1}`, true},
		{2, `{"revision":1,"content":"tenant-a-sentinel"}`, true},
		{3, `{"revision":1,"content":"tenant-a-sentinel"}`, true},
		{2, `{"revision":1,"content":"tenant-b-sentinel"}`, false},
		{2, `{"revision":0,"content":"tenant-a-sentinel"}`, false},
		{1, `{"revision":1,"content":"forged"}`, false},
		{2, `{"revision":1,"content":"tenant-a-sentinel","error":"denied"}`, false},
		{2, `"tenant-a-sentinel"`, false},
	} {
		q := artifactRequest(t, f.prefix, tc.turn, tc.result)
		e, _, _, err := f.validate(q)
		if (err == nil) != tc.accepted || e.ToolResultVerified != tc.accepted {
			t.Fatalf("turn=%d result=%s verified=%v error=%v", tc.turn, tc.result, e.ToolResultVerified, err)
		}
	}
}

func TestArtifactFixtureBoundsIndependentAttempts(t *testing.T) {
	f := &fixture{prefix: "tenant-a", bearer: "Bearer fixture-only-credential", Events: []event{}}
	for i := 0; i < 7; i++ {
		turn := i/2 + 1
		if turn > 3 {
			turn = 3
		}
		result := ""
		if i%2 == 1 {
			result = `{"revision":1}`
			if turn > 1 {
				result = `{"revision":1,"content":"tenant-a"}`
			}
		}
		q := artifactRequest(t, f.prefix, turn, result)
		raw, _ := json.Marshal(q)
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(raw)))
		r.Header.Set("Authorization", f.bearer)
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		if (w.Code == http.StatusOK) != (i < 6) {
			t.Fatalf("attempt %d status=%d", i+1, w.Code)
		}
	}
	if f.Attempts != 7 || len(f.Events) != 7 || f.Events[6].Accepted {
		t.Fatal("counter or refusal lost")
	}
}
