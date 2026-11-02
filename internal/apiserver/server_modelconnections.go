package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	"net/http"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *Server) listModelConnections(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	var list api.ModelConnectionList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list.Items == nil {
		list.Items = []api.ModelConnection{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list.Items)
}

func (s *Server) createModelConnection(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	var req struct {
		Name string                  `json:"name"`
		Spec api.ModelConnectionSpec `json:"spec"`
		// APIKey is optional. When set, the server writes a Kubernetes Secret
		// holding the credential and points the connection at it, so the Auth
		// step behaves exactly like an inline run credential.
		APIKey string `json:"apiKey,omitempty"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		http.Error(w, "invalid model connection JSON", http.StatusBadRequest)
		return
	}
	if len(validation.IsDNS1123Subdomain(req.Name)) != 0 {
		http.Error(w, "valid connection name required", http.StatusBadRequest)
		return
	}
	connection, code, err := s.saveModelConnection(r.Context(), ns, req.Name, req.Spec, req.APIKey)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(connection)
}

// saveModelConnection validates and stores a connection: with an API key it
// writes the Secret holding it first and points the connection at it; the
// Secret, however it was made, is pinned to the connection's origin. The code
// is the HTTP status to answer with: 201 for a new connection, 200 for a
// re-saved one, or the refusal's. POST /api/v1/model-connections and the
// one-call Agent create share it.
func (s *Server) saveModelConnection(ctx context.Context, ns, name string, spec api.ModelConnectionSpec, apiKey string) (*api.ModelConnection, int, error) {
	if apiKey != "" && spec.SecretRef != "" {
		return nil, http.StatusBadRequest, errors.New("provide an API key or a Secret reference, not both")
	}
	if err := spec.Validate(); err != nil {
		return nil, http.StatusBadRequest, err
	}
	// A tenant-authored connection is sized to what the namespace's Celln
	// policies can pay for, instead of failing its first run on a bound the
	// scope's ceiling cannot afford.
	s.clampConnectionOutputTokens(ctx, ns, &spec)
	createdSecret := false
	if apiKey != "" {
		secretName := defaultProviderSecretName(name, spec.Provider)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by":  "sympozium",
					"sympozium.ai/model-connection": name,
				},
			},
			// Data, not StringData: what the API server would convert is set
			// here so the ownership check that follows in the same request
			// reads the key back whatever client stored it.
			Data: map[string][]byte{spec.CredentialSecretKey(): []byte(apiKey)},
		}
		created, err := createOrMergeSecret(ctx, s.client, secret)
		if err != nil {
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to create credentials secret: %w", err)
		}
		createdSecret = created
		spec.SecretRef = secretName
	}
	// A key a person entered here is pinned to the origin this connection
	// names: the gateway sends it nowhere else, and a connection moved to
	// another origin needs a new key entered by a person. An existing Secret
	// is only checked: the API caller has shown no right to direct it, so an
	// unpinned one is left for the controller's claim, which pins it only for
	// an Agent that grants it.
	if spec.SecretRef != "" {
		if err := s.pinConnectionSecret(ctx, ns, spec, createdSecret); err != nil {
			if modelkey.IsRefusal(err) {
				return nil, http.StatusConflict, err
			}
			return nil, http.StatusInternalServerError, fmt.Errorf("failed to pin the key to its origin: %w", err)
		}
	}

	connection := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: spec}
	if err := s.client.Create(ctx, connection); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			return nil, http.StatusConflict, err
		}
		// Re-saving a connection (for example when the wizard is retried) is an
		// update, not an error, so the same provider/auth/model selection wins.
		var existing api.ModelConnection
		if err := s.client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &existing); err != nil {
			return nil, http.StatusInternalServerError, err
		}
		existing.Spec = spec
		if err := s.client.Update(ctx, &existing); err != nil {
			return nil, http.StatusConflict, err
		}
		return &existing, http.StatusOK, nil
	}
	return connection, http.StatusCreated, nil
}

// connectionSecretKey returns the Secret key the harness adapters read when a
// connection names none. The maintained OpenAI-compatible adapter always
// reads OPENAI_API_KEY, even when the upstream provider label differs.
func connectionSecretKey(protocol string) string { return api.DefaultSecretKey(protocol) }

// pinConnectionSecret pins a Secret the API created in this request from an
// entered key to the connection's endpoint origin. For any other Secret it
// only refuses one already pinned elsewhere: pinning an existing key would let
// any API caller choose where somebody else's key may be sent. An unpinned or
// missing Secret is left for the controller's claim, which pins it only for an
// Agent that grants it.
func (s *Server) pinConnectionSecret(ctx context.Context, namespace string, spec api.ModelConnectionSpec, created bool) error {
	origin, err := spec.Origin()
	if err != nil {
		return err
	}
	var secret corev1.Secret
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: spec.SecretRef}, &secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if modelkey.PinnedTo(&secret, origin) {
		return nil
	}
	if pinned := modelkey.PinnedOrigin(&secret); pinned != "" {
		return &modelkey.OriginMismatchError{Secret: secret.Name, Pinned: pinned, Origin: origin}
	}
	if !created {
		return nil
	}
	if err := modelkey.Pin(&secret, origin); err != nil {
		return err
	}
	return s.client.Update(ctx, &secret)
}

// clampConnectionOutputTokens lowers a connection's per-request output bound
// to what one turn can afford under the namespace's Celln policy ceilings
// (ceiling / TurnModelRequests). A namespace without Celln policies, or a
// bound already affordable, is left as stated.
func (s *Server) clampConnectionOutputTokens(ctx context.Context, namespace string, spec *api.ModelConnectionSpec) {
	if spec.MaxOutputTokens == 0 {
		return
	}
	authorised, err := cellnplatform.AuthorisedProfiles(ctx, s.client, namespace)
	if err != nil || len(authorised) == 0 {
		return
	}
	var affordable int64
	for _, a := range authorised {
		if turn := a.Policy.Spec.Ceilings.MaxOutputTokens / api.TurnModelRequests; turn > affordable {
			affordable = turn
		}
	}
	if affordable >= api.MinRequestOutputTokens && spec.MaxOutputTokens > affordable {
		spec.MaxOutputTokens = affordable
	}
}
