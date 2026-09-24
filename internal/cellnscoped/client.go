package cellnscoped

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelgateway"
)

const maxResponseBytes = 4 << 20

var publicReasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

type HTTPError struct {
	Status int
	Reason string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("scoped host request failed (HTTP %d, reason %s)", e.Status, e.Reason)
}

func IsNotFound(err error) bool {
	var target *HTTPError
	return errors.As(err, &target) && target.Status == http.StatusNotFound
}

type hostClient struct {
	origin string
	token  string
	http   *http.Client
}

func newHostClient(cfg EndpointConfig) (*hostClient, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || cfg.CAFile == "" || cfg.TokenFile == "" {
		return nil, errors.New("an origin-only HTTPS URL, explicit CA file, and token file are required")
	}
	ca, err := readBoundedFile(cfg.CAFile, 1<<20, false)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("explicit CA file contains no certificates")
	}
	tokenRaw, err := readBoundedFile(cfg.TokenFile, 64<<10, true)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(string(tokenRaw))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("operator transport token is invalid")
	}
	transport := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DisableCompression:    true,
		DisableKeepAlives:     false,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	httpClient := &http.Client{Transport: transport, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &hostClient{origin: strings.TrimSuffix(cfg.URL, "/"), token: token, http: httpClient}, nil
}

func (c *hostClient) post(ctx context.Context, path string, body any, headers map[string]string, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.http.Do(req) // exactly one attempt; transport retries are not implemented
	if err != nil {
		return fmt.Errorf("scoped host outcome unknown: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return errors.New("scoped host response is unreadable or exceeds its bound")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := "HOST_REQUEST_REFUSED"
		var failure struct {
			Reason string `json:"reason"`
			// Native responses include a generic error label. Never expose it:
			// only the separately validated public refusal code leaves custody.
			Error string `json:"error,omitempty"`
		}
		if len(data) != 0 && cap.StrictDecode(data, &failure) == nil && publicReasonPattern.MatchString(failure.Reason) {
			reason = failure.Reason
		}
		return &HTTPError{Status: resp.StatusCode, Reason: reason}
	}
	if out == nil {
		if len(bytes.TrimSpace(data)) != 0 {
			return errors.New("scoped host returned an unexpected response body")
		}
		return nil
	}
	if err := cap.StrictDecode(data, out); err != nil {
		return errors.New("scoped host returned an invalid response")
	}
	return nil
}

type NativeClient struct{ host *hostClient }

func NewNativeClient(cfg EndpointConfig) (*NativeClient, error) {
	host, err := newHostClient(cfg)
	if err != nil {
		return nil, err
	}
	return &NativeClient{host: host}, nil
}

type PrepareResponse struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
}

type OperationStatus struct {
	ID               string `json:"id"`
	Owner            string `json:"owner"`
	Phase            string `json:"phase"`
	Reason           string `json:"reason,omitempty"`
	Output           string `json:"output,omitempty"`
	ReceiptDigest    string `json:"receiptDigest,omitempty"`
	CleanupConfirmed bool   `json:"cleanupConfirmed,omitempty"`
	// Correlation and provenance are receiver-produced native evidence. The
	// controller never derives these identifiers from a synthetic status hash.
	ParentIncarnation string          `json:"parentIncarnation,omitempty"`
	TurnID            string          `json:"turnId,omitempty"`
	ParentID          string          `json:"parentId,omitempty"`
	ChildID           string          `json:"childId,omitempty"`
	CellID            string          `json:"cellId,omitempty"`
	Execution         json.RawMessage `json:"execution,omitempty"`
	Substrate         json.RawMessage `json:"substrate,omitempty"`
}

func (c *NativeClient) Prepare(ctx context.Context, operation cellnauthority.PreparedOperation, decision cap.Decision) (PrepareResponse, error) {
	var out PrepareResponse
	if err := c.PreflightArtifacts(ctx, operation.Resolution.Decision); err != nil {
		return out, err
	}
	in := struct {
		Operation cellnauthority.PreparedOperation `json:"operation"`
		Decision  cap.Decision                     `json:"decision"`
	}{operation, decision}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > 262144 {
		return out, errors.New("scoped preparation exceeds the receiver request bound")
	}
	err = c.host.post(ctx, "/v1/scoped/prepare", in, nil, &out)
	return out, err
}

func (c *NativeClient) Start(ctx context.Context, id, owner string, execution, model cap.Token) (OperationStatus, error) {
	headers := map[string]string{"X-Celln-Execution-Permit": execution.Bearer()}
	if !model.Empty() {
		headers["X-Celln-Model-Permit"] = model.Bearer()
	}
	var out OperationStatus
	err := c.host.post(ctx, "/v1/scoped/start", map[string]string{"id": id, "owner": owner}, headers, &out)
	return out, err
}

func (c *NativeClient) Read(ctx context.Context, id string, decision cap.Decision, execution cap.Token) (OperationStatus, error) {
	var out OperationStatus
	err := c.host.post(ctx, "/v1/scoped/read", struct {
		ID       string       `json:"id"`
		Decision cap.Decision `json:"decision"`
	}{id, decision}, map[string]string{"X-Celln-Execution-Permit": execution.Bearer()}, &out)
	return out, err
}

func (c *NativeClient) Cleanup(ctx context.Context, id string, decision cap.Decision, execution cap.Token) (OperationStatus, error) {
	var out OperationStatus
	err := c.host.post(ctx, "/v1/scoped/cleanup", struct {
		ID       string       `json:"id"`
		Decision cap.Decision `json:"decision"`
	}{id, decision}, map[string]string{"X-Celln-Execution-Permit": execution.Bearer()}, &out)
	return out, err
}

type GatewayClient struct{ host *hostClient }

func NewGatewayClient(cfg EndpointConfig) (*GatewayClient, error) {
	host, err := newHostClient(cfg)
	if err != nil {
		return nil, err
	}
	return &GatewayClient{host: host}, nil
}

func (c *GatewayClient) Pin(ctx context.Context, in modelgateway.PinRequest) (modelgateway.PinResponse, error) {
	var out modelgateway.PinResponse
	err := c.host.post(ctx, "/internal/pin", in, nil, &out)
	return out, err
}

func (c *GatewayClient) Register(ctx context.Context, decision cap.Decision, execution cap.Token, connection string) error {
	return c.host.post(ctx, "/internal/register", struct {
		Decision       cap.Decision `json:"decision"`
		ConnectionName string       `json:"connectionName"`
	}{decision, connection}, map[string]string{"X-Celln-Execution-Permit": execution.Bearer()}, nil)
}

func (c *GatewayClient) Close(ctx context.Context, decision cap.Decision, cleanup cap.Token) error {
	return c.host.post(ctx, "/internal/close", struct {
		Decision cap.Decision `json:"decision"`
	}{decision}, map[string]string{"X-Celln-Execution-Permit": cleanup.Bearer()}, nil)
}
