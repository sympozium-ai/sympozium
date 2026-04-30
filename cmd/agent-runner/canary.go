package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// canaryResult is the structured JSON output of a canary health check run.
type canaryResult struct {
	Overall string        `json:"overall"` // "healthy", "degraded", "unhealthy"
	Checks  []canaryCheck `json:"checks"`
}

type canaryCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "pass" or "fail"
	Details string `json:"details"`
}

// runCanary executes deterministic health checks plus one LLM connectivity
// test and returns the structured result as JSON.
func runCanary(ctx context.Context) string {
	var checks []canaryCheck

	// Deterministic checks — no LLM needed.
	checks = append(checks, checkAPIServer(ctx))
	checks = append(checks, checkClusterInfo(ctx))
	checks = append(checks, checkKubeAPI(ctx, "nodes", "Node Discovery"))
	checks = append(checks, checkKubeAPI(ctx, "sympoziumschedules.sympozium.ai", "Schedule System"))
	checks = append(checks, checkKubeAPI(ctx, "mcpservers.sympozium.ai", "MCP Servers"))

	// LLM connectivity test using the configured provider.
	checks = append(checks, checkLLMConnection(ctx))

	// Determine overall status.
	overall := "healthy"
	criticalFailed := false
	for _, c := range checks {
		if c.Status == "fail" {
			switch c.Name {
			case "API Server", "Cluster Info", "LLM Connection":
				criticalFailed = true
			default:
				if overall == "healthy" {
					overall = "degraded"
				}
			}
		}
	}
	if criticalFailed {
		overall = "unhealthy"
	}

	result := canaryResult{Overall: overall, Checks: checks}
	b, _ := json.Marshal(result)
	return string(b)
}

// checkAPIServer hits the Sympozium API server healthz endpoint.
func checkAPIServer(ctx context.Context) canaryCheck {
	url := "http://sympozium-apiserver.sympozium-system.svc:8080/healthz"
	body, err := httpGet(ctx, url, 10*time.Second)
	if err != nil {
		return canaryCheck{Name: "API Server", Status: "fail", Details: err.Error()}
	}
	if strings.TrimSpace(body) == "ok" {
		return canaryCheck{Name: "API Server", Status: "pass", Details: "ok"}
	}
	return canaryCheck{Name: "API Server", Status: "fail", Details: "unexpected response: " + truncate(body, 100)}
}

// checkClusterInfo hits the Sympozium API server cluster endpoint.
func checkClusterInfo(ctx context.Context) canaryCheck {
	url := "http://sympozium-apiserver.sympozium-system.svc:8080/api/v1/cluster"
	body, err := httpGet(ctx, url, 10*time.Second)
	if err != nil {
		return canaryCheck{Name: "Cluster Info", Status: "fail", Details: err.Error()}
	}
	var info struct {
		Nodes int `json:"nodes"`
	}
	if json.Unmarshal([]byte(body), &info) != nil {
		return canaryCheck{Name: "Cluster Info", Status: "fail", Details: "invalid JSON"}
	}
	if info.Nodes >= 1 {
		return canaryCheck{Name: "Cluster Info", Status: "pass", Details: fmt.Sprintf("%d node(s)", info.Nodes)}
	}
	return canaryCheck{Name: "Cluster Info", Status: "fail", Details: "0 nodes"}
}

// checkKubeAPI makes a GET request to the Kubernetes API to verify a resource type is accessible.
func checkKubeAPI(ctx context.Context, resource, name string) canaryCheck {
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return canaryCheck{Name: name, Status: "fail", Details: "no service account token"}
	}

	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return canaryCheck{Name: name, Status: "fail", Details: "KUBERNETES_SERVICE_HOST not set"}
	}

	// Determine URL based on whether this is a core resource or a CRD.
	var url string
	if strings.Contains(resource, ".") {
		// CRD: use the group API path.
		parts := strings.SplitN(resource, ".", 2)
		url = fmt.Sprintf("https://%s:%s/apis/%s/v1alpha1/%s?limit=1", host, port, parts[1], parts[0])
	} else {
		url = fmt.Sprintf("https://%s:%s/api/v1/%s?limit=1", host, port, resource)
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+string(token))

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // in-cluster API server
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return canaryCheck{Name: name, Status: "fail", Details: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return canaryCheck{Name: name, Status: "pass", Details: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return canaryCheck{Name: name, Status: "fail", Details: fmt.Sprintf("HTTP %d", resp.StatusCode)}
}

// checkLLMConnection makes a single minimal LLM call to verify provider connectivity.
func checkLLMConnection(ctx context.Context) canaryCheck {
	provider := strings.ToLower(getEnv("MODEL_PROVIDER", "openai"))
	model := getEnv("MODEL_NAME", "")
	baseURL := strings.TrimRight(getEnv("MODEL_BASE_URL", ""), "/")
	apiKey := resolveAPIKey()

	if model == "" {
		return canaryCheck{Name: "LLM Connection", Status: "fail", Details: "no model configured"}
	}

	// Build a minimal provider with no tools, just to make one chat call.
	var p LLMProvider
	var err error

	switch provider {
	case "anthropic":
		p = newAnthropicProvider(apiKey, baseURL, model, "Respond with exactly: OK", "Say OK", nil)
	case "bedrock":
		p, err = newBedrockProvider(ctx, model, "Respond with exactly: OK", "Say OK", nil)
	default:
		p, err = newOpenAIProvider(provider, apiKey, baseURL, model, "Respond with exactly: OK", "Say OK", nil)
	}
	if err != nil {
		return canaryCheck{Name: "LLM Connection", Status: "fail", Details: err.Error()}
	}

	// Short timeout for the LLM ping.
	llmCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	res, chatErr := p.Chat(llmCtx)
	if chatErr != nil {
		return canaryCheck{Name: "LLM Connection", Status: "fail", Details: chatErr.Error()}
	}

	detail := fmt.Sprintf("%s/%s responded (%d tokens)", provider, model, res.InputTokens+res.OutputTokens)
	return canaryCheck{Name: "LLM Connection", Status: "pass", Details: detail}
}

// httpGet is a simple HTTP GET helper with timeout.
func httpGet(ctx context.Context, url string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(b), 200))
	}
	return string(b), nil
}

// resolveAPIKey reads the API key from env or file.
func resolveAPIKey() string {
	if key := os.Getenv("LLM_API_KEY"); key != "" {
		return key
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		return key
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return key
	}
	// Try file-based secret mount.
	if b, err := os.ReadFile("/secrets/api-key"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func emitCanaryResult(result string) {
	log.Println("canary mode — running health checks")
	// Brief pause for ipc-bridge sidecar setup.
	time.Sleep(3 * time.Second)

	res := agentResult{
		Status:   "success",
		Response: result,
	}

	// Check if any critical checks failed.
	var cr canaryResult
	if json.Unmarshal([]byte(result), &cr) == nil && cr.Overall == "unhealthy" {
		res.Status = "error"
		res.Error = "system unhealthy"
	}

	_ = os.MkdirAll("/ipc/output", 0o755)
	writeJSON("/ipc/output/result.json", res)
	_ = os.WriteFile("/ipc/done", []byte("done"), 0o644)
	if markerBytes, err := json.Marshal(res); err == nil {
		fmt.Fprintf(os.Stdout, "\n__SYMPOZIUM_RESULT__%s__SYMPOZIUM_END__\n", string(markerBytes))
	}
	log.Println("canary health checks complete")
}
