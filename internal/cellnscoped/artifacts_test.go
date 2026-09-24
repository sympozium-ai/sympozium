package cellnscoped

import (
	"context"
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
