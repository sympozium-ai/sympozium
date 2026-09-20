package modelgateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"reflect"
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

func digestJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	_, digest, err := cellncapability.CanonicalRequest(raw)
	return digest, err
}

func requestDigest(raw []byte) (string, []byte, error) {
	// Use the same v1 integer-only JCS profile as the host consumer. Go's JSON
	// encoder alone HTML-escapes strings and orders keys by UTF-8, not UTF-16.
	if len(raw) == 0 || len(raw) > 262144 {
		return "", nil, fmt.Errorf("model request exceeds v1 canonical body bound")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	depth := 0
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			if depth > 64 {
				return "", nil, fmt.Errorf("model request exceeds v1 nesting bound")
			}
		}
	}
	canonical, digest, err := cellncapability.CanonicalRequest(raw)
	return digest, canonical, err
}

// connectionDigest is route.modelConnectionSpecSha256 for a live spec. It
// covers every spec field, including the request policy (parameters and
// maxOutputTokens): changing either changes the digest and refuses the runs
// pinned to the old one. Parameters are bound as text (api DigestView) because
// the integer-only canonical profile cannot carry a fractional number.
func connectionDigest(spec api.ModelConnectionSpec) (string, error) {
	view, err := spec.DigestView()
	if err != nil {
		return "", err
	}
	return digestJSON(view)
}

func turnID(decision cellncapability.Decision) string {
	if decision.Operation == "execution.turn" && decision.Parent != nil && decision.Parent.TurnID != nil {
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
		if !strings.HasPrefix(origin, "https://") {
			return fail(ReasonDestination, 403, nil)
		}
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

// providerRequestFields are the exact top-level names a guest body may use.
// encoding/json matches struct fields case-insensitively, so without this a
// guest could send "max_tokens" twice in different case: one value would be
// charged and bounded here, the other forwarded to the provider.
var providerRequestFields = func() map[string]bool {
	fields := map[string]bool{}
	t := reflect.TypeOf(providerRequest{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		fields[name] = true
	}
	return fields
}()

// requestPolicy is the per-connection request policy of the live, pinned
// ModelConnection spec. It is operator policy, never guest input.
type requestPolicy struct {
	// MaxOutputTokens bounds what one request may ask for (default 512).
	MaxOutputTokens int64
	// Parameters are merged into the outgoing provider body; the guest may
	// not carry any of their keys.
	Parameters map[string]any
}

func connectionRequestPolicy(spec api.ModelConnectionSpec) (requestPolicy, error) {
	parameters, err := spec.RequestParameters()
	if err != nil {
		return requestPolicy{}, err
	}
	return requestPolicy{MaxOutputTokens: spec.RequestOutputTokens(), Parameters: parameters}, nil
}

// validateProviderRequest checks the guest body and returns the output tokens
// to reserve, the digest and canonical form of the guest body (both exactly as
// before request policy existed: parameters are not part of either) and the
// body to send to the provider.
func validateProviderRequest(protocol, expectedModel string, raw []byte, turnCap int64, policy requestPolicy) (int64, string, []byte, []byte, error) {
	refuse := func(reason string, status int, err error) (int64, string, []byte, []byte, error) {
		return 0, "", nil, nil, fail(reason, status, err)
	}
	var req providerRequest
	if err := decodeStrict(raw, &req); err != nil {
		return refuse(ReasonMalformed, 400, err)
	}
	var guestFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &guestFields); err != nil {
		return refuse(ReasonMalformed, 400, err)
	}
	for field := range guestFields {
		if !providerRequestFields[field] {
			return refuse(ReasonMalformed, 400, fmt.Errorf("request field %q is not an exact provider field name", field))
		}
		// Operator parameters are host-pinned: a guest body carrying one of
		// their keys is refused, never merged or overridden in either direction.
		for pinned := range policy.Parameters {
			if strings.EqualFold(field, pinned) {
				return refuse(ReasonForbidden, 403, fmt.Errorf("request field %q is pinned by the model connection's parameters", field))
			}
		}
	}
	if req.Model != expectedModel || len(req.Messages) == 0 || bytes.Equal(req.Messages, []byte("null")) {
		return refuse(ReasonForbidden, 403, nil)
	}
	if req.Stream {
		return refuse(ReasonStreaming, 400, nil)
	}
	var requested int64
	switch protocol {
	case "openai-chat":
		if req.MaxCompletionTokens != nil && req.MaxTokens != nil {
			return refuse(ReasonMalformed, 400, nil)
		}
		if req.N != nil && *req.N != 1 {
			return refuse(ReasonForbidden, 403, nil)
		}
		if req.MaxCompletionTokens != nil {
			requested = *req.MaxCompletionTokens
		} else if req.MaxTokens != nil {
			requested = *req.MaxTokens
		}
	case "anthropic-messages":
		if req.MaxCompletionTokens != nil || req.MaxTokens == nil {
			return refuse(ReasonMalformed, 400, nil)
		}
		requested = *req.MaxTokens
	default:
		return refuse(ReasonProtocol, 400, nil)
	}
	bound := policy.MaxOutputTokens
	if bound < 1 || bound > api.MaxRequestOutputTokens {
		// Never trust an unvalidated policy: fall back to the default bound.
		bound = api.DefaultRequestOutputTokens
	}
	if requested < 1 || requested > bound {
		return refuse(ReasonForbidden, 403, fmt.Errorf("requested output tokens %d are outside 1..%d, the model connection's bound per request", requested, bound))
	}
	if requested > turnCap {
		return refuse(ReasonForbidden, 403, fmt.Errorf("requested output tokens %d exceed the turn's output-token cap %d", requested, turnCap))
	}
	digest, canonical, err := requestDigest(raw)
	if err != nil {
		return refuse(ReasonMalformed, 400, err)
	}
	outgoing, err := mergeParameters(canonical, policy.Parameters)
	if err != nil {
		return refuse(ReasonRouteChanged, 403, err)
	}
	return requested, digest, canonical, outgoing, nil
}

// mergeParameters appends the connection's parameters to the canonical guest
// body. The guest's bytes are kept exactly as canonicalised; the parameters
// are encoded by encoding/json with numbers as the operator wrote them
// (json.Number), so a fractional value such as 0.7 is forwarded intact and
// never meets the integer-only guest canonicaliser. The caller has already
// refused any key collision, so the result has no duplicate top-level key.
// Without parameters the outgoing body is the canonical guest body itself.
func mergeParameters(canonical []byte, parameters map[string]any) ([]byte, error) {
	if len(parameters) == 0 {
		return canonical, nil
	}
	if err := api.ValidateModelParameters(parameters); err != nil {
		return nil, err
	}
	extra, err := json.Marshal(parameters)
	if err != nil {
		return nil, err
	}
	// A validated guest body is a nonempty canonical object: {"messages":...}.
	if len(canonical) < 3 || canonical[0] != '{' || canonical[len(canonical)-1] != '}' || len(extra) < 3 {
		return nil, fmt.Errorf("model request is not a nonempty JSON object")
	}
	out := make([]byte, 0, len(canonical)+len(extra))
	out = append(out, canonical[:len(canonical)-1]...)
	out = append(out, ',')
	out = append(out, extra[1:]...)
	if !json.Valid(out) {
		return nil, fmt.Errorf("merged model request is not valid JSON")
	}
	return out, nil
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
