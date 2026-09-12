package modelgateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellncapability"
)

func decodeDecision(raw []byte) (cellncapability.Decision, error) {
	var out cellncapability.Decision
	if err := cellncapability.StrictDecode(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

func decodeStrict(raw []byte, out any) error { return cellncapability.StrictDecode(raw, out) }

func requireEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("request must contain one JSON value")
	}
	return nil
}

func digestJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	_, digest, err := cellncapability.CanonicalRequest(raw)
	return digest, err
}

func requestDigest(raw []byte) (string, []byte, error) {
	// StrictDecode above has already rejected duplicate keys and trailing data.
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return "", nil, err
	}
	if err := requireEOF(dec); err != nil {
		return "", nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), canonical, nil
}

func connectionDigest(spec api.ModelConnectionSpec) (string, error) { return digestJSON(spec) }

func turnID(decision cellncapability.Decision) string {
	if decision.Parent != nil && decision.Parent.TurnID != nil {
		return *decision.Parent.TurnID
	}
	return decision.Run.UID
}

func validateConnection(decision cellncapability.Decision, name string, connectionUID string, spec api.ModelConnectionSpec) error {
	if name == "" || decision.Route.ModelConnectionUID == nil || *decision.Route.ModelConnectionUID != connectionUID {
		return fail(ReasonRouteChanged, 403, nil)
	}
	if err := spec.Validate(); err != nil || spec.Disabled || !slices.Contains(spec.Models, decision.Route.Model) {
		return fail(ReasonRouteChanged, 403, err)
	}
	digest, err := connectionDigest(spec)
	if err != nil || digest != decision.Route.ModelConnectionSpecSHA256 {
		return fail(ReasonRouteChanged, 403, err)
	}
	origin, err := api.ModelEndpointOriginInsecure(spec.Endpoint, spec.AllowInsecure)
	if err != nil || origin != decision.Route.EndpointOrigin || spec.Provider != decision.Route.Provider || spec.Protocol != decision.Route.Protocol {
		return fail(ReasonRouteChanged, 403, err)
	}
	if decision.Route.Auth == "secret" {
		if decision.Route.CredentialSource == nil || spec.SecretRef != decision.Route.CredentialSource.SecretName || spec.CredentialProfile != "" {
			return fail(ReasonCredentialChanged, 403, nil)
		}
	} else if decision.Route.Auth != "none" || spec.SecretRef != "" || spec.CredentialProfile != "" || decision.Route.CredentialSource != nil {
		return fail(ReasonRouteChanged, 403, nil)
	}
	return nil
}

type providerRequest struct {
	Model               string          `json:"model"`
	Messages            json.RawMessage `json:"messages"`
	MaxTokens           *int64          `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int64          `json:"max_completion_tokens,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
	System              json.RawMessage `json:"system,omitempty"`
	Temperature         *float64        `json:"temperature,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	TopK                *int64          `json:"top_k,omitempty"`
	Stop                json.RawMessage `json:"stop,omitempty"`
	StopSequences       json.RawMessage `json:"stop_sequences,omitempty"`
	Tools               json.RawMessage `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat      json.RawMessage `json:"response_format,omitempty"`
	Seed                *int64          `json:"seed,omitempty"`
	N                   *int64          `json:"n,omitempty"`
	FrequencyPenalty    *float64        `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64        `json:"presence_penalty,omitempty"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls,omitempty"`
	User                string          `json:"user,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

func validateProviderRequest(protocol, expectedModel string, raw []byte, turnCap int64) (int64, string, []byte, error) {
	var req providerRequest
	if err := decodeStrict(raw, &req); err != nil {
		return 0, "", nil, fail(ReasonMalformed, 400, err)
	}
	if req.Model != expectedModel || len(req.Messages) == 0 || bytes.Equal(req.Messages, []byte("null")) {
		return 0, "", nil, fail(ReasonForbidden, 403, nil)
	}
	if req.Stream {
		return 0, "", nil, fail(ReasonStreaming, 400, nil)
	}
	var requested int64
	switch protocol {
	case "openai-chat":
		if req.MaxCompletionTokens != nil && req.MaxTokens != nil {
			return 0, "", nil, fail(ReasonMalformed, 400, nil)
		}
		if req.N != nil && *req.N != 1 {
			return 0, "", nil, fail(ReasonForbidden, 403, nil)
		}
		if req.MaxCompletionTokens != nil {
			requested = *req.MaxCompletionTokens
		} else if req.MaxTokens != nil {
			requested = *req.MaxTokens
		}
	case "anthropic-messages":
		if req.MaxCompletionTokens != nil || req.MaxTokens == nil {
			return 0, "", nil, fail(ReasonMalformed, 400, nil)
		}
		requested = *req.MaxTokens
	default:
		return 0, "", nil, fail(ReasonProtocol, 400, nil)
	}
	if requested < 1 || requested > turnCap {
		return 0, "", nil, fail(ReasonForbidden, 403, nil)
	}
	digest, canonical, err := requestDigest(raw)
	if err != nil {
		return 0, "", nil, fail(ReasonMalformed, 400, err)
	}
	return requested, digest, canonical, nil
}

func forbiddenIP(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

func exactOrigin(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid endpoint")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}
