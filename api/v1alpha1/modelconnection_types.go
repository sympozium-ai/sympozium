package v1alpha1

import (
	"fmt"
	"k8s.io/apimachinery/pkg/util/validation"
	"net/url"
	"regexp"
	"strings"

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
