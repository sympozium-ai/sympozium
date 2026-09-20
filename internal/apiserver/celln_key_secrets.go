package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Which Secrets in a namespace an Agent could name as its own model key. Read
// only. The answer is deliberately narrow: only Opaque Secrets that already
// hold a non-empty value under one of the two fixed model key names, and of
// those only the name. No value, no other key name and no other Secret is ever
// disclosed, and the key cannot be chosen freely, so the endpoint is not an
// oracle for what else a namespace stores.

// CellnKeySecret is one Secret that holds the asked model key.
type CellnKeySecret struct {
	Name string `json:"name"`
	// Key is the fixed key name the Secret holds; never its value.
	Key string `json:"key"`
	// Managed reports a Secret the console created for a model connection.
	Managed bool `json:"managed,omitempty"`
}

func (s *Server) listCellnKeySecrets(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	key := r.URL.Query().Get("key")
	if key != connectionSecretKey("anthropic-messages") && key != connectionSecretKey("openai-chat") {
		http.Error(w, "key must be ANTHROPIC_API_KEY or OPENAI_API_KEY", http.StatusBadRequest)
		return
	}
	var secrets corev1.SecretList
	if err := s.client.List(r.Context(), &secrets, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []CellnKeySecret{}
	for _, secret := range secrets.Items {
		if !holdsModelKey(&secret, key) {
			continue
		}
		out = append(out, CellnKeySecret{Name: secret.Name, Key: key, Managed: secret.Labels["sympozium.ai/model-connection"] != ""})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, out)
}

// holdsModelKey reports a Secret usable as a model key: Opaque, not being
// deleted, a non-empty value under key, and not the placeholder the API writes
// for keyless local harness endpoints.
func holdsModelKey(secret *corev1.Secret, key string) bool {
	if secret.Type != "" && secret.Type != corev1.SecretTypeOpaque {
		return false
	}
	if secret.DeletionTimestamp != nil || secret.Labels["sympozium.ai/credential-kind"] == "harness-local-compatibility" {
		return false
	}
	return len(secret.Data[key]) != 0
}

// cellnConnectionSecret is the authRefs grant for the Secret a native Celln
// Agent's own connection uses, after checking it exists and holds the key the
// connection's protocol fixes. Nil for a connection without a Secret (a host
// profile, or none).
func (s *Server) cellnConnectionSecret(ctx context.Context, namespace, connectionRef string) (*api.SecretRef, error) {
	var connection api.ModelConnection
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: connectionRef}, &connection); err != nil {
		return nil, fmt.Errorf("model connection %q unavailable: %w", connectionRef, err)
	}
	if connection.Spec.SecretRef == "" {
		return nil, nil
	}
	key := connectionSecretKey(connection.Spec.Protocol)
	var secret corev1.Secret
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: connection.Spec.SecretRef}, &secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, fmt.Errorf("secret %q not found in namespace %q", connection.Spec.SecretRef, namespace)
		}
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}
	if !holdsModelKey(&secret, key) {
		return nil, fmt.Errorf("secret %q does not hold a value under %s, the key a %s connection requires", secret.Name, key, connection.Spec.Protocol)
	}
	return &api.SecretRef{Provider: connection.Spec.Provider, Secret: secret.Name}, nil
}
