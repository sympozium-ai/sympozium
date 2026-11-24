package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestActorLoopSurvivesRefusalsThenRunsAndReportsItsTurn(t *testing.T) {
	var mu sync.Mutex
	polls, reported := 0, make(chan actorTurnResult, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != actorPlaceholderCredential {
			t.Errorf("request must carry the placeholder for the egress gateway to replace, got %q", r.Header.Get("Authorization"))
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case actorTurnPath:
			polls++
			switch {
			case polls == 1:
				w.WriteHeader(http.StatusForbidden) // golden snapshot: no egress
			case polls == 2:
				w.WriteHeader(http.StatusNoContent)
			default:
				_ = json.NewEncoder(w).Encode(actorTurn{ID: "turn-1", Env: map[string]string{"TASK": "hi"}})
			}
		case actorResultPath:
			var res actorTurnResult
			_ = json.NewDecoder(r.Body).Decode(&res)
			w.WriteHeader(http.StatusNoContent)
			select {
			case reported <- res:
			default:
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loop := &actorLoop{base: srv.URL, client: newActorHTTPClient(), idle: time.Millisecond,
		runTurn: func(_ context.Context, turn actorTurn) actorTurnResult {
			return actorTurnResult{ID: turn.ID, Result: json.RawMessage(`{"status":"success"}`), Logs: turn.Env["TASK"]}
		}}
	go loop.run(ctx)

	select {
	case res := <-reported:
		if res.ID != "turn-1" || res.Logs != "hi" || string(res.Result) != `{"status":"success"}` {
			t.Fatalf("reported %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn was never reported")
	}
}

func TestActorChildEnvComesOnlyFromTheTurn(t *testing.T) {
	t.Setenv("LEFTOVER_FROM_SNAPSHOT", "x")
	t.Setenv("PATH", "/bin")
	env := actorChildEnv(actorTurn{Env: map[string]string{"TASK": "a=b", "PATH": "/evil", "BAD=KEY": "v", "": "v"}})
	sort.Strings(env)
	got := strings.Join(env, "|")
	if got != "HOME="+getEnv("HOME", "/tmp")+"|PATH=/bin|TASK=a=b" {
		t.Fatalf("child env = %s", got)
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := &tailBuffer{max: 4}
	_, _ = b.Write([]byte("abc"))
	_, _ = b.Write([]byte("defg"))
	if b.String() != "defg" {
		t.Fatalf("tail = %q", b.String())
	}
}
