package apiserver

// server_github_auth.go — GitHub PAT-based authentication for the
// github-gitops skill.  The flow is:
//
//  1. Client POSTs /api/v1/skills/github-gitops/auth/token with a PAT.
//  2. Server writes the token to K8s Secret `github-gitops-token` in
//     `sympozium-system`.
//  3. Client polls GET /api/v1/skills/github-gitops/auth/status to
//     confirm the token is stored.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	githubTokenSecret  = "github-gitops-token"
	sympoziumNamespace = "sympozium-system"
)

// --------------------------------------------------------------------------
// GET /api/v1/skills/github-gitops/auth/status
// --------------------------------------------------------------------------

type githubAuthStatusResponse struct {
	Status string `json:"status"` // "idle", "complete"
}

func (s *Server) handleGithubAuthStatus(w http.ResponseWriter, r *http.Request) {
	status := "idle"

	secretKey := types.NamespacedName{Name: githubTokenSecret, Namespace: sympoziumNamespace}
	existing := &corev1.Secret{}
	if err := s.client.Get(r.Context(), secretKey, existing); err == nil {
		if token, ok := existing.Data["GH_TOKEN"]; ok && len(token) > 0 {
			status = "complete"
		}
	}

	writeJSON(w, githubAuthStatusResponse{Status: status})
}

// --------------------------------------------------------------------------
// POST /api/v1/skills/github-gitops/auth/token
// --------------------------------------------------------------------------

type githubAuthTokenRequest struct {
	Token string `json:"token"`
}

func (s *Server) handleGithubAuthToken(w http.ResponseWriter, r *http.Request) {
	var req githubAuthTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	token := strings.TrimSpace(req.Token)
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	if err := s.writeGithubTokenSecret(token); err != nil {
		http.Error(w, fmt.Sprintf("failed to store token: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, githubAuthStatusResponse{Status: "complete"})
}

// writeGithubTokenSecret upserts the github-gitops-token Secret.
func (s *Server) writeGithubTokenSecret(token string) error {
	ctx := context.Background()
	secretKey := types.NamespacedName{Name: githubTokenSecret, Namespace: sympoziumNamespace}

	existing := &corev1.Secret{}
	err := s.client.Get(ctx, secretKey, existing)
	if k8serrors.IsNotFound(err) {
		// Create.
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      githubTokenSecret,
				Namespace: sympoziumNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"app.kubernetes.io/component":  "skill-secret",
					"sympozium.ai/skill":           "github-gitops",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"GH_TOKEN": []byte(token),
			},
		}
		return s.client.Create(ctx, secret)
	}
	if err != nil {
		return fmt.Errorf("get secret: %w", err)
	}

	// Update in place.
	patch := client.MergeFrom(existing.DeepCopy())
	if existing.Data == nil {
		existing.Data = make(map[string][]byte)
	}
	existing.Data["GH_TOKEN"] = []byte(token)
	return s.client.Patch(ctx, existing, patch)
}
