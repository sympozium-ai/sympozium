package cellninstall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PreflightTimeout bounds one probe request to a backend.
const PreflightTimeout = 30 * time.Second

// PreflightBackend sends the smallest real chat request a backend accepts
// (one output token) with the key the fleet will use, so a dead provider,
// a wrong endpoint or a bad key is reported at install time instead of as a
// lost parent later. A models listing is not enough: a provider can list
// models while refusing completions, as DeepSeek did during a 503 outage.
func PreflightBackend(ctx context.Context, httpClient *http.Client, b FleetBackend, credential string) error {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: PreflightTimeout}
	}
	var body map[string]any
	headers := map[string]string{"Content-Type": "application/json"}
	switch b.Model.Protocol {
	case "anthropic-messages":
		body = map[string]any{"model": b.Model.Name, "max_tokens": 1, "messages": []map[string]string{{"role": "user", "content": "ping"}}}
		headers["x-api-key"] = credential
		headers["anthropic-version"] = "2023-06-01"
	default:
		body = map[string]any{"model": b.Model.Name, "max_tokens": 1, "messages": []map[string]string{{"role": "user", "content": "ping"}}}
		headers["Authorization"] = "Bearer " + credential
	}
	// The backend's parameters ride along exactly as the Celln host will send
	// them, so one the provider refuses (HTTP 400) is reported here and not as
	// lost turns. They never replace a field of the probe itself.
	for key, value := range b.Model.Parameters {
		if _, taken := body[key]; !taken {
			body[key] = value
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.Model.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("backend %s: %w", b.Name, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("backend %s: %s is unreachable from here: %w (if only the fleet nodes can reach it, skip the probe: --celln-fleet-skip-preflight, or skipPreflight in the API)", b.Name, b.Model.Endpoint, err)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("backend %s: %s refused the key (HTTP %d): %s", b.Name, b.Model.Endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	case resp.StatusCode >= 500:
		return fmt.Errorf("backend %s: %s is not serving completions (HTTP %d): %s (retry later, or skip the probe)", b.Name, b.Model.Endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	default:
		if len(b.Model.Parameters) != 0 {
			return fmt.Errorf("backend %s: %s answered HTTP %d to a minimal chat request carrying the model parameters %s: %s (check the parameters, the model name and the protocol)", b.Name, b.Model.Endpoint, resp.StatusCode, ModelParametersJSON(b.Model.Parameters), strings.TrimSpace(string(snippet)))
		}
		return fmt.Errorf("backend %s: %s answered HTTP %d to a minimal chat request: %s (check the model name and protocol)", b.Name, b.Model.Endpoint, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
}

// PreflightCredential is the key a probe uses: the named file, else the
// entry already published for the backend, else the keyless placeholder.
func PreflightCredential(ctx context.Context, store client.Client, b FleetBackend) (string, error) {
	if b.CredentialFile != "" {
		return readBackendCredential(b.CredentialFile)
	}
	if store != nil {
		var existing corev1.Secret
		err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, &existing)
		if err != nil && !apierrors.IsNotFound(err) {
			return "", err
		}
		if err == nil && len(existing.Data[b.Name]) != 0 {
			return string(existing.Data[b.Name]), nil
		}
	}
	if !b.Model.NeedsCredential() {
		return fleetModelPlaceholderCredential, nil
	}
	return "", fmt.Errorf("backend %s needs a key: pass credential-file=/path (or --celln-fleet-model-credential-file for the native backend)", b.Name)
}

// DetectModel asks an OpenAI-compatible server which model it serves (GET
// …/v1/models) and returns the first one. A local server such as
// llama-server serves exactly one, so the operator needs only its address.
func DetectModel(ctx context.Context, httpClient *http.Client, endpoint, credential string) (string, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: PreflightTimeout}
	}
	base := strings.TrimRight(endpoint, "/")
	for _, suffix := range []string{"/chat/completions", "/completions", "/messages"} {
		base = strings.TrimSuffix(base, suffix)
	}
	ctx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return "", err
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot list models at %s/models: %w", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cannot list models at %s/models (HTTP %d); give the model name", base, resp.StatusCode)
	}
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&listing); err != nil {
		return "", fmt.Errorf("%s/models did not return a model list: %w", base, err)
	}
	for _, m := range listing.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("%s/models lists no model; give the model name", base)
}
