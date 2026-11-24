package substrate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// Substrate is off unless an operator sets its issuer, and the clear-text hop
// is refused unless the operator says "true" in so many words.
func TestSettingsPlaintextHopNeedsAnExplicitAcknowledgement(t *testing.T) {
	if s, err := SettingsFromEnv(env(map[string]string{"SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP": "true", "SUBSTRATE_API_ENDPOINT": "api:443"})); err != nil || s != nil {
		t.Fatalf("without SUBSTRATE_JWT_ISSUER Substrate must be unconfigured, got %+v %v", s, err)
	}
	for value, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "1": true} {
		s, err := SettingsFromEnv(env(map[string]string{"SUBSTRATE_JWT_ISSUER": "https://idp.example", "SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP": value}))
		if err != nil || s == nil || s.AllowPlaintextGatewayHop != want {
			t.Errorf("SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP=%q: %+v %v, want %v", value, s, err, want)
		}
	}
	// A typo is an error, not a silent refusal to run or, worse, a yes.
	if _, err := SettingsFromEnv(env(map[string]string{"SUBSTRATE_JWT_ISSUER": "https://idp.example", "SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP": "yes please"})); err == nil {
		t.Error("a malformed acknowledgement was accepted")
	}
}

// The key set says whose tokens are believed, so it is never fetched in
// clear text.
func TestVerifierRefusesAClearTextKeySet(t *testing.T) {
	s, err := SettingsFromEnv(env(map[string]string{"SUBSTRATE_JWT_ISSUER": "https://idp.example", "SUBSTRATE_JWKS_URL": "http://idp.example/jwks"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verifier(); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("a clear-text key set URL was accepted: %v", err)
	}
}

type anyActor struct{}

func (anyActor) Verify(context.Context, string) (ActorRef, error) {
	return ActorRef{Atespace: "ns", Name: "run"}, nil
}

type completedStore struct{}

func (completedStore) Claim(context.Context, ActorRef) (Turn, bool, error) {
	return Turn{}, false, nil
}

func (completedStore) Complete(context.Context, ActorRef, TurnResult) error { return ErrTurnCompleted }

// A second result for a turn is a 409: not a success, and not something the
// runner retries.
func TestTurnHandlerRefusesASecondResult(t *testing.T) {
	handler := TurnHandler{Verifier: anyActor{}, Store: completedStore{}}
	req := httptest.NewRequest(http.MethodPost, ResultPath, strings.NewReader(`{"id":"turn-1","exitCode":0,"result":{"status":"success"}}`))
	req.Header.Set("Authorization", "Bearer jwt")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
}

// Substrate is the default plane wherever it is configured, unless the
// operator says no. A value that is not a boolean is an error, not a "yes".
func TestSubstrateIsTheDefaultPlaneUnlessSwitchedOff(t *testing.T) {
	for value, want := range map[string]bool{"": true, "true": true, "false": false} {
		s, err := SettingsFromEnv(func(k string) string {
			return map[string]string{"SUBSTRATE_JWT_ISSUER": "https://issuer.example", "SUBSTRATE_DEFAULT": value}[k]
		})
		if err != nil || s.Default != want {
			t.Errorf("SUBSTRATE_DEFAULT=%q: Default=%v err=%v, want %v", value, s != nil && s.Default, err, want)
		}
	}
	if _, err := SettingsFromEnv(func(k string) string {
		return map[string]string{"SUBSTRATE_JWT_ISSUER": "https://issuer.example", "SUBSTRATE_DEFAULT": "yes please"}[k]
	}); err == nil {
		t.Error("a SUBSTRATE_DEFAULT that is not a boolean was accepted")
	}
}
