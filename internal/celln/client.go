// Package celln is the control-plane client for Celln. It owns the base-URL,
// credential and transport policy shared by the one-shot execution protocol
// (`/v1/executions*`) and the enduring parent protocol (`/v1/parents*`), so both
// lifecycles are reached through a single Celln service.
//
// See docs/design/celln-single-execution-plane.md.
package celln

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultTimeout bounds a single request unless a Config overrides it.
const DefaultTimeout = 30 * time.Second

// Config is the operator-supplied connection to the Celln service.
type Config struct {
	// BaseURL is the Celln origin, e.g.
	// http://celln-router.celln-system.svc.cluster.local:8787. It must not carry
	// userinfo, a path, query or fragment.
	BaseURL string
	// TokenFile is the path to a bearer credential. It is re-read on every
	// request so a mounted Secret rotation takes effect.
	TokenFile string
	// AllowInsecure permits plaintext HTTP to a non-loopback origin. Loopback is
	// always permitted; HTTPS is always permitted.
	AllowInsecure bool
	// Timeout bounds a single request. Defaults to DefaultTimeout.
	Timeout time.Duration
	// TLSRoots adds trust anchors (e.g. an operator owner CA).
	TLSRoots *x509.CertPool
	// MinTLSVersion raises the floor (e.g. tls.VersionTLS13 for owner origins).
	MinTLSVersion uint16
	// NoProxy disables environment proxy use for a frozen owner origin.
	NoProxy bool
}

// Client is a credentialed client for one Celln origin.
type Client struct {
	base      *url.URL
	tokenFile string
	http      *http.Client
}

// DefaultHTTPClient is a no-redirect client with the default timeout, for
// callers that build a request with Client.Request and send it separately
// (e.g. to freeze state between build and send).
var DefaultHTTPClient = &http.Client{
	Timeout: DefaultTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func (cfg Config) transport() (http.RoundTripper, error) {
	if cfg.TLSRoots == nil && cfg.MinTLSVersion == 0 && !cfg.NoProxy {
		return nil, nil
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("Celln: default transport is not *http.Transport")
	}
	t := base.Clone()
	if cfg.NoProxy {
		t.Proxy = nil
	}
	if cfg.MinTLSVersion != 0 || cfg.TLSRoots != nil {
		if t.TLSClientConfig == nil {
			t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if cfg.MinTLSVersion != 0 {
			t.TLSClientConfig.MinVersion = cfg.MinTLSVersion
		}
		if cfg.TLSRoots != nil {
			t.TLSClientConfig.RootCAs = cfg.TLSRoots
		}
	}
	return t, nil
}

// New validates cfg and returns a client. It does not read the credential;
// Request does, per call.
func New(cfg Config) (*Client, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || base.Opaque != "" ||
		base.Path != "" || base.RawPath != "" || base.RawQuery != "" ||
		base.ForceQuery || base.Fragment != "" {
		return nil, fmt.Errorf("Celln: invalid base URL")
	}
	loopback := base.Hostname() == "localhost"
	if ip := net.ParseIP(base.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && (loopback || cfg.AllowInsecure)) {
		return nil, fmt.Errorf("Celln: requires HTTPS; plaintext non-loopback requires explicit opt-in")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	transport, err := cfg.transport()
	if err != nil {
		return nil, err
	}
	return &Client{
		base:      base,
		tokenFile: cfg.TokenFile,
		http: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			// Never follow a redirect with a credential or reinterpret a
			// redirected POST as GET. A moved service needs an explicit
			// operator configuration update.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// BaseURL returns the configured origin.
func (c *Client) BaseURL() string { return c.base.String() }

// Close releases idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// credential reads and validates the bearer token for a single call.
func (c *Client) credential() (string, error) {
	if c.tokenFile == "" {
		return "", fmt.Errorf("Celln: no operator-provisioned credential configured")
	}
	f, err := os.Open(c.tokenFile)
	if err != nil {
		return "", fmt.Errorf("Celln: cannot read credential")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	token := strings.TrimSpace(string(data))
	if err != nil || len(data) > 4096 || len(token) < 24 || strings.ContainsAny(token, "\r\n\t ") {
		return "", fmt.Errorf("Celln: invalid credential")
	}
	return token, nil
}

// Request builds a credentialed JSON request against the configured origin.
func (c *Client) Request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	token, err := c.credential()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.base.String(), "/")+path, body)
	if err != nil {
		return nil, fmt.Errorf("Celln: cannot build request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// Do sends an already-built request.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.http.Do(req)
}
