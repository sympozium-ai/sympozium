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

const ScopedArtifactContract = "celln.scoped-artifacts/v1"

func IsUnsupported(err error) bool {
	var target *HTTPError
	return errors.As(err, &target) && target.Reason == "AUTH_PROTOCOL_UNSUPPORTED"
}

// PreflightArtifacts negotiates only for explicit artifact authority. It grants
// nothing and is deliberately not cached across admissions or backend changes.
func (c *NativeClient) PreflightArtifacts(ctx context.Context, decision cellnauthority.PlatformDecision) error {
	required := false
	for _, tool := range decision.Tools {
		required = required || tool.Limits.Artifacts != nil
	}
	if !required {
		return nil
	}
	unsupported := &HTTPError{Status: http.StatusNotImplemented, Reason: "AUTH_PROTOCOL_UNSUPPORTED"}
	if c == nil || c.host == nil || (decision.Lifecycle != "enduring-initial" && decision.Lifecycle != "enduring-turn") || decision.Route.Protocol == "none" {
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
	var contracts []string
	if json.Unmarshal(document["scopedArtifactContracts"], &contracts) != nil || !slices.Contains(contracts, ScopedArtifactContract) {
		return unsupported
	}
	return nil
}
