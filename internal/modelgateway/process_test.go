package modelgateway

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestLiveKubernetesGatewayProcessTenantCredentialIsolation(t *testing.T) {
	if os.Getenv("CELLN_GATEWAY_PROCESS") != "1" {
		t.Skip("explicit gateway process tier required")
	}
	if os.Getenv("CELLN_GATEWAY_LIVE_KUBERNETES") != "1" || os.Getenv("CELLN_MODEL_BUDGET_DATABASE_URL") == "" || os.Getenv("CELLN_GATEWAY_TEST_BINARY") == "" {
		t.Fatal("process tier requires explicit binary, live Kubernetes and disposable PostgreSQL")
	}
	testGatewayTenantCredentialIsolation(t, true, true)
}

// Replace the in-process listener with the actual separately built binary, using
// its TLS/file/JWKS/startup/readiness path. Never inherit ambient provider keys.
func startGatewayProcess(t *testing.T, server, provider *httptest.Server, pub ed25519.PublicKey, db string) func() {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, raw []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cert := server.TLS.Certificates[0]
	certPath := write("tls.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}))
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := write("tls.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	caPath := write("provider-ca.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.TLS.Certificates[0].Certificate[0]}))
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "OKP", "crv": "Ed25519", "kid": "test", "use": "sig", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(pub)}}})
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{"clusterId": "cluster", "issuer": "sympozium-control-plane", "listen": server.Listener.Addr().String(), "tlsCertificateFile": certPath, "tlsKeyFile": keyPath, "verificationKeysFile": write("jwks.json", jwks), "registrationTokenFile": write("registration", []byte("issuer-transport-canary")), "databaseUrlFile": write("database", []byte(db)), "privateOrigins": []string{provider.URL}})
	if err != nil {
		t.Fatal(err)
	}
	configPath := write("gateway.json", config)
	server.Close()
	cmd := exec.Command(os.Getenv("CELLN_GATEWAY_TEST_BINARY"), "--config", configPath)
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH"), "KUBECONFIG=" + os.Getenv("KUBECONFIG"), "SSL_CERT_FILE=" + caPath, "SSL_CERT_DIR=" + dir}
	log, err := os.OpenFile(filepath.Join(dir, "process.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(done) }()
	stop := func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
			if exitErr != nil {
				t.Errorf("gateway process exited unsuccessfully: %v", exitErr)
			}
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("gateway failed graceful shutdown")
		}
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("gateway process stopped before readiness: %v", exitErr)
		default:
		}
		response, err := server.Client().Get(server.URL + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 204 {
				t.Log("actual gateway binary ready over verified TLS with PostgreSQL and live Kubernetes")
				var restart func()
				restart = func() {
					stop()
					restart = startGatewayProcess(t, server, provider, pub, db)
				}
				return func() { restart() }
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("gateway process readiness deadline exceeded")
	return nil
}
