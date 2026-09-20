package modelgateway

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPublicProviderDoesNotTrustFixtureCA(t *testing.T) {
	c, err := clientForEndpoint("https://api.example.com/chat/completions", false, time.Second, x509.NewCertPool())
	if err != nil {
		t.Fatal(err)
	}
	transport := c.Transport.(*http.Transport)
	if transport.TLSClientConfig.RootCAs != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.Proxy != nil {
		t.Fatal("public provider trust or transport was widened")
	}
}

type fixedResolver []net.IP

func (f fixedResolver) LookupIP(context.Context, string, string) ([]net.IP, error) { return f, nil }
func TestConnectTimeAddressValidation(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		ips                   []net.IP
		allow, loopback, deny bool
	}{
		{"public", []net.IP{net.ParseIP("8.8.8.8")}, false, false, false},
		{"mixed", []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("169.254.169.254")}, false, false, true},
		{"ipv6-private", []net.IP{net.ParseIP("fd00::1")}, false, false, true},
		{"mapped-private", []net.IP{net.ParseIP("::ffff:10.0.0.1")}, false, false, true},
		{"approved-private", []net.IP{net.ParseIP("10.0.0.1")}, true, false, false},
		{"plaintext-private", []net.IP{net.ParseIP("10.0.0.1")}, true, true, false},
		{"plaintext-ipv6-private", []net.IP{net.ParseIP("fd00::1")}, true, true, false},
		{"plaintext-public", []net.IP{net.ParseIP("8.8.8.8")}, true, true, true},
		{"plaintext-metadata", []net.IP{net.ParseIP("169.254.169.254")}, true, true, true},
		{"plaintext-mixed", []net.IP{net.ParseIP("10.0.0.1"), net.ParseIP("8.8.8.8")}, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			d := restrictedDialer{resolver: fixedResolver(tc.ips), allowPrivate: tc.allow, privateOnly: tc.loopback, dial: func(_ context.Context, _, address string) (net.Conn, error) {
				called = true
				host, _, _ := net.SplitHostPort(address)
				if host != tc.ips[0].String() {
					t.Fatal("dial did not pin validated IP")
				}
				return nil, nil
			}}
			_, err := d.DialContext(context.Background(), "tcp", "provider.example:443")
			if (err != nil) != tc.deny || called == tc.deny {
				t.Fatalf("err=%v called=%v", err, called)
			}
		})
	}
}
func TestRealSocketPrivateDestinationAndRedirect(t *testing.T) {
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer provider.Close()
	denied, err := clientForEndpoint(provider.URL, false, time.Second, nil)
	if err == nil {
		_, err = denied.Get(provider.URL)
	}
	if err == nil || calls != 0 {
		t.Fatal("unapproved private destination connected")
	}
	approved, err := clientForEndpoint(provider.URL, true, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := approved.Get(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if calls != 1 {
		t.Fatal("approved loopback not reached")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, provider.URL, 302) }))
	defer redirect.Close()
	_, err = approved.Get(redirect.URL)
	if err == nil || calls != 1 {
		t.Fatal("redirect followed")
	}
}

func TestProviderRootCAsKeepTLSVerificationEnabled(t *testing.T) {
	roots := x509.NewCertPool()
	client, err := clientForEndpoint("https://127.0.0.1:9443/v1", true, time.Second, roots)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig := client.Transport.(*http.Transport).TLSClientConfig
	if tlsConfig.RootCAs != roots || tlsConfig.InsecureSkipVerify || tlsConfig.ServerName != "127.0.0.1" {
		t.Fatalf("explicit roots weakened or lost TLS verification: %#v", tlsConfig)
	}
}
func TestProviderRequestRefusals(t *testing.T) {
	for _, raw := range []string{
		`{"model":"m","messages":[],"max_tokens":1,"stream":true}`,
		`{"model":"m","messages":[],"max_tokens":1,"max_tokens":2}`,
		`{"model":"m","messages":[],"max_tokens":1,"n":2}`,
		`{"model":"m","messages":[],"max_tokens":1,"endpoint":"https://evil.example"}`,
		`{"model":"m","messages":[],"max_tokens":1,"max_completion_tokens":1}`,
	} {
		if _, _, _, _, err := validateProviderRequest("openai-chat", "m", []byte(raw), 512, requestPolicy{}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, protocol := range []string{"openai-chat", "anthropic-messages"} {
		if _, _, _, _, err := validateProviderRequest(protocol, "m", []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`), 512, requestPolicy{}); err != nil {
			t.Fatal(err)
		}
	}
}
