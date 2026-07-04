// Package main is the entry point for the Sympozium node-probe DaemonSet.
// It probes localhost ports for local inference providers (Ollama, vLLM, llama-cpp, LM Studio)
// and annotates the Kubernetes node with discovered providers and models.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	annotationPrefix  = "sympozium.ai/inference-"
	annotationHealthy = "sympozium.ai/inference-healthy"
	annotationLastPr  = "sympozium.ai/inference-last-probe"
	defaultConfigPath = "/etc/node-probe/config.yaml"
	defaultHealthPort = 9473
)

// defaultHost returns "host.docker.internal" when it resolves (Docker Desktop),
// otherwise "127.0.0.1". This lets probes reach host-local inference servers
// from inside kind/Docker containers without config changes.
func defaultHost() string {
	if addrs, err := net.LookupHost("host.docker.internal"); err == nil && len(addrs) > 0 {
		return "host.docker.internal"
	}
	return "127.0.0.1"
}

// ProbeConfig is the top-level config loaded from the ConfigMap.
type ProbeConfig struct {
	ProbeInterval string        `yaml:"probeInterval"`
	Targets       []ProbeTarget `yaml:"targets"`
}

// ProbeTarget describes a single inference provider to probe.
type ProbeTarget struct {
	Name       string `yaml:"name"`
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	HealthPath string `yaml:"healthPath"`
	ModelsPath string `yaml:"modelsPath"`
}

// ProbeResult holds the outcome of probing a single target.
type ProbeResult struct {
	Name   string
	Port   int
	Alive  bool
	Models []string
}

var log = ctrl.Log.WithName("node-probe")

// targetRegistry tracks alive probe targets so the reverse proxy knows where to route.
type targetRegistry struct {
	mu      sync.RWMutex
	targets map[string]ProbeTarget // name → target (only alive ones)
}

func newTargetRegistry() *targetRegistry {
	return &targetRegistry{targets: make(map[string]ProbeTarget)}
}

func (r *targetRegistry) update(results []ProbeResult, allTargets []ProbeTarget) {
	byName := make(map[string]ProbeTarget, len(allTargets))
	for _, t := range allTargets {
		byName[t.Name] = t
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.targets = make(map[string]ProbeTarget)
	for _, res := range results {
		if res.Alive {
			if t, ok := byName[res.Name]; ok {
				r.targets[res.Name] = t
			}
		}
	}
}

func (r *targetRegistry) get(name string) (ProbeTarget, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.targets[name]
	return t, ok
}

func main() {
	ctrl.SetLogger(zap.New())

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		fmt.Fprintln(os.Stderr, "NODE_NAME environment variable is required")
		os.Exit(1)
	}

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = defaultConfigPath
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	interval, reason := resolveProbeInterval(cfg.ProbeInterval)
	if reason != "" {
		log.Info(reason, "value", cfg.ProbeInterval)
	}

	restCfg, err := rest.InClusterConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get in-cluster config: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		log.Info("received shutdown signal, cleaning up annotations")
		cleanupAnnotations(context.Background(), clientset, nodeName, cfg.Targets)
		cancel()
	}()

	// Create target registry for the reverse proxy.
	registry := newTargetRegistry()

	// Start health + proxy server.
	go serveHealthAndProxy(registry)

	log.Info("starting node-probe", "node", nodeName, "interval", interval, "targets", len(cfg.Targets), "defaultHost", defaultHost())

	// Run the first probe immediately.
	runProbeLoop(ctx, clientset, nodeName, cfg.Targets, interval, registry)
}

// resolveProbeInterval parses the configured probe interval, falling back to
// 30s when it is unparseable or non-positive. A non-positive duration would
// otherwise panic time.NewTicker and crash-loop the DaemonSet. The returned
// reason is empty when the configured value was used as-is.
func resolveProbeInterval(raw string) (time.Duration, string) {
	const fallback = 30 * time.Second
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback, "invalid probeInterval, using default"
	}
	if d <= 0 {
		return fallback, "non-positive probeInterval, using default"
	}
	return d, ""
}

func loadConfig(path string) (*ProbeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var cfg ProbeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config YAML: %w", err)
	}
	if cfg.ProbeInterval == "" {
		cfg.ProbeInterval = "30s"
	}
	return &cfg, nil
}

func runProbeLoop(ctx context.Context, clientset kubernetes.Interface, nodeName string, targets []ProbeTarget, interval time.Duration, registry *targetRegistry) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately on start.
	results := probeAll(targets)
	registry.update(results, targets)
	if err := patchNodeAnnotations(ctx, clientset, nodeName, results); err != nil {
		log.Error(err, "failed to patch node annotations")
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			results := probeAll(targets)
			registry.update(results, targets)
			if err := patchNodeAnnotations(ctx, clientset, nodeName, results); err != nil {
				log.Error(err, "failed to patch node annotations")
			}
		}
	}
}

func probeAll(targets []ProbeTarget) []ProbeResult {
	results := make([]ProbeResult, 0, len(targets))
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		},
	}

	for _, t := range targets {
		result := probeTarget(client, t)
		results = append(results, result)
		if result.Alive {
			log.Info("probe succeeded", "target", t.Name, "port", t.Port, "models", result.Models)
		} else {
			log.Info("probe failed", "target", t.Name, "port", t.Port)
		}
	}
	return results
}

func probeTarget(client *http.Client, target ProbeTarget) ProbeResult {
	result := ProbeResult{
		Name: target.Name,
		Port: target.Port,
	}

	// Use modelsPath if available (it also serves as health check), otherwise healthPath.
	probePath := target.ModelsPath
	if probePath == "" {
		probePath = target.HealthPath
	}
	if probePath == "" {
		probePath = "/"
	}

	host := target.Host
	if host == "" {
		host = defaultHost()
	}
	probeURL := fmt.Sprintf("http://%s:%d%s", host, target.Port, probePath)
	resp, err := client.Get(probeURL)
	if err != nil {
		log.Info("probe connection failed", "target", target.Name, "url", probeURL, "error", err.Error())
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Info("probe got non-OK status", "target", target.Name, "url", probeURL, "status", resp.StatusCode)
		return result
	}

	result.Alive = true

	// Try to parse models from response.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return result
	}

	result.Models = parseModels(body, target.Name)
	return result
}

// parseModels extracts model names from a JSON response.
// Supports Ollama format ({"models":[{"name":"..."}]}) and
// OpenAI-compatible format ({"data":[{"id":"..."}]}).
func parseModels(body []byte, providerName string) []string {
	// Try Ollama format first.
	var ollamaResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &ollamaResp); err == nil && len(ollamaResp.Models) > 0 {
		names := make([]string, 0, len(ollamaResp.Models))
		for _, m := range ollamaResp.Models {
			// Strip tag suffix for cleaner display (e.g., "llama3:latest" → "llama3").
			name := m.Name
			if idx := strings.Index(name, ":"); idx > 0 {
				base := name[:idx]
				tag := name[idx+1:]
				if tag == "latest" {
					name = base
				}
			}
			names = append(names, name)
		}
		return names
	}

	// Try OpenAI-compatible format.
	var openaiResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &openaiResp); err == nil && len(openaiResp.Data) > 0 {
		names := make([]string, 0, len(openaiResp.Data))
		for _, m := range openaiResp.Data {
			names = append(names, m.ID)
		}
		return names
	}

	return nil
}

// buildAnnotations converts probe results into the annotation map to set on the node.
func buildAnnotations(results []ProbeResult) map[string]interface{} {
	annotations := make(map[string]interface{})
	anyHealthy := false

	for _, r := range results {
		portKey := annotationPrefix + r.Name
		modelsKey := annotationPrefix + "models-" + r.Name
		if r.Alive {
			anyHealthy = true
			annotations[portKey] = fmt.Sprintf("%d", r.Port)
			if len(r.Models) > 0 {
				annotations[modelsKey] = strings.Join(r.Models, ",")
			} else {
				annotations[modelsKey] = nil // remove if no models
			}
		} else {
			annotations[portKey] = nil   // remove annotation
			annotations[modelsKey] = nil // remove annotation
		}
	}

	if anyHealthy {
		annotations[annotationHealthy] = "true"
		annotations[annotationLastPr] = time.Now().UTC().Format(time.RFC3339)
		annotations[annotationPrefix+"proxy-port"] = fmt.Sprintf("%d", defaultHealthPort)
	} else {
		annotations[annotationHealthy] = nil
		annotations[annotationLastPr] = nil
		annotations[annotationPrefix+"proxy-port"] = nil
	}

	return annotations
}

func patchNodeAnnotations(ctx context.Context, clientset kubernetes.Interface, nodeName string, results []ProbeResult) error {
	annotations := buildAnnotations(results)

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": annotations,
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshalling patch: %w", err)
	}

	_, err = clientset.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		k8stypes.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patching node %s: %w", nodeName, err)
	}

	log.Info("patched node annotations", "node", nodeName)
	return nil
}

func cleanupAnnotations(ctx context.Context, clientset kubernetes.Interface, nodeName string, targets []ProbeTarget) {
	annotations := make(map[string]interface{})
	for _, t := range targets {
		annotations[annotationPrefix+t.Name] = nil
		annotations[annotationPrefix+"models-"+t.Name] = nil
	}
	annotations[annotationHealthy] = nil
	annotations[annotationLastPr] = nil
	annotations[annotationPrefix+"proxy-port"] = nil

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"annotations": annotations,
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		log.Error(err, "failed to marshal cleanup patch")
		return
	}

	_, err = clientset.CoreV1().Nodes().Patch(
		ctx,
		nodeName,
		k8stypes.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		log.Error(err, "failed to clean up node annotations", "node", nodeName)
	} else {
		log.Info("cleaned up node annotations", "node", nodeName)
	}
}

// buildProxyHandler returns an http.HandlerFunc that reverse-proxies requests
// from /proxy/{provider}/... to the target's host:port/... for alive providers.
func buildProxyHandler(registry *targetRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Strip "/proxy/" prefix, then split provider name from the rest.
		trimmed := strings.TrimPrefix(r.URL.Path, "/proxy/")
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) == 0 || parts[0] == "" {
			http.Error(w, "missing provider name in path", http.StatusBadRequest)
			return
		}

		providerName := parts[0]
		target, ok := registry.get(providerName)
		if !ok {
			http.Error(w, fmt.Sprintf("provider %q not found or not alive", providerName), http.StatusBadGateway)
			return
		}

		host := target.Host
		if host == "" {
			host = defaultHost()
		}
		upstream, err := url.Parse(fmt.Sprintf("http://%s:%d", host, target.Port))
		if err != nil {
			http.Error(w, "invalid upstream URL", http.StatusInternalServerError)
			return
		}

		proxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = upstream.Scheme
				req.URL.Host = upstream.Host
				// Forward only the path after /proxy/{provider}.
				remainingPath := "/"
				if len(parts) > 1 {
					remainingPath = "/" + parts[1]
				}
				req.URL.Path = remainingPath
				req.URL.RawQuery = r.URL.RawQuery
				req.Host = upstream.Host
			},
		}
		proxy.ServeHTTP(w, r)
	}
}

func serveHealthAndProxy(registry *targetRegistry) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/proxy/", buildProxyHandler(registry))

	addr := fmt.Sprintf(":%d", defaultHealthPort)
	log.Info("starting health + proxy server", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Error(err, "health server failed")
	}
}
