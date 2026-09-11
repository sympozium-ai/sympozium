package celln

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodToken = "0123456789abcdefghijklmn" // 24 printable bytes

func writeToken(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewRejectsNonOriginBaseURL(t *testing.T) {
	for _, base := range []string{
		"",
		"https://user@celln.example",
		"https://celln.example/v1",
		"https://celln.example?x=1",
		"https://celln.example#frag",
	} {
		if _, err := New(Config{BaseURL: base, TokenFile: "/tmp/t"}); err == nil {
			t.Errorf("New(%q) accepted a non-origin base URL", base)
		}
	}
}

func TestNewRequiresHTTPSExceptLoopbackOrOptIn(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://celln.example:8787", TokenFile: "/tmp/t"}); err == nil {
		t.Error("plaintext non-loopback accepted without opt-in")
	}
	if _, err := New(Config{BaseURL: "http://celln.example:8787", TokenFile: "/tmp/t", AllowInsecure: true}); err != nil {
		t.Errorf("plaintext accepted with opt-in failed: %v", err)
	}
	if _, err := New(Config{BaseURL: "https://celln.example:8787", TokenFile: "/tmp/t"}); err != nil {
		t.Errorf("https rejected: %v", err)
	}
	if _, err := New(Config{BaseURL: "http://127.0.0.1:8787", TokenFile: "/tmp/t"}); err != nil {
		t.Errorf("loopback http rejected: %v", err)
	}
}

func TestRequestCarriesCredentialAndRejectsBadTokens(t *testing.T) {
	good := writeToken(t, goodToken+"\n")
	c, err := New(Config{BaseURL: "http://127.0.0.1:8787", TokenFile: good})
	if err != nil {
		t.Fatal(err)
	}
	req, err := c.Request(context.Background(), http.MethodGet, "/v1/executions/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+goodToken {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer "+goodToken)
	}

	for name, token := range map[string]string{
		"empty":       "",
		"too-short":   "short",
		"has-space":   "0123456789 abcdefghijklmn",
		"has-newline": "0123456789abcdefghijklm\nn",
	} {
		c, err := New(Config{BaseURL: "http://127.0.0.1:8787", TokenFile: writeToken(t, token)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Request(context.Background(), http.MethodGet, "/v1/executions/x", nil); err == nil {
			t.Errorf("%s token accepted", name)
		}
	}

	missing, err := New(Config{BaseURL: "http://127.0.0.1:8787", TokenFile: filepath.Join(t.TempDir(), "absent")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Request(context.Background(), http.MethodGet, "/v1/executions/x", nil); err == nil {
		t.Error("missing credential accepted")
	}

	noCred, err := New(Config{BaseURL: "http://127.0.0.1:8787"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noCred.Request(context.Background(), http.MethodGet, "/v1/executions/x", nil); err == nil {
		t.Error("unconfigured credential accepted")
	}
}

func TestClientRefusesRedirects(t *testing.T) {
	var redirected bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		redirected = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, TokenFile: writeToken(t, goodToken)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(mustRequest(t, c, srv.URL+"/moved"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302 (redirect must not be followed)", resp.StatusCode)
	}
	if redirected {
		t.Fatal("client followed a redirect")
	}
}

func mustRequest(t *testing.T, c *Client, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, c.BaseURL()) {
		t.Fatalf("test URL %q not under base %q", url, c.BaseURL())
	}
	return req
}
