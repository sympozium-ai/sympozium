package cellnscoped

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
)

// Scoped (mediated) broker contracts a Celln node advertises in
// /v1/capabilities. v1 artifacts are exact read/write for enduring runs; v2
// adds list, append, search and delete, and one-shot runs (a private store
// per run). ScopedHTTPSContract carries the public-only web tools.
const (
	ScopedArtifactContract   = "celln.scoped-artifacts/v1"
	ScopedArtifactContractV2 = "celln.scoped-artifacts/v2"
	ScopedHTTPSContract      = "celln.scoped-https/v1"
)

func IsUnsupported(err error) bool {
	var target *HTTPError
	return errors.As(err, &target) && target.Reason == "AUTH_PROTOCOL_UNSUPPORTED"
}

// RequiredContracts names the capabilities a decision's brokered tools need,
// as (capabilities field, contract) pairs; none for a tool-free decision.
// A decision a v1 node could serve asks only for v1, so older nodes keep
// working for exactly what they always did.
func RequiredContracts(decision cellnauthority.PlatformDecision) [][2]string {
	artifacts, v2, https := false, false, false
	enduring := decision.Lifecycle == "enduring-initial" || decision.Lifecycle == "enduring-turn"
	for _, tool := range decision.Tools {
		if a := tool.Limits.Artifacts; a != nil {
			artifacts = true
			v2 = v2 || !enduring || (a.Operation != "read" && a.Operation != "write")
		}
		https = https || tool.Limits.HTTPS != nil
	}
	var out [][2]string
	switch {
	case v2:
		out = append(out, [2]string{"scopedArtifactContracts", ScopedArtifactContractV2})
	case artifacts:
		out = append(out, [2]string{"scopedArtifactContracts", ScopedArtifactContract})
	}
	if https {
		out = append(out, [2]string{"scopedHttpsContracts", ScopedHTTPSContract})
	}
	return out
}

// PreflightArtifacts negotiates only for explicit brokered tool authority
// (run artifacts and web tools). It grants nothing and is deliberately not
// cached across admissions or backend changes. A node that does not
// advertise every contract the decision needs fails closed with
// AUTH_PROTOCOL_UNSUPPORTED before any native or gateway work.
func (c *NativeClient) PreflightArtifacts(ctx context.Context, decision cellnauthority.PlatformDecision) error {
	required := RequiredContracts(decision)
	if len(required) == 0 {
		return nil
	}
	unsupported := &HTTPError{Status: http.StatusNotImplemented, Reason: "AUTH_PROTOCOL_UNSUPPORTED"}
	if c == nil || c.host == nil || decision.Route.Protocol == "none" {
		return unsupported
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host.origin+"/v1/capabilities", nil)
	if err != nil {
		return unsupported
	}
	req.Header.Set("Authorization", "Bearer "+c.host.token)
	req.Header.Set("Accept", "application/json")
	response, err := c.host.http.Do(req)
	if err != nil {
		return unsupported
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes || response.StatusCode != http.StatusOK {
		return unsupported
	}
	// Capabilities are additive; unrelated capabilities are not decision fields.
	var document map[string]json.RawMessage
	if cap.StrictDecode(data, &document) != nil {
		return unsupported
	}
	for _, need := range required {
		var contracts []string
		if json.Unmarshal(document[need[0]], &contracts) != nil || !slices.Contains(contracts, need[1]) {
			return unsupported
		}
	}
	return nil
}
