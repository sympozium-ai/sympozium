package modelkey

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func anthropicKey(name, pinned string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: map[string][]byte{"ANTHROPIC_API_KEY": []byte("sk-ant-DUMMY")}}
	if pinned != "" {
		s.Annotations = map[string]string{OriginAnnotation: pinned}
	}
	return s
}

// granting is an Agent that grants the keys "k" and "local" for provider.
func granting(provider string) *api.Agent {
	a := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "a"}}
	a.Spec.AuthRefs = []api.SecretRef{{Provider: provider, Secret: "k"}, {Provider: provider, Secret: "local"}}
	return a
}

func pinOf(t *testing.T, c client.Client, name string) string {
	t.Helper()
	var s corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	return s.Annotations[OriginAnnotation]
}

func TestRunOrigin(t *testing.T) {
	for _, tc := range []struct{ provider, baseURL, want string }{
		{"anthropic", "", "https://api.anthropic.com"},
		{"openai", "", "https://api.openai.com"},
		{"groq", "", "https://api.openai.com"}, // what the runner really reaches
		{"ollama", "", "http://ollama.default.svc:11434"},
		{"", "", "https://api.openai.com"},
		{"azure-openai", "", ""},
		{"anthropic", "http://listener.attacker.svc:443", "http://listener.attacker.svc:443"},
		{"anthropic", "https://API.Anthropic.com/v1/", "https://api.anthropic.com"},
		{"openrouter", "https://openrouter.ai/api/v1", "https://openrouter.ai"},
	} {
		got, err := RunOrigin(tc.provider, tc.baseURL)
		if err != nil || got != tc.want {
			t.Errorf("RunOrigin(%q, %q) = %q, %v; want %q", tc.provider, tc.baseURL, got, err, tc.want)
		}
	}
	for _, bad := range []string{"ftp://x", "http://user:pw@x", "listener:443", "http://"} {
		if _, err := RunOrigin("anthropic", bad); err == nil {
			t.Errorf("RunOrigin accepted %q", bad)
		}
	}
}

// The reported attack: an unpinned key, a run that points baseURL at a
// listener. Refused, and the key is not pinned to the listener.
func TestAnUnpinnedKeyIsNotSentToARunsCustomBaseURL(t *testing.T) {
	c := store(t, anthropicKey("k", ""))
	err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), "anthropic", "http://listener.attacker.svc:443", "k")
	var notAllowed *OriginNotAllowedError
	if !errors.As(err, &notAllowed) || !IsRefusal(err) || notAllowed.Origin != "http://listener.attacker.svc:443" {
		t.Fatalf("expected OriginNotAllowedError, got %v", err)
	}
	if got := pinOf(t, c, "k"); got != "" {
		t.Fatalf("refused run pinned the key to %q", got)
	}
}

// Zero-config migration: an unpinned key used at its provider's public
// endpoint keeps working and is pinned there on first use.
func TestAnUnpinnedKeyGoesToItsProvidersDefaultAndIsPinnedThere(t *testing.T) {
	for _, baseURL := range []string{"", "https://api.anthropic.com/v1"} {
		c := store(t, anthropicKey("k", ""))
		if err := CheckRunEndpoint(context.Background(), c, granting("anthropic"), "anthropic", baseURL, "k"); err != nil {
			t.Fatalf("check %q: %v", baseURL, err)
		}
		if got := pinOf(t, c, "k"); got != "" {
			t.Fatalf("read-only check pinned the key: %q", got)
		}
		if err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), "anthropic", baseURL, "k"); err != nil {
			t.Fatalf("authorize %q: %v", baseURL, err)
		}
		if got := pinOf(t, c, "k"); got != "https://api.anthropic.com" {
			t.Fatalf("pin = %q", got)
		}
	}
}

// An unpinned key may not be moved to another provider's public endpoint
// either: the default is per provider.
func TestAnUnpinnedKeyIsNotSentToAnotherProvidersEndpoint(t *testing.T) {
	c := store(t, anthropicKey("k", ""))
	if err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), "anthropic", "https://openrouter.ai/api/v1", "k"); !IsRefusal(err) {
		t.Fatalf("expected refusal, got %v", err)
	}
}

// An operator who manages the Secret allows a custom endpoint by pinning it.
func TestAnOperatorPinnedCustomOriginIsAccepted(t *testing.T) {
	c := store(t, anthropicKey("k", "https://llm-gateway.internal:8443"))
	if err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), "anthropic", "https://llm-gateway.internal:8443/anthropic", "k"); err != nil {
		t.Fatal(err)
	}
}

// Once pinned, a run cannot move the key: not to another host, not back to
// the provider default, not to another provider's default.
func TestARunCannotOverrideAPinnedOrigin(t *testing.T) {
	for _, tc := range []struct{ name, pinned, provider, baseURL string }{
		{"custom pin, attacker baseURL", "https://llm-gateway.internal:8443", "anthropic", "http://listener.attacker.svc:443"},
		{"custom pin, empty baseURL", "https://llm-gateway.internal:8443", "anthropic", ""},
		{"default pin, attacker baseURL", "https://api.anthropic.com", "anthropic", "https://evil.example/v1"},
		{"default pin, other provider", "https://api.anthropic.com", "openai", ""},
		{"same host other port", "https://llm-gateway.internal:8443", "anthropic", "https://llm-gateway.internal:9443"},
		{"same host other scheme", "https://llm-gateway.internal:8443", "anthropic", "http://llm-gateway.internal:8443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := store(t, anthropicKey("k", tc.pinned))
			err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), tc.provider, tc.baseURL, "k")
			var mismatch *OriginMismatchError
			if !errors.As(err, &mismatch) || !IsRefusal(err) {
				t.Fatalf("expected OriginMismatchError, got %v", err)
			}
			if got := pinOf(t, c, "k"); got != tc.pinned {
				t.Fatalf("pin moved to %q", got)
			}
		})
	}
}

func TestEndpointCheckLeavesWhatHasNoKeyToProtect(t *testing.T) {
	placeholder := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "local"}, Data: map[string][]byte{"OPENAI_API_KEY": []byte(PlaceholderKey)}}
	c := store(t, placeholder, anthropicKey("k", ""))
	for _, tc := range []struct{ name, provider, baseURL, secret string }{
		{"no secret", "anthropic", "http://anywhere:1", ""},
		{"keyless placeholder", "custom", "http://llama.local:8080/v1", "local"},
		{"bedrock never sends the key", "bedrock", "http://anywhere:1", "k"},
	} {
		if err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), tc.provider, tc.baseURL, tc.secret); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

func TestLocalProviderDefaultsNeedAPin(t *testing.T) {
	c := store(t, anthropicKey("k", ""))
	err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), "ollama", "", "k")
	if !IsRefusal(err) {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestIsRoutingEnvVar(t *testing.T) {
	for _, name := range []string{"MODEL_BASE_URL", "ANTHROPIC_BASE_URL", "openai_base_url", "MODEL_PROVIDER_HEADERS", "AZURE_OPENAI_ENDPOINT", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "MODEL_PROVIDER"} {
		if !IsRoutingEnvVar(name) {
			t.Errorf("%s should be refused", name)
		}
	}
	for _, name := range []string{"MY_SETTING", "LOG_LEVEL", "MODEL_NAME", "HTTPS_PROXY"} {
		if IsRoutingEnvVar(name) {
			t.Errorf("%s should be allowed", name)
		}
	}
}

// The run chooses its provider; it must not choose where an unpinned key is
// pinned. An Anthropic key run as "openai" is refused, not pinned to OpenAI.
func TestTheRunsProviderDoesNotChooseTheZeroConfigPin(t *testing.T) {
	c := store(t, anthropicKey("k", ""))
	if err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), "openai", "", "k"); !IsRefusal(err) {
		t.Fatalf("expected refusal, got %v", err)
	}
	if got := pinOf(t, c, "k"); got != "" {
		t.Fatalf("pinned to %q", got)
	}
	// A provider-agnostic grant of a Secret holding only an Anthropic key is
	// an Anthropic grant.
	agnostic := granting("")
	if err := AuthorizeRunEndpoint(context.Background(), c, agnostic, "openai", "", "k"); !IsRefusal(err) {
		t.Fatalf("expected refusal, got %v", err)
	}
	if err := AuthorizeRunEndpoint(context.Background(), c, agnostic, "anthropic", "", "k"); err != nil {
		t.Fatal(err)
	}
}

// The runner trims nothing: "bedrock " runs as an OpenAI-compatible provider.
func TestAProviderWithWhitespaceIsRefused(t *testing.T) {
	c := store(t, anthropicKey("k", ""))
	for _, p := range []string{"bedrock ", " anthropic", "anthropic\n"} {
		if err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), p, "http://listener.attacker.svc:443", "k"); !IsRefusal(err) {
			t.Fatalf("%q: expected refusal, got %v", p, err)
		}
	}
	// Casing is what the runner normalises too.
	if err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), "Anthropic", "", "k"); err != nil {
		t.Fatal(err)
	}
}

// A Secret created after admission would reach the run's pod (its Secret refs
// are optional): a missing key passes only for its provider's public endpoint.
func TestAMissingSecretPassesOnlyForThePublicEndpoint(t *testing.T) {
	c := store(t)
	if err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), "anthropic", "", "k"); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeRunEndpoint(context.Background(), c, granting("anthropic"), "anthropic", "http://listener.attacker.svc:443", "k"); !IsRefusal(err) {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestALocalDefaultCanBePinned(t *testing.T) {
	c := store(t, anthropicKey("k", "http://ollama.default.svc:11434"))
	if err := AuthorizeRunEndpoint(context.Background(), c, granting("ollama"), "ollama", "", "k"); err != nil {
		t.Fatal(err)
	}
}

func TestRunEnvAllowed(t *testing.T) {
	a := granting("anthropic")
	a.Spec.Agents.Default.Env = map[string]string{"HTTPS_PROXY": "http://corp-proxy:3128"}
	if !RunEnvAllowed(a, "HTTPS_PROXY", "http://corp-proxy:3128") {
		t.Fatal("the Agent's own proxy must be allowed")
	}
	if RunEnvAllowed(a, "HTTPS_PROXY", "http://listener.attacker.svc:443") || RunEnvAllowed(a, "HTTP_PROXY", "http://x") {
		t.Fatal("a run's own proxy must be refused")
	}
	a.Spec.Agents.Default.Env["MODEL_BASE_URL"] = "http://x"
	if RunEnvAllowed(a, "MODEL_BASE_URL", "http://x") {
		t.Fatal("routing variables are never taken from env")
	}
	if !RunEnvAllowed(a, "LOG_LEVEL", "debug") {
		t.Fatal("ordinary env refused")
	}
}
