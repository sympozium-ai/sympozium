// Package modelbudget implements the durable PostgreSQL model-accounting ledger
// used by mediated Celln model access. It has no in-memory production fallback.
package modelbudget

import (
	"errors"
	"fmt"
	"time"
)

const (
	ReasonExhausted        = "AUTH_BUDGET_EXHAUSTED"
	ReasonRegisterConflict = "AUTH_BUDGET_REGISTER_CONFLICT"
	ReasonRequestConflict  = "AUTH_REQUEST_ID_CONFLICT"
	ReasonUnavailable      = "AUTH_ACCOUNTING_UNAVAILABLE"
	ReasonClosed           = "AUTH_BUDGET_CLOSED"
	ReasonDeadline         = "AUTH_WORK_DEADLINE_EXPIRED"
	ReasonNotFound         = "AUTH_BUDGET_NOT_REGISTERED"
	ReasonUsageExceeded    = "AUTH_PROVIDER_USAGE_EXCEEDS_RESERVATION"
)

type Error struct {
	Reason string
	Err    error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Reason
	}
	return fmt.Sprintf("%s: %v", e.Reason, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

func reasonError(reason string, err error) error { return &Error{Reason: reason, Err: err} }

func Reason(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Reason
	}
	return ""
}

type RunRegistration struct {
	BudgetID        string
	ClusterID       string
	NamespaceUID    string
	RunUID          string
	DecisionDigest  string
	RouteDigest     string
	MaxRequests     int64
	MaxOutputTokens int64
	MaxTurns        int64
	ParentDeadline  time.Time
}

type TurnRegistration struct {
	BudgetID        string
	TurnID          string
	DecisionDigest  string
	MaxRequests     int64
	MaxOutputTokens int64
	Deadline        time.Time
}

type ReservationRequest struct {
	BudgetID             string
	TurnID                string
	RequestID             string
	RequestDigest         string
	ReservedOutputTokens  int64
}

type Reservation struct {
	BudgetID             string
	TurnID                string
	RequestID             string
	RequestDigest         string
	ReservedOutputTokens  int64
	ObservedOutputTokens  *int64
	State                  string
	Outcome                string
	ProviderAttempted      bool
	Existing               bool
}

type Usage struct {
	RunReservedRequests      int64
	RunReservedOutputTokens  int64
	RunObservedOutputTokens  int64
	TurnReservedRequests     int64
	TurnReservedOutputTokens int64
	TurnObservedOutputTokens int64
	RunClosed                bool
	TurnClosed               bool
}
