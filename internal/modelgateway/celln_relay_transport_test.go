package modelgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type relayProbeResult struct {
	OK     bool            `json:"ok"`
	Budget bool            `json:"budget"`
	Body   json.RawMessage `json:"body"`
}

func runRelayProbe(t *testing.T, ctx context.Context, binary string, input map[string]any) []relayProbeResult {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	command.Stdin = bytes.NewReader(data)
	out, err := command.Output()
	if err != nil {
		t.Fatal("Rust host relay probe failed (diagnostics withheld)")
	}
	var results []relayProbeResult
	if err := json.Unmarshal(out, &results); err != nil {
		t.Fatal("invalid relay probe output")
	}
	return results
}

func exerciseRelayTransportRefusals(t *testing.T, binary string, original map[string]any) {
	for _, mode := range []string{"untrusted-ca", "redirect", "credential-echo", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			input := make(map[string]any, len(original))
			for key, value := range original {
				input[key] = value
			}
			input["requests"] = []json.RawMessage{json.RawMessage(`{"model":"m","stream":false,"max_tokens":512,"messages":[{"role":"user","content":"hello"}]}`)}
			var targetCalls atomic.Int64
			target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); w.WriteHeader(200) }))
			defer target.Close()
			cancelled := make(chan bool, 1)
			var calls atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				switch mode {
				case "redirect":
					w.Header().Set("Location", target.URL+"/v1/invoke")
					w.WriteHeader(302)
				case "credential-echo":
					bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
					if !ok {
						t.Error("missing scoped bearer")
						w.WriteHeader(401)
						return
					}
					token, _ := json.Marshal(bearer)
					// A map-normalizing scanner would discard the credential-bearing first key.
					escaped := strings.Replace(string(token), "e", `\u0065`, 1)
					_, _ = io.WriteString(w, `{"x":`+escaped+`,"x":"safe"}`)
				case "cancellation":
					select {
					case <-r.Context().Done():
						cancelled <- true
					case <-time.After(5 * time.Second):
						cancelled <- false
					}
				default:
					_, _ = io.WriteString(w, `{"unexpected":true}`)
				}
			}))
			defer server.Close()
			ca := server.TLS.Certificates[0].Certificate[0]
			if mode == "untrusted-ca" {
				pub, priv, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				cert := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
				ca, err = x509.CreateCertificate(rand.Reader, cert, cert, pub, priv)
				if err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca}), 0600); err != nil {
				t.Fatal(err)
			}
			input["gatewayOrigin"] = server.URL
			input["gatewayCa"] = path
			if mode == "cancellation" {
				input["timeoutMs"] = 500
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			results := runRelayProbe(t, ctx, binary, input)
			if len(results) != 1 || results[0].OK {
				t.Fatal("unsafe transport response accepted")
			}
			if targetCalls.Load() != 0 {
				t.Fatal("redirect followed")
			}
			if mode == "untrusted-ca" && calls.Load() != 0 {
				t.Fatal("credential sent without trusted TLS")
			}
			if mode != "untrusted-ca" && calls.Load() != 1 {
				t.Fatal("missing request or automatic retry")
			}
			if mode == "cancellation" {
				select {
				case stopped := <-cancelled:
					if !stopped {
						t.Fatal("TLS invocation did not cancel")
					}
				case <-ctx.Done():
					t.Fatal("cancellation not observed by server")
				}
			}
		})
	}
}
