// Package modelgateway implements the dedicated, fail-closed model credential
// and forwarding boundary for mediated Celln workloads.
package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
)

const (
	ReasonMalformed           = "MODEL_REQUEST_MALFORMED"
	ReasonUnauthorized        = "MODEL_AUTH_UNAUTHORIZED"
	ReasonForbidden           = "MODEL_AUTH_FORBIDDEN"
	ReasonRouteChanged        = "MODEL_ROUTE_CHANGED"
	ReasonCredentialChanged   = "MODEL_CREDENTIAL_SOURCE_CHANGED"
	ReasonStreaming           = "MODEL_STREAMING_UNSUPPORTED"
	ReasonProtocol            = "MODEL_PROTOCOL_UNSUPPORTED"
	ReasonDestination         = "MODEL_DESTINATION_FORBIDDEN"
	ReasonRequestConflict     = "MODEL_REQUEST_CONFLICT"
	ReasonProviderUnavailable = "MODEL_PROVIDER_UNAVAILABLE"
	ReasonResponseTooLarge    = "MODEL_RESPONSE_TOO_LARGE"
	ReasonUnavailable         = "MODEL_GATEWAY_UNAVAILABLE"
)

type Error struct {
	Reason string
	Status int
	Err    error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Reason
}
func (e *Error) Unwrap() error { return e.Err }
func fail(reason string, status int, err error) error {
	return &Error{Reason: reason, Status: status, Err: err}
}
func Reason(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Reason
	}
	if reason := modelbudget.Reason(err); reason != "" {
		return reason
	}
	if reason := cellncapability.Reason(err); reason != "" {
		return reason
	}
	return ReasonUnavailable
}

// BudgetStore is deliberately fail-closed. The production implementation is
// PostgreSQL-backed modelbudget.Store; no in-memory production fallback exists.
type BudgetStore interface {
	RegisterRun(context.Context, modelbudget.RunRegistration) error
	RegisterTurn(context.Context, modelbudget.TurnRegistration) error
	ReserveBound(context.Context, modelbudget.ReservationRequest, modelbudget.ReservationBinding) (modelbudget.Reservation, error)
	MarkInFlight(context.Context, string, string, string) error
	Reconcile(context.Context, string, string, string, int64, string, string) error
	ReconcileUnknown(context.Context, string, string, string, string, string) error
	CheckReady(context.Context) error
	Inspect(context.Context, string, string) (modelbudget.Usage, error)
	FenceRun(context.Context, string) error
	FenceTurn(context.Context, string, string) error
}

type AuthorityStore interface {
	RegisterAuthority(context.Context, Authority) error
	Authority(context.Context, string, string) (Authority, error)
	CheckReady(context.Context) error
}

// Authority is durable, secret-free route metadata. Credential data is always
// re-read from Kubernetes after capability and registration checks.
type Authority struct {
	Decision       cellncapability.Decision     `json:"decision"`
	BudgetID       string                       `json:"budgetId"`
	TurnID         string                       `json:"turnId"`
	DecisionDigest string                       `json:"decisionDigest"`
	ClusterID      string                       `json:"clusterId"`
	Namespace      string                       `json:"namespace"`
	NamespaceUID   string                       `json:"namespaceUid"`
	RunUID         string                       `json:"runUid"`
	RunSpecSHA256  string                       `json:"runSpecSha256"`
	ConnectionName string                       `json:"connectionName"`
	Endpoint       string                       `json:"endpoint"`
	AllowInsecure  bool                         `json:"allowInsecure"`
	Route          cellncapability.RouteBinding `json:"route"`
}

type PinRequest struct {
	Namespace                 string `json:"namespace"`
	ConnectionName            string `json:"connectionName"`
	ModelConnectionSpecSHA256 string `json:"modelConnectionSpecSha256"`
	CredentialSecretName      string `json:"credentialSecretName"`
	CredentialSecretKey       string `json:"credentialSecretKey"`
}

type PinResponse struct {
	ModelConnectionUID string `json:"modelConnectionUid"`
	SecretUID          string `json:"secretUid"`
}

type RegistrationRequest struct {
	Decision       json.RawMessage       `json:"decision"`
	ExecutionToken cellncapability.Token `json:"-"`
	ConnectionName string                `json:"connectionName"`
}

type InvokeRequest struct {
	Decision  json.RawMessage `json:"decision"`
	RequestID string          `json:"requestId"`
	Request   json.RawMessage `json:"request"`
}

type InvokeResponse struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

type Config struct {
	AuthorityReady      func(context.Context) error
	ClusterID           string
	RegistrationToken   cellncapability.Token
	MaxRequestBytes     int64
	MaxResponseBytes    int64
	MaxConcurrent       int
	MaxProviderDuration time.Duration
	AllowPrivateOrigins map[string]bool
}

func (c *Config) defaults() error {
	if c.AuthorityReady == nil {
		return fmt.Errorf("live authority readiness probe is required")
	}
	if c.ClusterID == "" {
		return fmt.Errorf("cluster id is required")
	}
	if c.RegistrationToken.Empty() {
		return fmt.Errorf("registration transport token is required")
	}
	if c.MaxRequestBytes == 0 {
		c.MaxRequestBytes = 1 << 20
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = 4 << 20
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 32
	}
	if c.MaxProviderDuration == 0 {
		c.MaxProviderDuration = 2 * time.Minute
	}
	if c.MaxRequestBytes < 1 || c.MaxResponseBytes < 1 || c.MaxConcurrent < 1 || c.MaxProviderDuration <= 0 {
		return fmt.Errorf("gateway limits must be positive")
	}
	return nil
}
