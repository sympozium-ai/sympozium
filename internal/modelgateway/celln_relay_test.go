package modelgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
)

// Real Rust warden -> host context -> verified TLS gateway -> durable ledger ->
// provider transport. This helper does NOT admit a native parent or execute a VM.
func exerciseCellnRelay(t *testing.T, g *Gateway, issuer *cap.Issuer, original cap.Decision, server *httptest.Server, public ed25519.PublicKey, received <-chan string, providerKey string) {
	binary := os.Getenv("CELLN_GATEWAY_RELAY_PROBE")
	if binary == "" {
		return
	}
	t.Run("RustHostRelay", func(t *testing.T) {
		if !filepath.IsAbs(binary) {
			t.Fatal("relay probe must be an absolute executable path")
		}
		d := original
		d.Run.UID += "-relay"
		d.Subject.UID = d.Run.UID
		d.Budget.BudgetID = fixtureDigest("budget/" + d.Run.UID)
		d.Budget.RunCap = cap.Cap{Requests: 1, OutputTokens: 512}
		d.Budget.TurnCap = d.Budget.RunCap
		d.RequestDigest = fixtureDigest("relay-input")
		raw, _, err := cap.CanonicalDecision(d)
		if err != nil {
			t.Fatal(err)
		}
		execution, err := issuer.Issue(d, cap.IssueRequest{Audience: cap.AudienceExecution, Operation: "execution.start"})
		if err != nil {
			t.Fatal(err)
		}
		model, err := issuer.Issue(d, cap.IssueRequest{Audience: cap.AudienceModelGateway, Operation: "model.invoke"})
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Register(context.Background(), RegistrationRequest{Decision: raw, ExecutionToken: execution, ConnectionName: "model"}); err != nil {
			t.Fatal(err)
		}
		ca := filepath.Join(t.TempDir(), "gateway-ca.pem")
		if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.TLS.Certificates[0].Certificate[0]}), 0600); err != nil {
			t.Fatal(err)
		}
		request := json.RawMessage(`{"model":"m","stream":false,"max_tokens":512,"messages":[{"role":"user","content":"hello"}]}`)
		input := map[string]any{
			"jwks":           map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA", "use": "sig", "x": base64.RawURLEncoding.EncodeToString(public)}}},
			"receiver":       map[string]any{"now": time.Now().Unix(), "expectedAudience": string(cap.AudienceExecution), "expectedOperation": "execution.start", "clusterId": d.ClusterID, "namespace": d.Run.Namespace, "namespaceUid": d.Run.NamespaceUID, "runUid": d.Run.UID, "runSpecSha256": d.Run.SpecSHA256, "parent": d.Parent, "requestDigest": d.RequestDigest, "route": d.Route, "budgetId": d.Budget.BudgetID, "seenAdmissionJti": false},
			"executionToken": execution.Bearer(), "modelToken": model.Bearer(), "decision": json.RawMessage(raw), "gatewayOrigin": server.URL, "gatewayCa": ca, "requests": []json.RawMessage{request, request},
		}
		// Transport auth is never argv, an environment variable, or a token file.
		exerciseRelayTransportRefusals(t, binary, input)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		results := runRelayProbe(t, ctx, binary, input)
		if len(results) != 2 || !results[0].OK || results[1].OK || !results[1].Budget {
			t.Fatal("relay did not return one success followed by durable budget refusal")
		}
		if !bytes.Contains(results[0].Body, []byte("fixture result")) {
			t.Fatal("missing provider result")
		}
		select {
		case auth := <-received:
			if auth != "Bearer "+providerKey {
				t.Fatal("wrong tenant provider credential")
			}
		case <-ctx.Done():
			t.Fatal("no provider invocation")
		}
		select {
		case <-received:
			t.Fatal("duplicate provider invocation")
		default:
		}
		usage, err := g.budgets.Inspect(ctx, d.Budget.BudgetID, turnID(d))
		if err != nil {
			t.Fatal(err)
		}
		if usage.RunReservedRequests != 1 || usage.RunReservedOutputTokens != 512 {
			t.Fatal("unexpected relay accounting")
		}
		t.Logf("Rust host TLS relay: namespaceUid=%s runUid=%s budgetId=%s requests=1 outputReserved=512; no native admission/VM claim", d.Run.NamespaceUID, d.Run.UID, d.Budget.BudgetID)
	})
}
