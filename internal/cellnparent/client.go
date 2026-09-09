// Package cellnparent implements the caller-scoped persistent parent protocol.
// An endpoint is a frozen operator-selected owner, not a load-balanced retry target.
package cellnparent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	ErrReconcile = errors.New("parent outcome requires reconciliation; preserve incarnation and turn ID")
	ErrNotFound  = errors.New("parent or turn not found; not permission to replay")
	hashPattern  = regexp.MustCompile(`^blake3:[0-9a-f]{64}$`)
	turnPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

type Options struct {
	URL, TokenFile string
	Roots          *x509.CertPool
}

type Client struct {
	origin, tokenFile string
	http              *http.Client
}

func New(o Options) (*Client, error) {
	u, err := url.Parse(o.URL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Path != "" || !filepath.IsAbs(o.TokenFile) {
		return nil, errors.New("parent client requires an operator origin and absolute credential path")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("parent origin requires HTTPS except literal loopback test endpoints")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: o.Roots},
		DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 35 * time.Second, IdleConnTimeout: 30 * time.Second, MaxConnsPerHost: 4, DisableCompression: true}
	return &Client{origin: u.String(), tokenFile: o.TokenFile, http: &http.Client{Transport: transport, Timeout: 40 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

// No operation retries automatically. Transport errors and ambiguous responses
// preserve the exact incarnation/turn; they never authorize a replacement POST.
func (c *Client) request(ctx context.Context, method, path string, body any, output any, respondAsync ...bool) (int, error) {
	f, err := os.Open(c.tokenFile)
	if err != nil {
		return 0, errors.New("parent credential unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	token := strings.TrimSpace(string(raw))
	if err != nil || len(raw) > 4096 || len(token) < 24 {
		return 0, errors.New("invalid parent credential")
	}
	for _, b := range []byte(token) {
		if b < 33 || b > 126 {
			return 0, errors.New("invalid parent credential")
		}
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(payload))
	if err != nil {
		return 0, errors.New("invalid parent request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if len(respondAsync) == 1 && respondAsync[0] {
		req.Header.Set("Prefer", "respond-async")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, ErrReconcile
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32769))
	if err != nil || len(data) > 32768 {
		return res.StatusCode, ErrReconcile
	}
	if res.StatusCode == 404 {
		return res.StatusCode, ErrNotFound
	}
	if res.StatusCode != 200 && res.StatusCode != 202 {
		return res.StatusCode, ErrReconcile
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil || decoder.Decode(new(any)) != io.EOF {
		return res.StatusCode, ErrReconcile
	}
	return res.StatusCode, nil
}

func parentPath(id string) (string, error) {
	if !hashPattern.MatchString(id) {
		return "", errors.New("invalid parent incarnation")
	}
	return "/v1/parents/" + id, nil
}

func (c *Client) Create(ctx context.Context, profile, incarnation string) error {
	if !hashPattern.MatchString(profile) || !hashPattern.MatchString(incarnation) {
		return errors.New("invalid frozen parent selection")
	}
	var result struct {
		Incarnation string `json:"incarnation"`
		Pending     bool   `json:"initializationPending"`
		Retry       *bool  `json:"retryAuthorized"`
	}
	status, err := c.request(ctx, "POST", "/v1/parents", map[string]string{"apiVersion": "celln.parent-create/v1", "launchProfile": profile}, &result)
	if err != nil {
		return err
	}
	if status != 202 || result.Incarnation != incarnation || !result.Pending || result.Retry == nil || *result.Retry {
		return ErrReconcile
	}
	return nil
}

type Status struct {
	Incarnation string `json:"incarnation"`
	Status      string `json:"status"`
	Live        bool   `json:"statusIsLiveOwnerObservation"`
	Retry       *bool  `json:"retryAuthorized"`
}

func (c *Client) Status(ctx context.Context, id string) (Status, error) {
	var result Status
	path, err := parentPath(id)
	if err != nil {
		return result, err
	}
	code, err := c.request(ctx, "GET", path, nil, &result)
	if err != nil {
		return result, err
	}
	valid := false
	for _, status := range []string{"Initializing", "Ready", "TurnActive", "Stopping", "Stopped", "ContextLost", "TeardownUncertain"} {
		valid = valid || result.Status == status
	}
	if code != 200 || !valid || result.Incarnation != id || result.Retry == nil || *result.Retry || (!result.Live && result.Status != "ContextLost") {
		return Status{}, ErrReconcile
	}
	return result, nil
}

type TurnResult struct {
	Kind      string `json:"kind,omitempty"`
	Version   string `json:"apiVersion,omitempty"`
	TurnID    string `json:"turnId,omitempty"`
	Succeeded *bool  `json:"succeeded,omitempty"`
	Answer    string `json:"answer,omitempty"`
	Pending   bool   `json:"pending,omitempty"`
	Retry     *bool  `json:"retryAuthorized,omitempty"`
}

func (c *Client) Submit(ctx context.Context, id, turn, message string) (TurnResult, error) {
	return c.submit(ctx, id, turn, message, false)
}

// SubmitAsync releases the controller to reconcile cancellation while the
// original owner executes. Pending is not permission to submit again.
func (c *Client) SubmitAsync(ctx context.Context, id, turn, message string) (TurnResult, error) {
	return c.submit(ctx, id, turn, message, true)
}

func (c *Client) submit(ctx context.Context, id, turn, message string, respondAsync bool) (TurnResult, error) {
	var result TurnResult
	path, err := parentPath(id)
	if err != nil {
		return result, err
	}
	if !turnPattern.MatchString(turn) || strings.TrimSpace(message) == "" || len(message) > 2048 || strings.ContainsRune(message, 0) {
		return result, errors.New("invalid bounded parent turn")
	}
	body := map[string]string{"kind": "turn", "apiVersion": "celln.parent-context/v1", "turnId": turn, "message": message}
	code, err := c.request(ctx, "POST", path+"/turns", body, &result, respondAsync)
	if err != nil {
		return result, err
	}
	if code == 202 && result.Pending && result.Retry != nil && !*result.Retry && result.Kind == "" && result.TurnID == "" && result.Succeeded == nil && result.Answer == "" {
		return result, nil
	}
	if code != 200 || result.Pending || result.Kind != "completed" || result.Version != "celln.parent-context/v1" || result.TurnID != turn || result.Succeeded == nil || len(result.Answer) > 2048 || strings.TrimSpace(result.Answer) == "" || strings.ContainsRune(result.Answer, 0) {
		return TurnResult{}, ErrReconcile
	}
	return result, nil
}

func (c *Client) Stop(ctx context.Context, id string) error {
	path, err := parentPath(id)
	if err != nil {
		return err
	}
	var result struct {
		Confirmed bool `json:"teardownConfirmed"`
	}
	code, err := c.request(ctx, "POST", path+"/stop", nil, &result)
	if err != nil {
		return err
	}
	if code != 200 || !result.Confirmed {
		return ErrReconcile
	}
	return nil
}

func (c *Client) Cancel(ctx context.Context, id string) error {
	path, err := parentPath(id)
	if err != nil {
		return err
	}
	var result struct {
		Requested bool  `json:"cancellationRequested"`
		Confirmed *bool `json:"teardownConfirmed"`
	}
	code, err := c.request(ctx, "POST", path+"/cancel", nil, &result)
	if err != nil {
		return err
	}
	if code != 202 || !result.Requested || result.Confirmed == nil || *result.Confirmed {
		return ErrReconcile
	}
	return nil
}

// CancelTurn signals only this immutable turn's child. Success is not proof of
// teardown or parent readiness; callers must reconcile the durable turn result.
// It never falls back to Cancel, which stops the entire parent.
func (c *Client) CancelTurn(ctx context.Context, id, turn string) error {
	path, err := parentPath(id)
	if err != nil {
		return err
	}
	if !turnPattern.MatchString(turn) {
		return fmt.Errorf("invalid turn ID")
	}
	var result struct {
		Requested bool  `json:"cancellationRequested"`
		Confirmed *bool `json:"teardownConfirmed"`
		Retry     *bool `json:"retryAuthorized"`
	}
	code, err := c.request(ctx, "POST", path+"/turns/"+turn+"/cancel", nil, &result)
	if err != nil {
		return err
	}
	if code != 202 || !result.Requested || result.Confirmed == nil || *result.Confirmed || result.Retry == nil || *result.Retry {
		return ErrReconcile
	}
	return nil
}

type TurnEvidence struct {
	Turn struct {
		Stage  string          `json:"stage"`
		Record json.RawMessage `json:"record"`
	} `json:"turn"`
	Retry       *bool  `json:"retryAuthorized"`
	OwnerStatus string `json:"ownerStatus"`
}

// Turn preserves both historical stage and live-owner observation. A committed
// answer never implies a resumable parent. The controller must additionally
// match child identity and request/budgets against its frozen admission.
func (c *Client) Turn(ctx context.Context, id, turn string) (TurnEvidence, error) {
	var result TurnEvidence
	path, err := parentPath(id)
	if err != nil {
		return result, err
	}
	if !turnPattern.MatchString(turn) {
		return result, fmt.Errorf("invalid turn ID")
	}
	code, err := c.request(ctx, "GET", path+"/turns/"+turn, nil, &result)
	if err != nil {
		return result, err
	}
	if code != 200 || result.Retry == nil || *result.Retry {
		return TurnEvidence{}, ErrReconcile
	}
	switch result.OwnerStatus {
	case "Initializing", "Ready", "TurnActive", "Stopping", "Stopped", "ContextLost", "TeardownUncertain":
	default:
		return TurnEvidence{}, ErrReconcile
	}
	var record struct {
		Version int    `json:"version"`
		Parent  string `json:"parent"`
		Child   string `json:"child"`
		TurnID  string `json:"turnId"`
		Request struct {
			TurnID string `json:"turnId"`
		} `json:"request"`
	}
	if json.Unmarshal(result.Turn.Record, &record) != nil || record.Version != 1 || record.Parent != id || !hashPattern.MatchString(record.Child) {
		return TurnEvidence{}, ErrReconcile
	}
	switch result.Turn.Stage {
	case "reserved":
		if record.Request.TurnID != turn {
			return TurnEvidence{}, ErrReconcile
		}
	case "child-destroyed", "parent-committed":
		if record.TurnID != turn {
			return TurnEvidence{}, ErrReconcile
		}
	default:
		return TurnEvidence{}, ErrReconcile
	}
	return result, nil
}
