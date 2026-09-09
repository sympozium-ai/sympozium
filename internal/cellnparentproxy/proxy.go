// Package cellnparentproxy exposes only the parent protocol through TLS.
// The fixed loopback dispatcher remains the bearer/principal authority.
package cellnparentproxy

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var hash = regexp.MustCompile(`^blake3:[0-9a-f]{64}$`)
var turn = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func New(backend string) (http.Handler, func(), error) {
	u, err := url.Parse(backend)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, nil, fmt.Errorf("fixed literal loopback HTTP dispatcher origin required")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 90 * time.Second, IdleConnTimeout: 30 * time.Second, MaxConnsPerHost: 32, DisableCompression: true}
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(u)
		p.Out.Host = u.Host
		// Never turn a POST into a transport-retryable request based on caller headers.
		p.Out.Header.Del("Idempotency-Key")
		p.Out.Header.Del("X-Idempotency-Key")
		p.Out.Header.Del("Cookie")
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "parent owner unavailable; reconcile original identity", http.StatusBadGateway)
	}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Upgrade") != "" || !route(r.Method, r.URL.Path) {
			http.Error(w, "parent protocol route required", http.StatusNotFound)
			return
		}
		if r.ContentLength > 65536 {
			http.Error(w, "parent request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid or oversized parent request", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		proxy.ServeHTTP(w, r)
	})
	return handler, transport.CloseIdleConnections, nil
}

func route(method, path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) < 4 || parts[0] != "" || parts[1] != "v1" || parts[2] != "parents" {
		return path == "/v1/parents" && method == http.MethodPost
	}
	if !hash.MatchString(parts[3]) {
		return false
	}
	if len(parts) == 4 {
		return method == http.MethodGet
	}
	if len(parts) == 5 {
		return method == http.MethodPost && (parts[4] == "turns" || parts[4] == "stop" || parts[4] == "cancel")
	}
	if len(parts) == 7 {
		return method == http.MethodPost && parts[4] == "turns" && turn.MatchString(parts[5]) && parts[6] == "cancel"
	}
	return len(parts) == 6 && method == http.MethodGet && parts[4] == "turns" && turn.MatchString(parts[5])
}
