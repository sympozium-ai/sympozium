// A dedicated TLS edge for a loopback-only Celln parent owner.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"github.com/sympozium-ai/sympozium/internal/cellnparentproxy"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9443", "TLS parent protocol listener")
	backend := flag.String("backend", "", "Fixed loopback HTTP dispatcher origin")
	cert := flag.String("tls-cert", "", "Absolute operator certificate-chain file")
	key := flag.String("tls-key", "", "Absolute operator private-key file")
	executionRouter := flag.Bool("execution-router", false, "Expose only one-shot execution routes instead of parent routes")
	flag.Parse()
	if !filepath.IsAbs(*cert) || !filepath.IsAbs(*key) {
		fmt.Fprintln(os.Stderr, "absolute TLS certificate and key required")
		os.Exit(1)
	}
	constructor := cellnparentproxy.New
	if *executionRouter {
		constructor = cellnparentproxy.NewExecution
	}
	handler, closeTransport, err := constructor(*backend)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer closeTransport()
	pair, err := tls.LoadX509KeyPair(*cert, *key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "operator TLS certificate/key unavailable")
		os.Exit(1)
	}
	server := &http.Server{Addr: *listen, Handler: handler, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServeTLS("", "") }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "parent TLS listener failed")
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
		<-done
	}
}
