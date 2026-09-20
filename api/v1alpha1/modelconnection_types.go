package v1alpha1

import (
	"encoding/json"
	"fmt"
	"k8s.io/apimachinery/pkg/util/validation"
	"net/url"
	"regexp"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModelConnection describes a model route, not a grant to execute it.
// The independent host policy must admit the same endpoint and credential profile.
// +kubebuilder:object:root=true
type ModelConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ModelConnectionSpec `json:"spec"`
}

type ModelConnectionSpec struct {
	// Provider is a descriptive provider identifier, including custom providers.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]{1,64}$`
	Provider string `json:"provider"`
	// Protocol selects the host adapter. Compatible gateways may expose other providers.
	// +kubebuilder:validation:Enum=openai-chat;anthropic-messages
	Protocol string `json:"protocol"`
	// Endpoint is an HTTP(S) API base URL or full request URL. Native Celln requires a full HTTPS request URL.
	// +kubebuilder:validation:MaxLength=2048
	Endpoint string `json:"endpoint"`
	// CredentialProfile is an opaque host mapping name, never an API key or file path.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]{1,64}$`
	// +optional
	CredentialProfile string `json:"credentialProfile,omitempty"`
	// SecretRef selects a Kubernetes credential Secret for container harnesses.
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
	// Models lists the exact model identifiers selectable through this connection.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	// +listType=set
	Models []string `json:"models"`
	// Disabled prevents new connection resolution. Host profile withdrawal stops live use.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
	// AllowInsecure permits an HTTP or self-signed HTTPS endpoint on a private
	// address. This is an explicit operator opt-in that weakens the default
	// public-HTTPS transport contract. It never bypasses host admission.
	// +optional
	AllowInsecure bool `json:"allowInsecure,omitempty"`
	// Parameters is a bounded JSON object the model gateway merges into every
	// provider request made through this connection, for example
	// {"temperature":0.7} or {"chat_template_kwargs":{"enable_thinking":false}}.
	// It is operator policy pinned on the host side: the guest never sees it, a
	// guest request that carries one of its keys is refused, and request fields
	// the gateway owns (ReservedModelParameters) cannot be set. At most 16
	// top-level keys matching ^[a-z][a-z0-9_]{0,63}$, 3 levels deep and 2048
	// bytes (ValidateModelParameters). Only gateway-mediated connections
	// (secretRef or no credential) may set it.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	Parameters *apiextensionsv1.JSON `json:"parameters,omitempty"`
	// MaxOutputTokens bounds the output tokens one model request through this
	// connection may ask for. Omitted means 512. The model gateway refuses a
	// request above it; a turn's own output-token cap still applies. Only
	// gateway-mediated connections (secretRef or no credential) may set it.
	// +kubebuilder:validation:Minimum=256
	// +kubebuilder:validation:Maximum=4096
	// +optional
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
}

var modelConnectionIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (s ModelConnectionSpec) Validate() error {
	if !modelConnectionIdentifier.MatchString(s.Provider) || (s.CredentialProfile != "" && !modelConnectionIdentifier.MatchString(s.CredentialProfile)) {
		return fmt.Errorf("provider and host credential profile must be identifiers of 1–64 letters, digits, underscores or hyphens")
	}
	if s.SecretRef != "" && len(validation.IsDNS1123Subdomain(s.SecretRef)) != 0 {
		return fmt.Errorf("secretRef must name a Kubernetes Secret in this namespace")
	}
	if s.Protocol != "openai-chat" && s.Protocol != "anthropic-messages" {
		return fmt.Errorf("protocol must be openai-chat or anthropic-messages")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || len(s.Endpoint) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(s.Endpoint, "\r\n\x00") {
		return fmt.Errorf("endpoint must be an HTTP(S) API URL without credentials, query or fragment")
	}
	if s.CredentialProfile != "" {
		if s.SecretRef != "" {
			return fmt.Errorf("choose a host credential profile or a Kubernetes Secret, not both")
		}
		if _, err := ModelEndpointOriginInsecure(s.Endpoint, s.AllowInsecure); err != nil {
			return err
		}
	}
	if _, err := s.RequestParameters(); err != nil {
		return err
	}
	if err := ValidateRequestOutputTokens(s.MaxOutputTokens); err != nil {
		return err
	}
	if s.CredentialProfile != "" && (s.Parameters != nil || s.MaxOutputTokens != 0) {
		// A host credential profile is served by the native host broker, which
		// never reads these fields: refuse instead of silently ignoring policy.
		return fmt.Errorf("parameters and maxOutputTokens are enforced by the model gateway and cannot be combined with a host credential profile")
	}
	if len(s.Models) == 0 || len(s.Models) > 128 {
		return fmt.Errorf("connection requires 1–128 model identifiers")
	}
	seen := map[string]bool{}
	for _, model := range s.Models {
		if len(model) == 0 || len(model) > 128 || strings.TrimSpace(model) != model || strings.ContainsAny(model, "\x00\r\n") || seen[model] {
			return fmt.Errorf("model identifiers must be unique, nonempty and at most 128 bytes")
		}
		seen[model] = true
	}
	return nil
}

// RequestParameters is the validated parameters object, numbers in their
// written form; nil when the connection sets none.
func (s ModelConnectionSpec) RequestParameters() (map[string]any, error) {
	if s.Parameters == nil {
		return nil, nil
	}
	if len(s.Parameters.Raw) == 0 {
		return nil, fmt.Errorf("model parameters: a JSON object is required")
	}
	return ParseModelParameters(s.Parameters.Raw)
}

// RequestOutputTokens is the output-token bound of one request through this
// connection: maxOutputTokens, or the default when omitted.
func (s ModelConnectionSpec) RequestOutputTokens() int64 {
	return EffectiveRequestOutputTokens(s.MaxOutputTokens)
}

// DigestView is the value every authority digests as this connection's spec
// (route.modelConnectionSpecSha256). Decision digests use an integer-only
// canonical JSON profile, which cannot carry a fractional parameter such as
// {"temperature":0.7}; parameters are therefore bound as one string, their
// key-sorted compact JSON text with numbers as written. A spec without
// parameters is returned unchanged, so its digest is what it always was.
func (s ModelConnectionSpec) DigestView() (any, error) {
	if s.Parameters == nil {
		return s, nil
	}
	parameters, err := s.RequestParameters()
	if err != nil {
		return nil, err
	}
	text := []byte("{}")
	if len(parameters) != 0 {
		if text, err = json.Marshal(parameters); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields["parameters"], err = json.Marshal(string(text)); err != nil {
		return nil, err
	}
	return fields, nil
}

// ModelEndpointOrigin matches the native host HTTPS transport: no userinfo,
// query credentials, fragments, alternate ports or redirect-based endpoints.
func ModelEndpointOrigin(endpoint string) (string, error) {
	return ModelEndpointOriginInsecure(endpoint, false)
}

// ModelEndpointOriginInsecure returns the egress origin for a model endpoint.
// The default keeps the public HTTPS transport contract; allowInsecure is an
// explicit opt-in that also permits HTTP and an explicit port for a private or
// self-signed endpoint.
func ModelEndpointOriginInsecure(endpoint string, allowInsecure bool) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path == "" || strings.ContainsAny(endpoint, "\r\n\x00") {
		return "", fmt.Errorf("model endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	if allowInsecure {
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("model endpoint must be an HTTP(S) URL")
		}
	} else if u.Scheme != "https" || u.Port() != "" {
		return "", fmt.Errorf("model endpoint must be a complete HTTPS URL without credentials, query, fragment or port")
	}
	return u.Scheme + "://" + u.Host, nil
}

// +kubebuilder:object:root=true
type ModelConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelConnection `json:"items"`
}

func init() { SchemeBuilder.Register(&ModelConnection{}, &ModelConnectionList{}) }
