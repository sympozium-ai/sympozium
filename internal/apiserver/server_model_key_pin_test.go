package apiserver

import (
	"context"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

func pinnedOrigin(t *testing.T, cl client.Client, name string) string {
	t.Helper()
	var s corev1.Secret
	if err := cl.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &s); err != nil {
		t.Fatal(err)
	}
	return s.Annotations[modelkey.OriginAnnotation]
}

func attackerConnection(secretRef string) api.ModelConnectionSpec {
	return api.ModelConnectionSpec{Provider: "anthropic", Protocol: "anthropic-messages", Endpoint: "https://attacker.example/v1/messages", SecretRef: secretRef, Models: []string{"m"}}
}

// An API caller who names somebody's existing, unpinned Secret in a
// connection must not choose where that key may go.
func TestSavingAConnectionDoesNotPinAnExistingSecret(t *testing.T) {
	victim := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "victim-key", Namespace: "default"}, Data: map[string][]byte{"ANTHROPIC_API_KEY": []byte("sk-ant-DUMMY")}}
	srv, cl := newTestServer(t, victim)
	if _, _, err := srv.saveModelConnection(context.Background(), "default", "evil", attackerConnection("victim-key"), ""); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := pinnedOrigin(t, cl, "victim-key"); got != "" {
		t.Fatalf("an existing Secret was pinned by an API caller to %q", got)
	}
}

func TestSavingAConnectionRefusesASecretPinnedElsewhere(t *testing.T) {
	victim := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "victim-key", Namespace: "default", Annotations: map[string]string{modelkey.OriginAnnotation: "https://api.anthropic.com"}}, Data: map[string][]byte{"ANTHROPIC_API_KEY": []byte("sk-ant-DUMMY")}}
	srv, cl := newTestServer(t, victim)
	_, code, err := srv.saveModelConnection(context.Background(), "default", "evil", attackerConnection("victim-key"), "")
	if err == nil || code != http.StatusConflict {
		t.Fatalf("expected 409, got %d %v", code, err)
	}
	if got := pinnedOrigin(t, cl, "victim-key"); got != "https://api.anthropic.com" {
		t.Fatalf("pin moved to %q", got)
	}
}

// A key a person enters with the connection is pinned to its endpoint.
func TestSavingAConnectionWithAnEnteredKeyPinsIt(t *testing.T) {
	srv, cl := newTestServer(t)
	spec := attackerConnection("")
	spec.Endpoint = "https://llm.internal:8443/v1/messages"
	connection, _, err := srv.saveModelConnection(context.Background(), "default", "mine", spec, "sk-ant-DUMMY")
	if err != nil {
		t.Fatal(err)
	}
	if got := pinnedOrigin(t, cl, connection.Spec.SecretRef); got != "https://llm.internal:8443" {
		t.Fatalf("pin = %q", got)
	}
}

func TestPinNewModelKey(t *testing.T) {
	fresh := func(name string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Data: map[string][]byte{"OPENAI_API_KEY": []byte("sk-DUMMY")}}
	}
	_, cl := newTestServer(t, fresh("custom"), fresh("cloud"), fresh("local"))
	ctx := context.Background()
	if err := pinNewModelKey(ctx, cl, "default", "custom", "custom", "http://llama.lan:8080/v1"); err != nil {
		t.Fatal(err)
	}
	if err := pinNewModelKey(ctx, cl, "default", "cloud", "openai", ""); err != nil {
		t.Fatal(err)
	}
	if err := pinNewModelKey(ctx, cl, "default", "local", "ollama", ""); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"custom": "http://llama.lan:8080", "cloud": "https://api.openai.com", "local": ""} {
		if got := pinnedOrigin(t, cl, name); got != want {
			t.Errorf("%s pin = %q, want %q", name, got, want)
		}
	}
}
