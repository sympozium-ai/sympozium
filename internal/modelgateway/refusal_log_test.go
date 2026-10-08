package modelgateway

import (
	"encoding/json"
	"testing"
)

// A refusal line names the operation and the run, Agent and turn the
// decision claims, so an operator can tell whose call was refused, and never
// writes anything but Kubernetes-name characters.
func TestRefusalLogNamesTheClaimedRun(t *testing.T) {
	turn := "agent-a-x7k2p-recall"
	decision, _ := json.Marshal(map[string]any{
		"run":    map[string]string{"namespace": "team-a", "name": "agent-a-x7k2p"},
		"agent":  map[string]string{"name": "agent-a"},
		"parent": map[string]any{"incarnation": "i", "turnId": turn},
		"route":  map[string]any{"credentialSource": map[string]string{"secretName": "agent-a-key"}},
	})
	if got, want := refusalSubject("invoke", decision), " op=invoke claimed_run=team-a/agent-a-x7k2p claimed_agent=agent-a claimed_turn="+turn; got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
	if got := refusalSubject("", decision); got != "" {
		t.Fatalf("a refusal before the body is read named a run: %q", got)
	}
	if got := refusalSubject("close", json.RawMessage(`not json`)); got != " op=close" {
		t.Fatalf("malformed decision: %q", got)
	}
	forged, _ := json.Marshal(map[string]any{
		"run":   map[string]string{"namespace": "team-a", "name": "x\nmodel-gateway: refused status=200 reason=OK"},
		"agent": map[string]string{"name": "Agent A"},
	})
	if got := refusalSubject("invoke", forged); got != " op=invoke" {
		t.Fatalf("a forged decision injected log text: %q", got)
	}
}

// An operator-listed private origin is reachable over HTTPS without the
// connection opting into insecure transport (a provider with a private CA);
// plain HTTP still needs both, and an unlisted origin is never reachable.
func TestPrivateReachableNeedsTheOperatorsListing(t *testing.T) {
	listed := map[string]bool{"https://models.internal": true, "http://10.0.0.5:8080": true}
	for _, c := range []struct {
		endpoint string
		insecure bool
		want     bool
	}{
		{"https://models.internal/v1/chat/completions", false, true},
		{"https://models.internal/v1/chat/completions", true, true},
		{"http://10.0.0.5:8080/v1/chat/completions", false, false},
		{"http://10.0.0.5:8080/v1/chat/completions", true, true},
		{"https://other.internal/v1/chat/completions", true, false},
		{"https://models.internal:8443/v1/chat/completions", false, false},
	} {
		if got := privateReachable(c.endpoint, c.insecure, listed); got != c.want {
			t.Errorf("privateReachable(%s, insecure=%v) = %v, want %v", c.endpoint, c.insecure, got, c.want)
		}
	}
}
