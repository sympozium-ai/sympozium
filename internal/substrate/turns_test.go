package substrate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type issuer struct {
	key    *ecdsa.PrivateKey
	signer jose.Signer
	jwks   *httptest.Server
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "ES256", Use: "sig"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(set) }))
	t.Cleanup(srv.Close)
	return &issuer{key: key, signer: signer, jwks: srv}
}

func (i *issuer) token(t *testing.T, iss, sub, aud string, exp time.Time) string {
	t.Helper()
	return i.claims(t, jwt.Claims{Issuer: iss, Subject: sub, Audience: jwt.Audience{aud}, Expiry: jwt.NewNumericDate(exp)})
}

func (i *issuer) claims(t *testing.T, claims jwt.Claims) string {
	t.Helper()
	s, err := jwt.Signed(i.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJWTVerifier(t *testing.T) {
	iss := newIssuer(t)
	other := newIssuer(t)
	v := &JWTVerifier{Issuer: "https://idp", Audience: "sympozium-turns", JWKSURL: iss.jwks.URL}
	ok := time.Now().Add(5 * time.Minute)
	cases := []struct {
		name, token string
		want        string
	}{
		{"valid", iss.token(t, "https://idp", "actor/symp-default/run-1", "sympozium-turns", ok), "symp-default/run-1"},
		{"wrong audience", iss.token(t, "https://idp", "actor/symp-default/run-1", "model-gateway", ok), ""},
		{"wrong issuer", iss.token(t, "https://evil", "actor/symp-default/run-1", "sympozium-turns", ok), ""},
		{"expired", iss.token(t, "https://idp", "actor/symp-default/run-1", "sympozium-turns", time.Now().Add(-time.Hour)), ""},
		{"not an actor subject", iss.token(t, "https://idp", "system:serviceaccount:x:y", "sympozium-turns", ok), ""},
		{"signed by someone else", other.token(t, "https://idp", "actor/symp-default/run-1", "sympozium-turns", ok), ""},
		{"placeholder the gateway did not replace", "substrate-actor", ""},
		{"no expiry", iss.claims(t, jwt.Claims{Issuer: "https://idp", Subject: "actor/symp-default/run-1", Audience: jwt.Audience{"sympozium-turns"}}), ""},
	}
	// A verifier without an issuer or an audience refuses everything: an
	// empty expectation would otherwise not be checked at all.
	for _, open := range []*JWTVerifier{
		{Audience: "sympozium-turns", JWKSURL: iss.jwks.URL},
		{Issuer: "https://idp", JWKSURL: iss.jwks.URL},
	} {
		if got, err := open.Verify(context.Background(), cases[0].token); err == nil {
			t.Errorf("verifier %+v accepted a token as %v", open, got)
		}
	}
	for _, c := range cases {
		got, err := v.Verify(context.Background(), c.token)
		if c.want == "" {
			if err == nil {
				t.Errorf("%s: accepted as %v", c.name, got)
			}
			continue
		}
		if err != nil || got.String() != c.want {
			t.Errorf("%s: got %v err=%v", c.name, got, err)
		}
	}
}

type fixedVerifier map[string]ActorRef

func (f fixedVerifier) Verify(_ context.Context, token string) (ActorRef, error) {
	if a, ok := f[token]; ok {
		return a, nil
	}
	return ActorRef{}, ErrNoSuchTurn
}

func TestTurnHandlerHandsATurnOnlyToItsActor(t *testing.T) {
	store := NewMemoryTurnStore()
	mine, theirs := ActorRef{Atespace: "a", Name: "run-1"}, ActorRef{Atespace: "a", Name: "run-2"}
	srv := httptest.NewServer(TurnHandler{Verifier: fixedVerifier{"t1": mine, "t2": theirs}, Store: store})
	defer srv.Close()
	done := store.Offer(mine, Turn{ID: "turn-1", Env: map[string]string{"TASK": "hello"}})

	call := func(method, path, token, body string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, _ := call("GET", TurnPath, "", ""); code != http.StatusUnauthorized {
		t.Fatalf("no credential: %d", code)
	}
	if code, _ := call("GET", TurnPath, "substrate-actor", ""); code != http.StatusUnauthorized {
		t.Fatalf("unreplaced placeholder: %d", code)
	}
	if code, _ := call("GET", TurnPath, "t2", ""); code != http.StatusNoContent {
		t.Fatalf("another actor must see no turn: %d", code)
	}
	for i := 0; i < 2; i++ { // a claim is repeatable until the turn completes
		code, body := call("GET", TurnPath, "t1", "")
		if code != http.StatusOK || !strings.Contains(body, `"TASK":"hello"`) {
			t.Fatalf("claim %d: %d %s", i, code, body)
		}
	}
	if code, _ := call("POST", ResultPath, "t2", `{"id":"turn-1","exitCode":0}`); code != http.StatusNotFound {
		t.Fatalf("another actor must not complete the turn: %d", code)
	}
	if code, _ := call("POST", ResultPath, "t1", `{"id":"turn-9","exitCode":0}`); code != http.StatusNotFound {
		t.Fatalf("a result for a turn never handed out: %d", code)
	}
	if code, _ := call("POST", ResultPath, "t1", `{"id":"turn-1","exitCode":0,"result":{"status":"success"}}`); code != http.StatusNoContent {
		t.Fatalf("result: %d", code)
	}
	select {
	case res := <-done:
		if res.ID != "turn-1" || string(res.Result) != `{"status":"success"}` {
			t.Fatalf("delivered %+v", res)
		}
	default:
		t.Fatal("result was not delivered")
	}
	if code, _ := call("POST", ResultPath, "t1", `{"id":"turn-1","exitCode":0}`); code != http.StatusNotFound {
		t.Fatalf("a turn completes once: %d", code)
	}
	if code, _ := call("POST", ResultPath, "t1", `{"id":"x","logs":"`+strings.Repeat("a", maxResultBody)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("oversized result: %d", code)
	}
}

type fakeTools struct {
	calls []string
	err   error
}

func (f *fakeTools) CallTool(_ context.Context, actor ActorRef, tool string, args json.RawMessage) (string, error) {
	f.calls = append(f.calls, actor.String()+" "+tool+" "+string(args))
	return "sent", f.err
}

// The tool endpoint decides nothing itself: it identifies the actor and hands
// the call to the ToolCaller. What it must get right is who is asking, and
// that a refusal reaches the model as a refusal.
func TestToolEndpointIdentifiesTheActorAndRelaysTheDecision(t *testing.T) {
	tools := &fakeTools{}
	h := TurnHandler{Verifier: fixedVerifier{"good": {Atespace: "ns", Name: "run-1", UID: "u1"}}, Store: NewMemoryTurnStore(), Tools: tools}
	call := func(path, token, body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, body := call(ToolPathPrefix+"send_channel_message", "good", `{"channel":"slack","text":"hi"}`); code != http.StatusOK || !strings.Contains(body, `"result":"sent"`) {
		t.Fatalf("allowed call = %d %s", code, body)
	}
	if len(tools.calls) != 1 || !strings.HasPrefix(tools.calls[0], "ns/run-1 send_channel_message ") {
		t.Fatalf("calls = %v", tools.calls)
	}

	// Nothing reaches the ToolCaller without a verified actor, a tool name
	// that could be one, and well-formed arguments.
	for name, c := range map[string]struct {
		path, token, body string
		want              int
	}{
		"no credential":       {ToolPathPrefix + "send_channel_message", "", `{}`, http.StatusUnauthorized},
		"unknown credential":  {ToolPathPrefix + "send_channel_message", "forged", `{}`, http.StatusUnauthorized},
		"placeholder":         {ToolPathPrefix + "send_channel_message", "substrate-actor", `{}`, http.StatusUnauthorized},
		"path traversal":      {ToolPathPrefix + "../turn", "good", `{}`, http.StatusNotFound},
		"nested path":         {ToolPathPrefix + "a/b", "good", `{}`, http.StatusNotFound},
		"malformed arguments": {ToolPathPrefix + "send_channel_message", "good", `{"channel":`, http.StatusBadRequest},
		"oversized arguments": {ToolPathPrefix + "send_channel_message", "good", `{"text":"` + strings.Repeat("a", maxToolBody) + `"}`, http.StatusBadRequest},
	} {
		if code, _ := call(c.path, c.token, c.body); code != c.want {
			t.Errorf("%s: status = %d, want %d", name, code, c.want)
		}
	}
	if len(tools.calls) != 1 {
		t.Fatalf("a rejected request reached the tool caller: %v", tools.calls)
	}

	tools.err = &ToolRefusal{Reason: `tool "schedule_task" is denied by policy`}
	if code, body := call(ToolPathPrefix+"schedule_task", "good", `{}`); code != http.StatusForbidden || !strings.Contains(body, "denied by policy") {
		t.Errorf("refusal = %d %s, want 403 with the reason", code, body)
	}
	tools.err = ErrNoSuchTurn
	if code, _ := call(ToolPathPrefix+"schedule_task", "good", `{}`); code != http.StatusNotFound {
		t.Errorf("no live run = %d, want 404", code)
	}
	tools.err = errors.New("the API server said something internal")
	if code, body := call(ToolPathPrefix+"schedule_task", "good", `{}`); code != http.StatusServiceUnavailable || strings.Contains(body, "internal") {
		t.Errorf("failure = %d %s, want 503 with no detail", code, body)
	}

	// With no ToolCaller the path does not exist.
	h.Tools = nil
	if code, _ := call(ToolPathPrefix+"send_channel_message", "good", `{}`); code != http.StatusNotFound {
		t.Errorf("no tools configured = %d, want 404", code)
	}
}
