// Package main is the entry point for the web-proxy that exposes
// Sympozium agents as OpenAI-compatible and MCP HTTP endpoints.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/webproxy"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sympoziumv1alpha1.AddToScheme(scheme))
}

func main() {
	var instanceName string
	var eventBusURL string
	var apiKey string
	var addr string
	var rpm int
	var burst int

	flag.StringVar(&instanceName, "instance", envStr("INSTANCE_NAME", envStr("SKILL_INSTANCE_NAME", "")), "SympoziumInstance name")
	flag.StringVar(&eventBusURL, "event-bus-url", os.Getenv("EVENT_BUS_URL"), "Event bus (NATS) URL")
	flag.StringVar(&apiKey, "api-key", os.Getenv("WEB_PROXY_API_KEY"), "API key for authentication")
	flag.StringVar(&addr, "addr", ":8080", "HTTP listen address")
	flag.IntVar(&rpm, "rate-limit-rpm", envInt("RATE_LIMIT_RPM", envInt("SKILL_RATE_LIMIT_RPM", 60)), "Requests per minute rate limit")
	flag.IntVar(&burst, "rate-limit-burst", envInt("RATE_LIMIT_BURST", envInt("SKILL_RATE_LIMIT_BURST", 10)), "Burst size for rate limiter")
	flag.Parse()

	if instanceName == "" {
		fmt.Fprintln(os.Stderr, "INSTANCE_NAME is required")
		os.Exit(1)
	}
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "WEB_PROXY_API_KEY is required")
		os.Exit(1)
	}

	log := zap.New(zap.UseDevMode(false)).WithName("web-proxy")

	// Connect to NATS event bus
	bus, err := eventbus.NewNATSEventBus(eventBusURL)
	if err != nil {
		log.Error(err, "failed to connect to event bus")
		os.Exit(1)
	}
	defer bus.Close()

	// Create in-cluster K8s client
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "failed to get kubeconfig")
		os.Exit(1)
	}
	k8sClient, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "failed to create K8s client")
		os.Exit(1)
	}

	// Create proxy
	proxy := webproxy.NewProxy(webproxy.Config{
		InstanceName: instanceName,
		APIKey:       apiKey,
		RPM:          rpm,
		BurstSize:    burst,
	}, bus, k8sClient, log)

	// Start HTTP server
	server := &http.Server{
		Addr:              addr,
		Handler:           proxy.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Info("Shutting down web-proxy")
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 15*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Info("Starting web-proxy", "addr", addr, "instance", instanceName)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error(err, "HTTP server error")
		os.Exit(1)
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
