package main

import (
	"context"
	"encoding/json"
	"io"
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

// Inside an actor a tool that acts outside the sandbox is a request to the
// control plane carrying the placeholder credential, and the model is told
// what the control plane decided.
func TestActorToolIsARequestToTheControlPlane(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	status, reply := http.StatusOK, `{"result":"Message sent to slack channel"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotAuth, gotBody = r.URL.Path, r.Header.Get("Authorization"), string(b)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	t.Setenv(actorToolsURLEnv, srv.URL)
	t.Setenv("ACTOR_TOOLS", "send_channel_message")

	out := executeToolCall(context.Background(), ToolSendChannelMessage, `{"channel":"slack","text":"hi"}`)
	if out != "Message sent to slack channel" {
		t.Fatalf("result = %q", out)
	}
	if gotPath != "/v1/actor/tools/send_channel_message" || gotAuth != actorPlaceholderCredential || !strings.Contains(gotBody, `"text":"hi"`) {
		t.Fatalf("request = %s auth=%q body=%s", gotPath, gotAuth, gotBody)
	}

	status, reply = http.StatusForbidden, `{"error":"tool \"send_channel_message\" is denied by policy"}`
	if out := executeToolCall(context.Background(), ToolSendChannelMessage, `{"channel":"slack","text":"hi"}`); !strings.Contains(out, "denied by policy") {
		t.Errorf("refusal reached the model as %q", out)
	}

	// A tool the run was not offered never leaves the process, and never
	// falls back to the pod's file protocol.
	gotPath = ""
	for _, tool := range []string{ToolScheduleTask, ToolExecuteCommand, ToolFetchURL, ToolWriteFile} {
		if out := executeToolCall(context.Background(), tool, `{"name":"n","action":"delete","command":"id","url":"http://x","path":"/tmp/x","content":"y"}`); !strings.Contains(out, "not available") {
			t.Errorf("%s: %q, want it refused", tool, out)
		}
	}
	if gotPath != "" {
		t.Errorf("an unoffered tool made a request to %s", gotPath)
	}
}

// The turn cannot redirect tool calls: where they go is fixed by the actor's
// own environment.
func TestTurnCannotRedirectToolCalls(t *testing.T) {
	t.Setenv("ACTOR_TURN_URL", "http://controller.svc:8090/")
	env := actorChildEnv(actorTurn{Env: map[string]string{actorToolsURLEnv: "http://attacker.example", "TASK": "t"}})
	var urls []string
	for _, kv := range env {
		if strings.HasPrefix(kv, actorToolsURLEnv+"=") {
			urls = append(urls, kv)
		}
	}
	if len(urls) != 1 || urls[0] != actorToolsURLEnv+"=http://controller.svc:8090" {
		t.Fatalf("%s = %v, want only the actor's own turn endpoint", actorToolsURLEnv, urls)
	}
}

// An actor has no mcp-bridge, so the runner must not wait for its manifest:
// that wait is five seconds, more than ten times the rest of an actor run.
func TestActorDoesNotWaitForASidecarManifest(t *testing.T) {
	t.Setenv(actorToolsURLEnv, "http://controller.svc:8090")
	start := time.Now()
	if tools := loadActorlessMCPTools(); tools != nil {
		t.Fatalf("tools = %v, want none", tools)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("loading sidecar tools in an actor took %s", took)
	}
}
