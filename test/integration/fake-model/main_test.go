package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	token        = "sympozium-1790601097"
	rememberText = "Remember this exact token for the next turn: " + token + ". Reply only STORED."
	recallText   = "What exact token did I ask you to remember? Reply with the token only."
)

func msg(role, content string) message {
	b, _ := json.Marshal(content)
	return message{Role: role, Content: b}
}

// hermesPrompt mirrors prompt_with_history in the Hermes session adapter.
func hermesPrompt(turns [][2]string, prompt string) string {
	lines := []string{"Continue this conversation. Treat the transcript as context and answer the final User message.", ""}
	for _, t := range turns {
		lines = append(lines, strings.ToUpper(t[0][:1])+t[0][1:]+": "+t[1])
	}
	lines = append(lines, "User: "+prompt, "Assistant:")
	return strings.Join(lines, "\n")
}

func TestRespond(t *testing.T) {
	cases := []struct {
		name string
		msgs []message
		want string
	}{
		{"remember", []message{msg("system", "be terse"), msg("user", rememberText)}, "STORED"},
		{"pi recall from history", []message{msg("system", "be terse"), msg("user", rememberText), msg("assistant", "STORED"), msg("user", recallText)}, token},
		{"hermes recall from flattened transcript", []message{msg("system", "x"), msg("user", hermesPrompt([][2]string{{"user", rememberText}, {"assistant", "STORED"}}, recallText))}, token},
		{"latest remembered token wins", []message{msg("user", rememberText), msg("assistant", "STORED"), msg("user", "Remember this exact token for the next turn: newer-42."), msg("user", recallText)}, "newer-42"},
		{"reply word", []message{msg("user", "Reply with STREAM-OK only.")}, "STREAM-OK"},
		{"default", []message{msg("user", "hello")}, "ACK"},
		{"array content parts", []message{{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"Reply with PARTS only."}]`)}}, "PARTS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Respond(tc.msgs).Text; got != tc.want {
				t.Fatalf("Respond() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The fixture must never manufacture a token: a recall whose history was
// lost has to fail the integration test's exact-token assertion.
func TestRecallWithoutHistoryDoesNotReturnToken(t *testing.T) {
	cases := map[string][]message{
		"pi lost history":                         {msg("system", "be terse"), msg("user", recallText)},
		"hermes lost history":                     {msg("user", hermesPrompt(nil, recallText))},
		"hermes history without remember turn":    {msg("user", hermesPrompt([][2]string{{"user", "Reply with STREAM-OK only."}, {"assistant", "STREAM-OK"}}, recallText))},
		"token only in final prompt, not history": {msg("user", recallText+" "+rememberText)},
		"assistant echo is not a remembered turn": {msg("assistant", token), msg("user", recallText)},
	}
	for name, msgs := range cases {
		t.Run(name, func(t *testing.T) {
			got := Respond(msgs).Text
			if strings.Contains(got, token) {
				t.Fatalf("recall without stored history returned the token: %q", got)
			}
			if got != NoTokenSentinel {
				t.Fatalf("Respond() = %q, want sentinel %q", got, NoTokenSentinel)
			}
		})
	}
}

func TestSlowMarkerOnlyAppliesToFinalPrompt(t *testing.T) {
	if got := Respond([]message{msg("user", "SLOW:120 Write an essay.")}); got.Slow != 120*time.Second {
		t.Fatalf("slow = %s, want 120s", got.Slow)
	}
	if got := Respond([]message{msg("user", "SLOW:9999 x")}); got.Slow != maxSlow {
		t.Fatalf("slow = %s, want cap %s", got.Slow, maxSlow)
	}
	h := hermesPrompt([][2]string{{"user", "SLOW:120 Write an essay."}, {"assistant", "..."}}, "Reply with OK only.")
	if got := Respond([]message{msg("user", h)}); got.Slow != 0 || got.Text != "OK" {
		t.Fatalf("history SLOW marker leaked into the final turn: %+v", got)
	}
}

func post(t *testing.T, ctx context.Context, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHTTPNonStreamingAndModels(t *testing.T) {
	ts := httptest.NewServer((&server{}).routes())
	defer ts.Close()
	resp := post(t, context.Background(), ts.URL, `{"model":"m","messages":[{"role":"user","content":"Reply with HI only."}]}`)
	defer resp.Body.Close()
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Model != "m" || len(out.Choices) != 1 || out.Choices[0].Message.Content != "HI" {
		t.Fatalf("unexpected completion: %+v", out)
	}

	mresp, err := http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer mresp.Body.Close()
	var models struct {
		Data []struct {
			ContextLength int `json:"context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(mresp.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	if len(models.Data) != 1 || models.Data[0].ContextLength < 65536 {
		t.Fatalf("models must advertise >=64K context for Hermes: %+v", models)
	}

	bad := post(t, context.Background(), ts.URL, `not json`)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid body status = %d", bad.StatusCode)
	}
}

func TestHTTPStreaming(t *testing.T) {
	ts := httptest.NewServer((&server{}).routes())
	defer ts.Close()
	resp := post(t, context.Background(), ts.URL, `{"model":"m","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"Reply with STREAM-OK only."}]}`)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	var content strings.Builder
	sawUsage, sawDone := false, false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			sawDone = true
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
			Usage *struct{} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("invalid SSE JSON %q: %v", data, err)
		}
		for _, c := range ev.Choices {
			content.WriteString(c.Delta.Content)
		}
		sawUsage = sawUsage || ev.Usage != nil
	}
	if content.String() != "STREAM-OK" || !sawUsage || !sawDone {
		t.Fatalf("stream content=%q usage=%t done=%t", content.String(), sawUsage, sawDone)
	}
}

func TestHTTPSlowStreamIsCancelledByDisconnect(t *testing.T) {
	s := &server{tick: 10 * time.Millisecond}
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	resp := post(t, ctx, ts.URL, `{"model":"m","stream":true,"messages":[{"role":"user","content":"SLOW:60 essay"}]}`)
	// Read a few keepalive bytes so the request is demonstrably in flight.
	buf := make([]byte, 64)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for s.cancelled.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("slow request was not observed as cancelled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The persistent-harness script names the sentinel in its failure diagnostics.
func TestScriptNamesSentinel(t *testing.T) {
	raw, err := os.ReadFile("../test-persistent-harness-session.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `FIXTURE_NO_TOKEN_SENTINEL="`+NoTokenSentinel+`"`) {
		t.Fatalf("test-persistent-harness-session.sh does not name sentinel %q", NoTokenSentinel)
	}
	if !strings.Contains(string(raw), "SLOW:") {
		t.Fatal("cancellation request no longer carries the fixture's SLOW marker")
	}
}

// Each request's key is recorded as a fingerprint with its conversation's
// canary tag, so the journey can prove which key served which Agent; the raw
// key is never kept or served back.
func TestKeyCanaryRecordsFingerprintAndTag(t *testing.T) {
	ts := httptest.NewServer((&server{}).routes())
	defer ts.Close()
	send := func(key, tag string) {
		body := `{"model":"canary","messages":[{"role":"user","content":"CANARY:` + tag + ` Reply with READY only"},{"role":"assistant","content":"READY"},{"role":"user","content":"Reply with OK only"}]}`
		req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", strings.NewReader(body))
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	send("sk-agent-a-secret", "agent-a")
	send("sk-agent-b-secret", "agent-b")
	send("", "agent-b")
	resp, err := http.Get(ts.URL + "/fixture/keys")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("raw key served back: %s", raw)
	}
	var records []KeyRecord
	if err := json.Unmarshal(raw, &records); err != nil || len(records) != 3 {
		t.Fatalf("records: %s %v", raw, err)
	}
	a, b := KeyFingerprint("Bearer sk-agent-a-secret"), KeyFingerprint("Bearer sk-agent-b-secret")
	if len(a) != 16 || a == b || records[0] != (KeyRecord{1, a, "agent-a"}) || records[1] != (KeyRecord{2, b, "agent-b"}) || records[2] != (KeyRecord{3, "", "agent-b"}) {
		t.Fatalf("records = %+v", records)
	}
}
