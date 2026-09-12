package modelgateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
		{"plaintext-private", []net.IP{net.ParseIP("10.0.0.1")}, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			d := restrictedDialer{resolver: fixedResolver(tc.ips), allowPrivate: tc.allow, loopbackOnly: tc.loopback, dial: func(_ context.Context, _, address string) (net.Conn, error) {
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
	denied, err := clientForEndpoint(provider.URL, false, time.Second)
	if err == nil {
		_, err = denied.Get(provider.URL)
	}
	if err == nil || calls != 0 {
		t.Fatal("unapproved private destination connected")
	}
	approved, err := clientForEndpoint(provider.URL, true, time.Second)
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
func TestProviderRequestRefusals(t *testing.T) {
	for _, raw := range []string{
		`{"model":"m","messages":[],"max_tokens":1,"stream":true}`,
		`{"model":"m","messages":[],"max_tokens":1,"max_tokens":2}`,
		`{"model":"m","messages":[],"max_tokens":1,"n":2}`,
		`{"model":"m","messages":[],"max_tokens":1,"endpoint":"https://evil.example"}`,
		`{"model":"m","messages":[],"max_tokens":1,"max_completion_tokens":1}`,
	} {
		if _, _, _, err := validateProviderRequest("openai-chat", "m", []byte(raw), 512); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, protocol := range []string{"openai-chat", "anthropic-messages"} {
		if _, _, _, err := validateProviderRequest(protocol, "m", []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":1}`), 512); err != nil {
			t.Fatal(err)
		}
	}
}
