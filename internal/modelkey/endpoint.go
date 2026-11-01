package modelkey

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// A run that carries its credential into its own pod (the Job and Agent
// Sandbox backends) and a harness session send it wherever their baseURL
// points. The run author is not the Secret's owner, so the run's baseURL
// cannot be what decides where a key goes: the key's origin pin
// (OriginAnnotation) does, the same pin the model gateway honours.
//
//   - A pinned key may be sent only to its pinned origin.
//   - An unpinned key may be sent only to the public endpoint of the provider
//     its Agent grants it for, and is pinned there on first use, so existing
//     setups that use the provider default keep working with no
//     configuration.
//   - Any other destination (a self-hosted gateway, Azure, a local model
//     server) needs someone who controls the Secret to pin it there first.

// wellKnownOrigins are the public API origins a provider's key may be sent to
// without an explicit pin.
var wellKnownOrigins = map[string]string{
	"anthropic":  "https://api.anthropic.com",
	"openai":     "https://api.openai.com",
	"groq":       "https://api.groq.com",
	"mistral":    "https://api.mistral.ai",
	"deepseek":   "https://api.deepseek.com",
	"openrouter": "https://openrouter.ai",
	"google":     "https://generativelanguage.googleapis.com",
	"gemini":     "https://generativelanguage.googleapis.com",
	"xai":        "https://api.x.ai",
}

// CanonicalProvider is the provider name as the agent-runner reads it:
// lowercase, and "openai" when empty (the runner's default).
func CanonicalProvider(provider string) string {
	if provider == "" {
		return "openai"
	}
	return strings.ToLower(provider)
}

// RunnerDefaultOrigin is the origin the agent-runner sends a provider's
// requests to when the run sets no baseURL (cmd/agent-runner provider_*.go):
// the Anthropic SDK default, the runner's local defaults, and the OpenAI SDK
// default for every other OpenAI-compatible provider. Azure needs a baseURL
// and Bedrock does not use one; both return "".
func RunnerDefaultOrigin(provider string) string {
	switch CanonicalProvider(provider) {
	case "anthropic":
		return wellKnownOrigins["anthropic"]
	case "ollama":
		return "http://ollama.default.svc:11434"
	case "lm-studio":
		return "http://localhost:1234"
	case "llama-server", "unsloth":
		return "http://localhost:8080"
	case "azure-openai", "bedrock":
		return ""
	default:
		return wellKnownOrigins["openai"]
	}
}

// WellKnownOrigin is the provider's public API origin, or "" for a provider
// without one.
func WellKnownOrigin(provider string) string {
	return wellKnownOrigins[CanonicalProvider(provider)]
}

// RunOrigin is the origin (scheme://host[:port], lowercase) a run's credential
// is sent to: the baseURL's when set, else the runner's default for the
// provider ("" for Azure without a baseURL, and Bedrock).
func RunOrigin(provider, baseURL string) (string, error) {
	if strings.TrimSpace(baseURL) == "" {
		return RunnerDefaultOrigin(provider), nil
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.ContainsAny(baseURL, "\r\n\x00") {
		return "", fmt.Errorf("model baseURL %q must be an HTTP(S) URL without credentials", baseURL)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

// OriginNotAllowedError reports a key asked to go somewhere it is not pinned
// to and that is not its granted provider's public endpoint. Only someone who
// controls the Secret may send it there, by pinning it.
type OriginNotAllowedError struct {
	Secret, Provider, Origin, Reason string
}

func (e *OriginNotAllowedError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("model key Secret %q may not be used: %s", e.Secret, e.Reason)
	}
	return fmt.Sprintf("model key Secret %q may not be sent to %s: an unpinned key goes only to the public endpoint of the provider its Agent grants it for; to use this endpoint, someone who manages the Secret must pin it: kubectl annotate secret %s %s=%s", e.Secret, e.Origin, e.Secret, OriginAnnotation, e.Origin)
}

// CheckRunEndpoint is the read-only form of AuthorizeRunEndpoint, for
// admission.
func CheckRunEndpoint(ctx context.Context, c client.Reader, agent *api.Agent, provider, baseURL, secret string) error {
	_, err := decideRunEndpoint(ctx, c, agent, provider, baseURL, secret)
	return err
}

// AuthorizeRunEndpoint decides whether a run of agent may send the key in
// secret to the origin its provider and baseURL select, and pins an unpinned
// key to its granted provider's public endpoint on first use there.
func AuthorizeRunEndpoint(ctx context.Context, c client.Client, agent *api.Agent, provider, baseURL, secret string) error {
	pin, err := decideRunEndpoint(ctx, c, agent, provider, baseURL, secret)
	if err != nil || pin == nil {
		return err
	}
	return c.Update(ctx, pin)
}

func decideRunEndpoint(ctx context.Context, c client.Reader, agent *api.Agent, provider, baseURL, secret string) (*corev1.Secret, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, nil
	}
	// The runner lowercases the provider but does not trim it, so a provider
	// with spaces would be judged as one provider here and run as another.
	if provider != strings.TrimSpace(provider) || strings.ContainsAny(provider, "\r\n\t\x00") {
		return nil, &OriginNotAllowedError{Secret: secret, Reason: fmt.Sprintf("model provider %q must not contain whitespace", provider)}
	}
	// Bedrock signs requests with the pod's AWS credentials and never sends
	// an API key or provider headers, whatever the baseURL says.
	if CanonicalProvider(provider) == "bedrock" {
		return nil, nil
	}
	origin, err := RunOrigin(provider, baseURL)
	if err != nil {
		return nil, &OriginNotAllowedError{Secret: secret, Provider: provider, Origin: strings.TrimSpace(baseURL)}
	}
	if origin == "" {
		return nil, &OriginNotAllowedError{Secret: secret, Reason: fmt.Sprintf("provider %q needs spec.model.baseURL", provider)}
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: secret}, &s); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		// The pod's Secret references are optional, so a key created later
		// would reach whatever this run points at: allow only an origin an
		// unpinned key could go to anyway.
		if origin != WellKnownOrigin(provider) {
			return nil, &OriginNotAllowedError{Secret: secret, Provider: provider, Origin: origin}
		}
		return nil, nil
	}
	if placeholderOnly(&s) {
		return nil, nil
	}
	if pinned := PinnedOrigin(&s); pinned != "" {
		if pinned != origin {
			return nil, &OriginMismatchError{Secret: secret, Pinned: pinned, Origin: origin}
		}
		return nil, nil
	}
	// Unpinned: only the public endpoint of the provider the Agent grants the
	// key for. The run chooses its provider, so the run's provider alone must
	// not choose where a key is pinned.
	granted := grantedProvider(agent, &s)
	if origin != WellKnownOrigin(provider) || granted == "" || WellKnownOrigin(granted) != origin {
		return nil, &OriginNotAllowedError{Secret: secret, Provider: provider, Origin: origin}
	}
	if err := Pin(&s, origin); err != nil {
		return nil, err
	}
	return &s, nil
}

// keyProviders names the provider a Secret's credential key is for, when a
// provider-agnostic grant leaves it to the Secret.
var keyProviders = map[string]string{
	"ANTHROPIC_API_KEY":  "anthropic",
	"OPENAI_API_KEY":     "openai",
	"GROQ_API_KEY":       "groq",
	"MISTRAL_API_KEY":    "mistral",
	"DEEPSEEK_API_KEY":   "deepseek",
	"OPENROUTER_API_KEY": "openrouter",
	"GOOGLE_API_KEY":     "google",
}

// grantedProvider is the provider the Agent grants the Secret for: the
// provider of its authRefs entries naming it (all must agree), or, for a
// provider-agnostic grant, the provider of the one credential key the Secret
// holds. "" when it cannot be told.
func grantedProvider(agent *api.Agent, s *corev1.Secret) string {
	provider := ""
	for _, ref := range agent.Spec.AuthRefs {
		if ref.Secret != s.Name || ref.Provider == "" {
			continue
		}
		p := CanonicalProvider(strings.TrimSpace(ref.Provider))
		if provider != "" && provider != p {
			return ""
		}
		provider = p
	}
	if provider != "" {
		return provider
	}
	for key := range s.Data {
		p, known := keyProviders[key]
		if !known || len(s.Data[key]) == 0 {
			continue
		}
		if provider != "" && provider != p {
			return ""
		}
		provider = p
	}
	return provider
}

// PlaceholderKey is the value the API writes for keyless local endpoints whose
// clients insist on an API key field. It is not a credential.
const PlaceholderKey = "local-no-key"

// placeholderOnly reports whether every value in the Secret is the keyless
// placeholder, so there is nothing to protect.
func placeholderOnly(s *corev1.Secret) bool {
	if len(s.Data) == 0 && len(s.StringData) == 0 {
		return false
	}
	for _, v := range s.Data {
		if string(v) != PlaceholderKey {
			return false
		}
	}
	for _, v := range s.StringData {
		if v != PlaceholderKey {
			return false
		}
	}
	return true
}

// routingEnvVars steer where the agent-runner's model client connects or what
// it sends. A run's spec.env is appended after the controller's own values,
// and the SDKs read ANTHROPIC_BASE_URL and OPENAI_BASE_URL when no baseURL is
// set, so any of these would move a key past the origin check. They are never
// taken from spec.env.
var routingEnvVars = map[string]bool{
	"MODEL_BASE_URL":         true,
	"MODEL_PROVIDER":         true,
	"MODEL_PROVIDER_HEADERS": true,
	"ANTHROPIC_BASE_URL":     true,
	"OPENAI_BASE_URL":        true,
	"AZURE_OPENAI_ENDPOINT":  true,
}

// agentOnlyEnvVars change the network path or trust store of the model client.
// A run may carry them only as its Agent declares them (Agent env is copied
// into the runs it creates), never with values of its own.
var agentOnlyEnvVars = map[string]bool{
	"HTTP_PROXY":    true,
	"HTTPS_PROXY":   true,
	"ALL_PROXY":     true,
	"NO_PROXY":      true,
	"SSL_CERT_FILE": true,
	"SSL_CERT_DIR":  true,
}

// IsRoutingEnvVar reports whether a run-supplied environment variable could
// redirect the model client, and so its credential, whatever its value.
// Names are matched without regard to case.
func IsRoutingEnvVar(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	// The AWS SDK (Bedrock) reads AWS_ENDPOINT_URL and its per-service forms.
	return routingEnvVars[name] || strings.HasPrefix(name, "AWS_ENDPOINT_URL")
}

// RunEnvAllowed reports whether a run may set name=value: never a routing
// variable, and a proxy or trust-store variable only with exactly the value
// its Agent declares.
func RunEnvAllowed(agent *api.Agent, name, value string) bool {
	if IsRoutingEnvVar(name) {
		return false
	}
	if !agentOnlyEnvVars[strings.ToUpper(strings.TrimSpace(name))] {
		return true
	}
	if agent == nil {
		return false
	}
	declared, ok := agent.Spec.Agents.Default.Env[name]
	return ok && declared == value
}
