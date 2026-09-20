package celln

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Persistent parent protocol (`/v1/parents*`). An endpoint is a frozen owner,
// not a load-balanced retry target. No operation retries automatically:
// transport errors and ambiguous responses preserve the exact incarnation/turn
// and never authorize a replacement POST.
var (
	ErrReconcile = errors.New("parent outcome requires reconciliation; preserve incarnation and turn ID")
	ErrNotFound  = errors.New("parent or turn not found; not permission to replay")
	// ErrOwnerRemoved is the gateway's definitive statement that the owner
	// bound to this incarnation is no longer part of the execution plane. Its
	// in-process parent context is gone and the identity is never re-placed.
	ErrOwnerRemoved = errors.New("parent owner left the execution plane; live context lost and never re-placed")
	// ErrCreateRefused is the owner's definitive refusal of one create (node
	// capacity or authority). Nothing was started; the incarnation is spent
	// and never retried.
	ErrCreateRefused = errors.New("owner refused parent creation; this incarnation is never retried")
	hashPattern      = regexp.MustCompile(`^blake3:[0-9a-f]{64}$`)
	turnPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Bounds of the enduring-conversation protocol, mirroring the Celln host
// (celln-warden parent_protocol). A turn-status response carries the
// journal's reservation, which holds the whole worker task (history plus
// message, up to 16384 bytes, escaped again inside a JSON string), so it runs
// to tens of kilobytes; the response bound leaves room for that.
const (
	// MaxParentMessageBytes bounds one user message.
	MaxParentMessageBytes = 2048
	// MaxParentAnswerBytes bounds one committed worker answer. A fleet still
	// on a starter package built for 2048-byte answers is held to that by
	// the Celln host, as a failed turn; this client accepts both.
	MaxParentAnswerBytes = 8192
	// MaxParentResponseBytes bounds any one owner response body.
	MaxParentResponseBytes = 131072
)

const (
	ownerRemovedError  = "original parent backend removed"
	createRefusedError = "parent creation refused; reconcile incarnation"
)

// ParentStatus is a live owner observation of a parent incarnation.
type ParentStatus struct {
	Incarnation string `json:"incarnation"`
	Status      string `json:"status"`
	Live        bool   `json:"statusIsLiveOwnerObservation"`
	Retry       *bool  `json:"retryAuthorized"`
}

// ParentTurnResult is one committed (or pending) turn outcome.
type ParentTurnResult struct {
	Kind      string `json:"kind,omitempty"`
	Version   string `json:"apiVersion,omitempty"`
	TurnID    string `json:"turnId,omitempty"`
	Succeeded *bool  `json:"succeeded,omitempty"`
	Answer    string `json:"answer,omitempty"`
	Pending   bool   `json:"pending,omitempty"`
	Retry     *bool  `json:"retryAuthorized,omitempty"`
}

// ParentTurnEvidence preserves both historical stage and live-owner observation.
type ParentTurnEvidence struct {
	Turn struct {
		Stage  string          `json:"stage"`
		Record json.RawMessage `json:"record"`
	} `json:"turn"`
	Retry       *bool  `json:"retryAuthorized"`
	OwnerStatus string `json:"ownerStatus"`
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, output any, respondAsync bool, headers ...http.Header) (int, error) {
	token, err := c.credential()
	if err != nil {
		return 0, err
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
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.base.String(), "/")+path, bytes.NewReader(payload))
	if err != nil {
		return 0, errors.New("invalid parent request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	for _, h := range headers {
		for key, values := range h {
			req.Header[key] = values
		}
	}
	if respondAsync {
		req.Header.Set("Prefer", "respond-async")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, ErrReconcile
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, MaxParentResponseBytes+1))
	if err != nil || len(data) > MaxParentResponseBytes {
		return res.StatusCode, ErrReconcile
	}
	if res.StatusCode == http.StatusNotFound {
		return res.StatusCode, ErrNotFound
	}
	if res.StatusCode == http.StatusServiceUnavailable {
		var refusal struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &refusal) == nil && refusal.Error == ownerRemovedError {
			return res.StatusCode, ErrOwnerRemoved
		}
	}
	if res.StatusCode == http.StatusConflict && method == http.MethodPost && path == "/v1/parents" {
		var refusal struct {
			Error string `json:"error"`
		}
		// Only the owner's own refusal is definitive; the gateway's "already
		// claimed" conflict means an owner may hold the parent.
		if json.Unmarshal(data, &refusal) == nil && refusal.Error == createRefusedError {
			return res.StatusCode, ErrCreateRefused
		}
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		return res.StatusCode, fmt.Errorf("%w: owner answered %d %s", ErrReconcile, res.StatusCode, refusalText(data))
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

// CreateParent provisions one parent incarnation from an operator-approved
// launch profile. It is at-most-once: a repeated POST of the same incarnation
// must be refused by the owner, not replayed.
func (c *Client) CreateParent(ctx context.Context, profile, incarnation string) error {
	if !hashPattern.MatchString(profile) || !hashPattern.MatchString(incarnation) {
		return errors.New("invalid frozen parent selection")
	}
	var result struct {
		Incarnation string `json:"incarnation"`
		Pending     bool   `json:"initializationPending"`
		Retry       *bool  `json:"retryAuthorized"`
	}
	status, err := c.doJSON(ctx, http.MethodPost, "/v1/parents",
		map[string]string{"apiVersion": "celln.parent-create/v1", "launchProfile": profile}, &result, false,
		http.Header{"X-Celln-Parent-Incarnation": []string{incarnation}})
	if err != nil {
		return err
	}
	if status != http.StatusAccepted || result.Incarnation != incarnation || !result.Pending || result.Retry == nil || *result.Retry {
		return ErrReconcile
	}
	return nil
}

// ProvisionParent asks the owner the gateway binds to incarnation to issue the
// permit and launch profile for an operator plan. Identical plans recover the
// same launch; the owner neither creates nor claims a parent here.
func (c *Client) ProvisionParent(ctx context.Context, plan json.RawMessage, incarnation string) (string, error) {
	if !hashPattern.MatchString(incarnation) || len(plan) > 65536 || !json.Valid(plan) {
		return "", errors.New("invalid frozen parent provision plan")
	}
	var result struct {
		APIVersion    string `json:"apiVersion"`
		LaunchProfile string `json:"launchProfile"`
		Incarnation   string `json:"incarnation"`
	}
	status, err := c.doJSON(ctx, http.MethodPost, "/v1/parents/provision", plan, &result, false,
		http.Header{"X-Celln-Parent-Incarnation": []string{incarnation}})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK || result.APIVersion != "celln.parent-provisioned/v1" || result.Incarnation != incarnation || !hashPattern.MatchString(result.LaunchProfile) {
		return "", ErrReconcile
	}
	return result.LaunchProfile, nil
}

// ParentStatus returns the live owner observation for an incarnation.
func (c *Client) ParentStatus(ctx context.Context, id string) (ParentStatus, error) {
	var result ParentStatus
	path, err := parentPath(id)
	if err != nil {
		return result, err
	}
	code, err := c.doJSON(ctx, http.MethodGet, path, nil, &result, false)
	if err != nil {
		return result, err
	}
	valid := false
	for _, status := range []string{"Initializing", "Ready", "TurnActive", "Stopping", "Stopped", "ContextLost", "TeardownUncertain"} {
		valid = valid || result.Status == status
	}
	if code != http.StatusOK || !valid || result.Incarnation != id || result.Retry == nil || *result.Retry || (!result.Live && result.Status != "ContextLost") {
		return ParentStatus{}, ErrReconcile
	}
	return result, nil
}

// SubmitParentTurn submits one bounded turn and waits for its outcome.
func (c *Client) SubmitParentTurn(ctx context.Context, id, turn, message string) (ParentTurnResult, error) {
	return c.submitParentTurn(ctx, id, turn, message, false)
}

// SubmitParentTurnAsync releases the caller while the owner executes; Pending is
// not permission to submit again.
func (c *Client) SubmitParentTurnAsync(ctx context.Context, id, turn, message string) (ParentTurnResult, error) {
	return c.submitParentTurn(ctx, id, turn, message, true)
}

func (c *Client) submitParentTurn(ctx context.Context, id, turn, message string, respondAsync bool) (ParentTurnResult, error) {
	var result ParentTurnResult
	path, err := parentPath(id)
	if err != nil {
		return result, err
	}
	if !turnPattern.MatchString(turn) || strings.TrimSpace(message) == "" || len(message) > MaxParentMessageBytes || strings.ContainsRune(message, 0) {
		return result, errors.New("invalid bounded parent turn")
	}
	body := map[string]string{"kind": "turn", "apiVersion": "celln.parent-context/v1", "turnId": turn, "message": message}
	code, err := c.doJSON(ctx, http.MethodPost, path+"/turns", body, &result, respondAsync)
	if err != nil {
		return result, err
	}
	if code == http.StatusAccepted && result.Pending && result.Retry != nil && !*result.Retry && result.Kind == "" && result.TurnID == "" && result.Succeeded == nil && result.Answer == "" {
		return result, nil
	}
	if code != http.StatusOK || result.Pending || result.Kind != "completed" || result.Version != "celln.parent-context/v1" || result.TurnID != turn || result.Succeeded == nil || len(result.Answer) > MaxParentAnswerBytes || strings.TrimSpace(result.Answer) == "" || strings.ContainsRune(result.Answer, 0) {
		return ParentTurnResult{}, ErrReconcile
	}
	return result, nil
}

// StopParent requests teardown of an incarnation.
func (c *Client) StopParent(ctx context.Context, id string) error {
	path, err := parentPath(id)
	if err != nil {
		return err
	}
	var result struct {
		Confirmed bool `json:"teardownConfirmed"`
	}
	code, err := c.doJSON(ctx, http.MethodPost, path+"/stop", nil, &result, false)
	if err != nil {
		return err
	}
	if code != http.StatusOK || !result.Confirmed {
		return ErrReconcile
	}
	return nil
}

// CancelParent requests cancellation of an entire parent.
func (c *Client) CancelParent(ctx context.Context, id string) error {
	path, err := parentPath(id)
	if err != nil {
		return err
	}
	var result struct {
		Requested bool  `json:"cancellationRequested"`
		Confirmed *bool `json:"teardownConfirmed"`
	}
	code, err := c.doJSON(ctx, http.MethodPost, path+"/cancel", nil, &result, false)
	if err != nil {
		return err
	}
	if code != http.StatusAccepted || !result.Requested || result.Confirmed == nil || *result.Confirmed {
		return ErrReconcile
	}
	return nil
}

// CancelParentTurn signals only this immutable turn's child. Success is not
// proof of teardown or parent readiness; callers must reconcile the durable
// turn result. It never falls back to CancelParent, which stops the whole parent.
func (c *Client) CancelParentTurn(ctx context.Context, id, turn string) error {
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
	code, err := c.doJSON(ctx, http.MethodPost, path+"/turns/"+turn+"/cancel", nil, &result, false)
	if err != nil {
		return err
	}
	if code != http.StatusAccepted || !result.Requested || result.Confirmed == nil || *result.Confirmed || result.Retry == nil || *result.Retry {
		return ErrReconcile
	}
	return nil
}

// ParentTurn returns the durable evidence for a turn, reconciling stage and
// live owner status.
func (c *Client) ParentTurn(ctx context.Context, id, turn string) (ParentTurnEvidence, error) {
	var result ParentTurnEvidence
	path, err := parentPath(id)
	if err != nil {
		return result, err
	}
	if !turnPattern.MatchString(turn) {
		return result, fmt.Errorf("invalid turn ID")
	}
	code, err := c.doJSON(ctx, http.MethodGet, path+"/turns/"+turn, nil, &result, false)
	if err != nil {
		return result, err
	}
	if code != http.StatusOK || result.Retry == nil || *result.Retry {
		return ParentTurnEvidence{}, ErrReconcile
	}
	switch result.OwnerStatus {
	case "Initializing", "Ready", "TurnActive", "Stopping", "Stopped", "ContextLost", "TeardownUncertain":
	default:
		return ParentTurnEvidence{}, ErrReconcile
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
		return ParentTurnEvidence{}, ErrReconcile
	}
	switch result.Turn.Stage {
	case "reserved":
		if record.Request.TurnID != turn {
			return ParentTurnEvidence{}, ErrReconcile
		}
	case "child-destroyed", "parent-committed":
		if record.TurnID != turn {
			return ParentTurnEvidence{}, ErrReconcile
		}
	default:
		return ParentTurnEvidence{}, ErrReconcile
	}
	return result, nil
}

// refusalText is the owner's bounded, printable error field for logs; the
// body is never interpreted beyond that.
func refusalText(data []byte) string {
	var refusal struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &refusal) != nil {
		return "(no error field)"
	}
	text := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return -1
		}
		return r
	}, refusal.Error)
	if len(text) > 200 {
		text = text[:200]
	}
	return strconv.Quote(text)
}
