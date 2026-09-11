// Package cellnparent implements the caller-scoped persistent parent protocol.
// An endpoint is a frozen operator-selected owner, not a load-balanced retry
// target.
//
// The transport and credential handling live in internal/celln (the single
// Celln client). This package keeps the parent-protocol surface stable for its
// callers and pins the owner-specific policy: TLS 1.3 minimum, no proxy, an
// absolute credential path.
package cellnparent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"path/filepath"
	"regexp"
	"time"

	"github.com/sympozium-ai/sympozium/internal/celln"
)

// The sentinels and validation patterns are shared with internal/celln.
var (
	ErrReconcile = celln.ErrReconcile
	ErrNotFound  = celln.ErrNotFound
	hashPattern  = regexp.MustCompile(`^blake3:[0-9a-f]{64}$`)
)

// Type aliases keep the parent protocol types in one place (internal/celln).
type (
	Status       = celln.ParentStatus
	TurnResult   = celln.ParentTurnResult
	TurnEvidence = celln.ParentTurnEvidence
)

// OwnerTimeout bounds one owner request.
const OwnerTimeout = 40 * time.Second

type Options struct {
	URL, TokenFile string
	Roots          *x509.CertPool
	// AllowInsecure permits plaintext HTTP to a non-loopback owner (the same
	// explicit acknowledgement the one-shot path uses). Cluster-local owners
	// are plaintext by default; external owners still require HTTPS.
	AllowInsecure bool
}

type Client struct {
	inner *celln.Client
}

func New(o Options) (*Client, error) {
	if !filepath.IsAbs(o.TokenFile) {
		return nil, errors.New("parent client requires an operator origin and absolute credential path")
	}
	inner, err := celln.New(celln.Config{
		BaseURL:       o.URL,
		TokenFile:     o.TokenFile,
		TLSRoots:      o.Roots,
		MinTLSVersion: tls.VersionTLS13,
		NoProxy:       true,
		Timeout:       OwnerTimeout,
		AllowInsecure: o.AllowInsecure,
	})
	if err != nil {
		return nil, errors.New("parent client requires an operator origin and absolute credential path")
	}
	return &Client{inner: inner}, nil
}

func (c *Client) Close() { c.inner.Close() }

// No operation retries automatically. Transport errors and ambiguous responses
// preserve the exact incarnation/turn; they never authorize a replacement POST.

func (c *Client) Create(ctx context.Context, profile, incarnation string) error {
	return c.inner.CreateParent(ctx, profile, incarnation)
}

func (c *Client) Status(ctx context.Context, id string) (Status, error) {
	return c.inner.ParentStatus(ctx, id)
}

func (c *Client) Submit(ctx context.Context, id, turn, message string) (TurnResult, error) {
	return c.inner.SubmitParentTurn(ctx, id, turn, message)
}

// SubmitAsync releases the controller to reconcile cancellation while the
// original owner executes. Pending is not permission to submit again.
func (c *Client) SubmitAsync(ctx context.Context, id, turn, message string) (TurnResult, error) {
	return c.inner.SubmitParentTurnAsync(ctx, id, turn, message)
}

func (c *Client) Stop(ctx context.Context, id string) error {
	return c.inner.StopParent(ctx, id)
}

func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.inner.CancelParent(ctx, id)
}

// CancelTurn signals only this immutable turn's child. Success is not proof of
// teardown or parent readiness; callers must reconcile the durable turn result.
// It never falls back to Cancel, which stops the entire parent.
func (c *Client) CancelTurn(ctx context.Context, id, turn string) error {
	return c.inner.CancelParentTurn(ctx, id, turn)
}

// Turn preserves both historical stage and live-owner observation. A committed
// answer never implies a resumable parent. The controller must additionally
// match child identity and request/budgets against its frozen admission.
func (c *Client) Turn(ctx context.Context, id, turn string) (TurnEvidence, error) {
	return c.inner.ParentTurn(ctx, id, turn)
}
