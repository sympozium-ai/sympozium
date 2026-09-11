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
	"strings"
)

// Persistent parent protocol (`/v1/parents*`). An endpoint is a frozen owner,
// not a load-balanced retry target. No operation retries automatically:
// transport errors and ambiguous responses preserve the exact incarnation/turn
// and never authorize a replacement POST.
var (
	ErrReconcile = errors.New("parent outcome requires reconciliation; preserve incarnation and turn ID")
	ErrNotFound  = errors.New("parent or turn not found; not permission to replay")
	hashPattern  = regexp.MustCompile(`^blake3:[0-9a-f]{64}$`)
	turnPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
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

func (c *Client) doJSON(ctx context.Context, method, path string, body any, output any, respondAsync bool) (int, error) {
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
	if respondAsync {
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
	if res.StatusCode == http.StatusNotFound {
		return res.StatusCode, ErrNotFound
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
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
		map[string]string{"apiVersion": "celln.parent-create/v1", "launchProfile": profile}, &result, false)
	if err != nil {
		return err
	}
	if status != http.StatusAccepted || result.Incarnation != incarnation || !result.Pending || result.Retry == nil || *result.Retry {
		return ErrReconcile
	}
	return nil
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
	if !turnPattern.MatchString(turn) || strings.TrimSpace(message) == "" || len(message) > 2048 || strings.ContainsRune(message, 0) {
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
	if code != http.StatusOK || result.Pending || result.Kind != "completed" || result.Version != "celln.parent-context/v1" || result.TurnID != turn || result.Succeeded == nil || len(result.Answer) > 2048 || strings.TrimSpace(result.Answer) == "" || strings.ContainsRune(result.Answer, 0) {
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
