package modelgateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ipResolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
}

type netResolver struct{}

func (netResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, network, host)
}

// restrictedDialer validates every DNS answer immediately before connecting.
// Mixed public/private answers fail closed, preventing fallback to a forbidden
// address after an apparently safe URL-string check.
type restrictedDialer struct {
	resolver     ipResolver
	allowPrivate bool
	loopbackOnly bool
	dial         func(context.Context, string, string) (net.Conn, error)
}

func (d restrictedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fail(ReasonDestination, 403, err)
	}
	ips, err := d.resolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fail(ReasonProviderUnavailable, 502, err)
	}
	for _, ip := range ips {
		if (d.loopbackOnly && !ip.IsLoopback()) || (forbiddenIP(ip) && !d.allowPrivate) {
			return nil, fail(ReasonDestination, 403, nil)
		}
	}
	// Pin this connection to the already-validated first answer. The transport
	// cannot perform a second DNS lookup and silently select another address.
	return d.dial(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

func clientForEndpoint(endpoint string, allowPrivate bool, maxDuration time.Duration) (*http.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		return nil, fail(ReasonDestination, 403, err)
	}
	if u.Scheme != "https" && !allowPrivate {
		return nil, fail(ReasonDestination, 403, nil)
	}
	base := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}
	dialer := restrictedDialer{resolver: netResolver{}, allowPrivate: allowPrivate, loopbackOnly: u.Scheme == "http", dial: base.DialContext}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()},
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: maxDuration,
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       maxDuration,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("provider redirects are disabled") },
	}, nil
}

func privateOriginAllowed(endpoint string, allowed map[string]bool) bool {
	origin, err := exactOrigin(endpoint)
	if err != nil {
		return false
	}
	for configured, permit := range allowed {
		if permit && strings.EqualFold(strings.TrimSuffix(configured, "/"), origin) {
			return true
		}
	}
	return false
}
