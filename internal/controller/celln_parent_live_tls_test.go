package controller_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnparentproxy"
)

// Exercise the production parent-edge handler with a test-only certificate.
// Its public trust anchor is explicitly supplied to the actual parent client.
func liveParentTLS(t *testing.T, origin string) (string, string) {
	t.Helper()
	if os.Getenv("CELLN_INTEROP_OWNER_TLS") != "true" {
		return origin, ""
	}
	handler, closeTransport, err := cellnparentproxy.New(origin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeTransport)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "owner-ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if binary := os.Getenv("CELLN_INTEROP_PROXY_BINARY"); binary != "" {
		return liveParentTLSProcess(t, binary, origin, path, server), path
	}
	t.Log("parent owner exposed through TLS 1.3 edge with explicit certificate trust")
	return server.URL, path
}

func liveParentTLSProcess(t *testing.T, binary, backend, certificate string, fixture *httptest.Server) string {
	t.Helper()
	if !filepath.IsAbs(binary) {
		t.Fatal("absolute parent proxy binary required")
	}
	key, err := x509.MarshalPKCS8PrivateKey(fixture.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(filepath.Dir(certificate), "owner-key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	url := fixture.URL
	address := strings.TrimPrefix(url, "https://")
	root := fixture.Certificate()
	fixture.Close()
	log, err := os.OpenFile(filepath.Join(filepath.Dir(certificate), "proxy.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--listen", address, "--backend", backend, "--tls-cert", certificate, "--tls-key", keyPath)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait(); log.Close(); close(done) }()
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("proxy exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Error("proxy did not stop gracefully")
		}
	})
	roots := x509.NewCertPool()
	roots.AddCert(root)
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := client.Get(url + "/not-a-parent-route")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusNotFound || response.TLS.Version != tls.VersionTLS13 {
				t.Fatal("unexpected proxy TLS/route policy")
			}
			break
		}
		select {
		case err := <-done:
			t.Fatalf("proxy stopped before ready: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy never became ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	legacy := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12, RootCAs: roots}}, Timeout: time.Second}
	defer legacy.CloseIdleConnections()
	if response, err := legacy.Get(url + "/not-a-parent-route"); err == nil {
		response.Body.Close()
		t.Fatal("proxy executable accepted TLS 1.2")
	}
	t.Logf("separate parent TLS proxy process started (PID %d); TLS 1.2 refused", command.Process.Pid)
	return url
}
