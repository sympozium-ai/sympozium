// Command harness-gateway swaps a harness run's token for its model key, so the
// key never enters the run pod.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "harness-gateway: %v\n", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func run() error {
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		return errors.New("POD_NAMESPACE is required")
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := api.AddToScheme(scheme); err != nil {
		return err
	}
	config, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	// Uncached: the gateway's RBAC is get, and every request must see the run's
	// current phase.
	reader, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	maxConcurrent, err := strconv.ParseInt(env("HARNESS_GATEWAY_MAX_CONCURRENT", "32"), 10, 64)
	if err != nil {
		return fmt.Errorf("HARNESS_GATEWAY_MAX_CONCURRENT: %w", err)
	}
	maxConcurrentPerRun, err := strconv.ParseInt(env("HARNESS_GATEWAY_MAX_CONCURRENT_PER_RUN", "16"), 10, 64)
	if err != nil {
		return fmt.Errorf("HARNESS_GATEWAY_MAX_CONCURRENT_PER_RUN: %w", err)
	}
	gateway := &harnessgateway.Gateway{
		Reader:              reader,
		MaxConcurrent:       maxConcurrent,
		MaxConcurrentPerRun: maxConcurrentPerRun,
		Profiles: &harnessgateway.Loader{
			Reader:    reader,
			Name:      env("SYMPOZIUM_HARNESS_GATEWAY_PROFILES", "sympozium-harness-gateway-profiles"),
			Namespace: namespace,
			OnRejected: func(rejected string) {
				fmt.Fprintf(os.Stderr, "harness-gateway: skipped %s\n", rejected)
			},
		},
	}
	tlsDir := env("TLS_DIR", "/etc/harness-gateway/tls")
	certificates := &harnessgateway.CertificateReloader{CertFile: tlsDir + "/tls.crt", KeyFile: tlsDir + "/tls.key"}
	if _, err := certificates.GetCertificate(nil); err != nil {
		return err
	}
	server := &http.Server{
		Addr:              env("LISTEN", ":8443"),
		Handler:           gateway.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		// No ReadTimeout or WriteTimeout: a model call lasts as long as the
		// harness's own timeouts allow.
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certificates.GetCertificate},
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
