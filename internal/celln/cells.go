package celln

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// CellsAPIVersion is the contract of the gateway's `GET /v1/cells` listing and
// of every node report inside it.
const CellsAPIVersion = "celln.cells/v1"

const (
	// MaxCellsLimit is the largest `limit` the gateway accepts.
	MaxCellsLimit = 500
	// maxCellsBackends bounds the node entries of one listing.
	maxCellsBackends = 32
	// maxCellsResponseBytes bounds the whole listing. A backend's report is at
	// most 2 MiB, so this is well under the worst case on purpose: a listing
	// this large is refused rather than buffered.
	maxCellsResponseBytes = 16 * 1024 * 1024
)

// ErrCellsUnsupported means the gateway has no `/v1/cells` endpoint (a Celln
// release older than the listing). Callers fall back to another source and
// need not ask again soon.
var ErrCellsUnsupported = errors.New("Celln: gateway does not serve /v1/cells")

// CellsListing is the gateway's answer: one entry per backend, in backend order.
type CellsListing struct {
	APIVersion string           `json:"apiVersion"`
	Nodes      []CellsNodeEntry `json:"nodes"`
}

// CellsNodeEntry carries a backend's report, or the reason it has none.
type CellsNodeEntry struct {
	Index  int              `json:"index"`
	Report *CellsNodeReport `json:"report,omitempty"`
	Reason string           `json:"reason,omitempty"`
}

// CellsNodeReport is what one dispatcher lists for its authority root.
type CellsNodeReport struct {
	APIVersion string        `json:"apiVersion"`
	Node       string        `json:"node"`
	Cells      []Cell        `json:"cells"`
	Parents    []CellsParent `json:"parents"`
}

// Cell is one cell as `celln ps -a` reports it.
type Cell struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Backend     string   `json:"backend"`
	StartedMs   int64    `json:"started_ms"`
	FinishedMs  *int64   `json:"finished_ms"`
	DurationMs  *int64   `json:"duration_ms"`
	Error       *string  `json:"error"`
	Tools       []string `json:"tools"`
}

// CellsParent is one parent incarnation with its owner's observation of it.
type CellsParent struct {
	Incarnation string            `json:"incarnation"`
	Status      string            `json:"status"`
	Live        bool              `json:"statusIsLiveOwnerObservation"`
	UpdatedMs   *int64            `json:"updated_ms"`
	Turns       []CellsParentTurn `json:"turns"`
	TurnsTotal  int               `json:"turns_total"`
}

// CellsParentTurn is one journalled turn. Succeeded is absent while reserved.
type CellsParentTurn struct {
	TurnID     string `json:"turnId"`
	Stage      string `json:"stage"`
	Child      string `json:"child,omitempty"`
	TimeoutMs  int64  `json:"timeout_ms,omitempty"`
	ReservedMs *int64 `json:"reserved_ms,omitempty"`
	Succeeded  *bool  `json:"succeeded,omitempty"`
}

// ListCells lists cells and parents on every backend through the gateway. With
// all false only live ones are listed. A 404 is ErrCellsUnsupported; any other
// failure (transport, 5xx, oversized or malformed body) is an ordinary error
// the caller may retry.
func (c *Client) ListCells(ctx context.Context, all bool, limit int) (CellsListing, error) {
	if limit < 1 || limit > MaxCellsLimit {
		return CellsListing{}, fmt.Errorf("Celln: cells limit must be 1..%d", MaxCellsLimit)
	}
	token, err := c.credential()
	if err != nil {
		return CellsListing{}, err
	}
	for _, b := range []byte(token) {
		if b < 33 || b > 126 {
			return CellsListing{}, errors.New("Celln: invalid credential")
		}
	}
	query := url.Values{"all": {strconv.FormatBool(all)}, "limit": {strconv.Itoa(limit)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.base.String(), "/")+"/v1/cells?"+query.Encode(), nil)
	if err != nil {
		return CellsListing{}, errors.New("Celln: cannot build cells request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return CellsListing{}, errors.New("Celln: cells listing unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return CellsListing{}, ErrCellsUnsupported
	}
	if res.StatusCode != http.StatusOK {
		return CellsListing{}, fmt.Errorf("Celln: cells listing answered %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxCellsResponseBytes+1))
	if err != nil {
		return CellsListing{}, errors.New("Celln: cells listing unreadable")
	}
	if len(data) > maxCellsResponseBytes {
		return CellsListing{}, errors.New("Celln: cells listing too large")
	}
	var listing CellsListing
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&listing) != nil || decoder.Decode(new(any)) != io.EOF {
		return CellsListing{}, errors.New("Celln: cells listing malformed")
	}
	if listing.APIVersion != CellsAPIVersion || len(listing.Nodes) > maxCellsBackends {
		return CellsListing{}, errors.New("Celln: cells listing contract is incompatible")
	}
	for _, node := range listing.Nodes {
		if node.Index < 0 || (node.Report == nil) == (node.Reason == "") ||
			(node.Report != nil && node.Report.APIVersion != CellsAPIVersion) {
			return CellsListing{}, errors.New("Celln: cells listing contract is incompatible")
		}
	}
	return listing, nil
}
