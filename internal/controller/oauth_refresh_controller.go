package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	// oauthAuthModeLabel is the label that identifies OAuth-managed secrets.
	oauthAuthModeLabel = "sympozium.ai/auth-mode"

	// refreshBufferDuration is how early before expiry we refresh the token.
	refreshBufferDuration = 5 * time.Minute

	// anthropicTokenEndpoint is Anthropic's OAuth token refresh endpoint.
	anthropicTokenEndpoint = "https://console.anthropic.com/v1/oauth/token"

	// refreshErrorBackoff is the requeue delay after a failed refresh.
	refreshErrorBackoff = 30 * time.Second
)

// OAuthRefreshReconciler watches Secrets labeled with sympozium.ai/auth-mode=oauth
// and refreshes OAuth tokens before they expire.
type OAuthRefreshReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Log        logr.Logger
	HTTPClient *http.Client
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update;patch

// Reconcile checks if the OAuth token in the Secret is near expiry and refreshes it.
func (r *OAuthRefreshReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("secret", req.NamespacedName)

	var secret corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &secret); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only process secrets with the oauth label.
	if secret.Labels[oauthAuthModeLabel] != "oauth" {
		return ctrl.Result{}, nil
	}

	// Read token data from the secret.
	refreshToken := string(secret.Data["refresh-token"])
	expiresAtStr := string(secret.Data["expires-at"])

	if refreshToken == "" {
		log.Info("Secret missing refresh-token key, skipping")
		return ctrl.Result{}, nil
	}

	// Parse expiry time.
	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		log.Error(err, "failed to parse expires-at", "value", expiresAtStr)
		// If we can't parse, try refreshing now.
		expiresAt = time.Now()
	}

	// Check if token needs refresh.
	timeUntilExpiry := time.Until(expiresAt)
	if timeUntilExpiry > refreshBufferDuration {
		// Token is still valid. Requeue to check again before it expires.
		requeueAfter := timeUntilExpiry - refreshBufferDuration
		log.Info("OAuth token still valid", "expiresAt", expiresAt, "requeueAfter", requeueAfter)
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// Token is expired or about to expire — refresh it.
	log.Info("Refreshing OAuth token", "expiresAt", expiresAt)

	newToken, newExpiresAt, err := r.refreshOAuthToken(ctx, refreshToken)
	if err != nil {
		log.Error(err, "failed to refresh OAuth token")
		r.annotateRefreshStatus(&secret, fmt.Sprintf("refresh failed: %v", err))
		_ = r.Update(ctx, &secret)
		return ctrl.Result{RequeueAfter: refreshErrorBackoff}, nil
	}

	// Update the secret with the new token.
	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data["oauth-token"] = []byte(newToken)
	secret.Data["expires-at"] = []byte(newExpiresAt.Format(time.RFC3339))
	r.annotateRefreshStatus(&secret, fmt.Sprintf("refreshed at %s", time.Now().UTC().Format(time.RFC3339)))

	if err := r.Update(ctx, &secret); err != nil {
		log.Error(err, "failed to update secret with refreshed token")
		return ctrl.Result{RequeueAfter: refreshErrorBackoff}, nil
	}

	log.Info("OAuth token refreshed successfully", "newExpiresAt", newExpiresAt)

	// Requeue for next refresh.
	requeueAfter := time.Until(newExpiresAt) - refreshBufferDuration
	if requeueAfter < time.Minute {
		requeueAfter = time.Minute
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// annotateRefreshStatus sets an annotation on the secret with the refresh status.
func (r *OAuthRefreshReconciler) annotateRefreshStatus(secret *corev1.Secret, status string) {
	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	secret.Annotations["sympozium.ai/oauth-refresh-status"] = status
}

// oauthTokenResponse represents the response from the OAuth token endpoint.
type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// refreshOAuthToken calls the OAuth token endpoint to refresh the access token.
func (r *OAuthRefreshReconciler) refreshOAuthToken(ctx context.Context, refreshToken string) (string, time.Time, error) {
	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicTokenEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var tokenResp oauthTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return "", time.Time{}, fmt.Errorf("unmarshal response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("empty access_token in response")
	}

	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return tokenResp.AccessToken, expiresAt, nil
}

// SetupWithManager registers the OAuthRefreshReconciler with the controller manager.
func (r *OAuthRefreshReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}).
		WithEventFilter(predicate.NewPredicateFuncs(func(obj client.Object) bool {
			labels := obj.GetLabels()
			return labels != nil && labels[oauthAuthModeLabel] == "oauth"
		})).
		Complete(r)
}
