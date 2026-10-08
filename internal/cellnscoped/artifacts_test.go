package cellnscoped

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
)

func TestArtifactNegotiationFailsBeforeNativeOrGatewayWork(t *testing.T) {
	for _, body := range []string{`{}`, `{"scopedArtifactContracts":[]}`, `{"scopedArtifactContracts":["future"]}`, `invalid`, `{"scopedArtifactContracts":[],"scopedArtifactContracts":["celln.scoped-artifacts/v1"]}`, `{"scopedArtifactContracts":["celln.scoped-artifacts/v1"]}`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/v1/capabilities" {
					t.Error("side effect before negotiation")
				}
				w.Write([]byte(body))
			}))
			defer server.Close()
			native := &NativeClient{host: &hostClient{origin: server.URL, http: server.Client()}}
			decision := cellnauthority.PlatformDecision{Lifecycle: "enduring-initial", Route: cellnauthority.DecisionRouteBinding{Protocol: "openai-chat"}, Tools: []cellnauthority.DecisionToolBinding{{Limits: cellnauthority.DecisionToolLimits{Artifacts: &api.CellnArtifactLimits{Operation: "write"}}}}}
			err := native.PreflightArtifacts(context.Background(), decision)
			if (err == nil) != (body == `{"scopedArtifactContracts":["celln.scoped-artifacts/v1"]}`) {
				t.Fatal("wrong negotiation", err)
			}
			if calls != 1 {
				t.Fatal("wrong call count", calls)
			}
			if err != nil {
				operation := cellnauthority.PreparedOperation{Resolution: cellnauthority.PlatformResolution{Decision: decision}}
				if _, err = native.Prepare(context.Background(), operation, cap.Decision{}); !IsUnsupported(err) {
					t.Fatal("preparation did not refuse unsupported backend", err)
				}
				if calls != 2 {
					t.Fatal("unexpected native or provider work")
				}
			}
			before := calls
			decision.Tools = nil
			if err = native.PreflightArtifacts(context.Background(), decision); err != nil || calls != before {
				t.Fatal("non-artifact regression")
			}
		})
	}
}

// The full starter toolbox on a mediated route asks for exactly the
// contracts it needs; a node missing any of them (an older Celln) refuses
// before any native or gateway work, and v1-only decisions still negotiate v1.
func TestBrokeredToolNegotiationRequiresEveryNeededContract(t *testing.T) {
	v1 := `"scopedArtifactContracts":["celln.scoped-artifacts/v1"]`
	v2 := `"scopedArtifactContracts":["celln.scoped-artifacts/v1","celln.scoped-artifacts/v2"]`
	web := `"scopedHttpsContracts":["celln.scoped-https/v1"]`
	tool := func(op string, https bool) cellnauthority.DecisionToolBinding {
		l := cellnauthority.DecisionToolLimits{}
		if op != "" {
			l.Artifacts = &api.CellnArtifactLimits{Operation: op}
		}
		if https {
			l.HTTPS = &api.CellnHTTPSLimits{AllowHosts: []string{"*"}}
		}
		return cellnauthority.DecisionToolBinding{Limits: l}
	}
	for name, c := range map[string]struct {
		lifecycle string
		tools     []cellnauthority.DecisionToolBinding
		allowed   []string
	}{
		"enduring-read-write": {"enduring-initial", []cellnauthority.DecisionToolBinding{tool("read", false), tool("write", false)}, []string{v1, v2, v1 + "," + web, v2 + "," + web}},
		"enduring-append":     {"enduring-turn", []cellnauthority.DecisionToolBinding{tool("append", false)}, []string{v2, v2 + "," + web}},
		"one-shot-read":       {"one-shot", []cellnauthority.DecisionToolBinding{tool("read", false)}, []string{v2, v2 + "," + web}},
		"one-shot-web":        {"one-shot", []cellnauthority.DecisionToolBinding{tool("", true)}, []string{web, v1 + "," + web, v2 + "," + web}},
		"enduring-web-list":   {"enduring-initial", []cellnauthority.DecisionToolBinding{tool("", true), tool("list", false)}, []string{v2 + "," + web}},
		"enduring-web-and-rw": {"enduring-initial", []cellnauthority.DecisionToolBinding{tool("", true), tool("write", false)}, []string{v1 + "," + web, v2 + "," + web}},
	} {
		for _, body := range []string{v1, v2, web, v1 + "," + web, v2 + "," + web, `"scopedHttpsContracts":["celln.scoped-https/v2"]`} {
			t.Run(name+"/"+body, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.Write([]byte("{" + body + "}"))
				}))
				defer server.Close()
				native := &NativeClient{host: &hostClient{origin: server.URL, http: server.Client()}}
				decision := cellnauthority.PlatformDecision{Lifecycle: c.lifecycle, Route: cellnauthority.DecisionRouteBinding{Protocol: "openai-chat"}, Tools: c.tools}
				err := native.PreflightArtifacts(context.Background(), decision)
				want := false
				for _, allowed := range c.allowed {
					want = want || allowed == body
				}
				if (err == nil) != want || (err != nil && !IsUnsupported(err)) || calls != 1 {
					t.Fatalf("allowed=%v err=%v calls=%d", want, err, calls)
				}
				// A model-free decision never carries brokered tools.
				decision.Route.Protocol = "none"
				if err := native.PreflightArtifacts(context.Background(), decision); !IsUnsupported(err) || calls != 1 {
					t.Fatal("model-free brokered tools negotiated", err)
				}
			})
		}
	}
}

// A receiver that rejects the controller's token, or cannot be reached over
// trusted TLS, is reported as such - not as a backend lacking the contract -
// so a rotated trust shows up as what it is.
func TestPreflightNamesReceiverFaults(t *testing.T) {
	decision := cellnauthority.PlatformDecision{Lifecycle: "enduring-initial", Route: cellnauthority.DecisionRouteBinding{Protocol: "openai-chat"}, Tools: []cellnauthority.DecisionToolBinding{{Limits: cellnauthority.DecisionToolLimits{Artifacts: &api.CellnArtifactLimits{Operation: "write"}}}}}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		native := &NativeClient{host: &hostClient{origin: server.URL, http: server.Client(), token: "stale"}}
		err := native.PreflightArtifacts(context.Background(), decision)
		server.Close()
		var e *HTTPError
		if IsUnsupported(err) || !IsReceiverMisconfigured(err) || !errors.As(err, &e) || e.Reason != ReasonReceiverAuthRejected {
			t.Fatalf("HTTP %d: %v", status, err)
		}
	}
	// A TLS receiver whose CA this client does not trust.
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	native := &NativeClient{host: &hostClient{origin: server.URL, http: &http.Client{}, token: "t"}}
	err := native.PreflightArtifacts(context.Background(), decision)
	var e *HTTPError
	if IsUnsupported(err) || !errors.As(err, &e) || e.Reason != ReasonReceiverUnreachable {
		t.Fatalf("untrusted TLS: %v", err)
	}
}
