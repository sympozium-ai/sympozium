package controller_test

import (
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

type liveAPIEndpoint struct {
	URL       string
	TokenFile string
	client    *http.Client
	stop      func()
}

func (s *liveAPIEndpoint) Client() *http.Client { return s.client }
func (s *liveAPIEndpoint) Close()               { s.stop() }

// External API mode still uses the operator/setup identity. It is not an
// API-ServiceAccount RBAC proof; its listener is explicitly loopback-only.
func liveParentAPI(t *testing.T, handler http.Handler, config *rest.Config, namespace, token, previewPath string) *liveAPIEndpoint {
	t.Helper()
	binary := os.Getenv("CELLN_INTEROP_API_BINARY")
	if binary == "" {
		server := httptest.NewServer(handler)
		return &liveAPIEndpoint{URL: server.URL, client: server.Client(), stop: server.Close}
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("absolute API binary required")
	}
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "api-token")
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	kubeconfig := clientcmdapi.Config{Clusters: map[string]*clientcmdapi.Cluster{"proof": {Server: config.Host, CertificateAuthority: config.CAFile, CertificateAuthorityData: config.CAData, TLSServerName: config.ServerName}}, AuthInfos: map[string]*clientcmdapi.AuthInfo{"operator": {ClientCertificate: config.CertFile, ClientCertificateData: config.CertData, ClientKey: config.KeyFile, ClientKeyData: config.KeyData, Token: config.BearerToken, TokenFile: config.BearerTokenFile}}, Contexts: map[string]*clientcmdapi.Context{"proof": {Cluster: "proof", AuthInfo: "operator", Namespace: namespace}}, CurrentContext: "proof"}
	raw, err := clientcmd.Write(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "kubeconfig")
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	log, err := os.OpenFile(filepath.Join(directory, "api.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--addr", address, "--namespace", namespace, "--token-file", tokenPath, "--event-bus-url", os.Getenv("CELLN_INTEROP_EVENT_BUS_URL"), "--serve-ui=true")
	command.Env = []string{"PATH=/usr/bin:/bin", "KUBECONFIG=" + configPath, "NATS_USERNAME=" + os.Getenv("NATS_USERNAME"), "NATS_PASSWORD=" + os.Getenv("NATS_PASSWORD"), "CELLN_PERMISSION_PREVIEW_CONFIG=" + previewPath}
	command.Stdout, command.Stderr = log, log
	if err := command.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { err := command.Wait(); log.Close(); done <- err; close(done) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = command.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("API process exit: %v", err)
				}
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				<-done
				t.Error("API did not stop gracefully")
			}
		})
	}
	t.Cleanup(stop)
	endpoint := &liveAPIEndpoint{URL: "http://" + address, TokenFile: tokenPath, client: &http.Client{Timeout: 10 * time.Second}, stop: stop}
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := endpoint.client.Get(endpoint.URL + "/api/v1/runs?namespace=" + namespace)
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatal("API was not authenticated")
			}
			break
		}
		select {
		case err := <-done:
			t.Fatalf("API stopped before ready: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("API never became ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("separate authenticated API/web process started (PID %d); operator Kubernetes identity", command.Process.Pid)
	return endpoint
}
