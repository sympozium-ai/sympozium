package apiserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sympozium-ai/sympozium/internal/celln"
)

const (
	// cellnGatewayCellsTimeout bounds one listing; the console polls every 2 s
	// and the ConfigMap is always there to fall back to.
	cellnGatewayCellsTimeout = 3 * time.Second
	// cellnGatewayCellsLimit is asked of every backend. The console filters
	// live from finished itself, so finished cells are always requested.
	cellnGatewayCellsLimit = 200
	// cellnGatewayUnsupportedTTL is how long a 404 from `/v1/cells` is
	// remembered, so an older router is not probed on every poll.
	cellnGatewayUnsupportedTTL = 5 * time.Minute
)

// cellnCellsSource is the API server's memory of the gateway listing: the
// client for the configured router, and whether that router lacks `/v1/cells`.
// The zero value is ready to use.
type cellnCellsSource struct {
	mu sync.Mutex
	// now and unsupportedTTL are replaced in tests.
	now            func() time.Time
	unsupportedTTL time.Duration

	key              string
	client           *celln.Client
	unsupportedUntil time.Time
}

func (c *cellnCellsSource) clock() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// gateway returns the client for the router the capability probe also uses —
// same origin, same read-only credential, same plaintext acknowledgement — or
// nil when Celln is off, misconfigured, or known not to serve the listing.
func (c *cellnCellsSource) gateway(now time.Time) *celln.Client {
	if os.Getenv("CELLN_ENABLED") != "true" {
		return nil
	}
	origin := os.Getenv("CELLN_ROUTER_URL")
	if origin == "" {
		origin = defaultCellnRouterURL
	}
	origin = strings.TrimRight(origin, "/")
	tokenFile := os.Getenv("CELLN_CAPABILITY_TOKEN_FILE")
	insecure := os.Getenv("CELLN_ALLOW_INSECURE_HTTP") == "true"
	// The capability probe refuses plaintext without the acknowledgement even
	// on loopback, which celln.New alone would permit.
	if tokenFile == "" || (!insecure && !strings.HasPrefix(origin, "https://")) {
		return nil
	}
	key := fmt.Sprintf("%s\x00%s\x00%t", origin, tokenFile, insecure)
	c.mu.Lock()
	defer c.mu.Unlock()
	if key != c.key {
		if c.client != nil {
			c.client.Close()
		}
		c.key, c.client, c.unsupportedUntil = key, nil, time.Time{}
		client, err := celln.New(celln.Config{BaseURL: origin, TokenFile: tokenFile, AllowInsecure: insecure, Timeout: cellnGatewayCellsTimeout, NoProxy: true})
		if err == nil {
			c.client = client
		}
	}
	if c.client == nil || now.Before(c.unsupportedUntil) {
		return nil
	}
	return c.client
}

func (c *cellnCellsSource) markUnsupported(client *celln.Client, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != client {
		return
	}
	ttl := c.unsupportedTTL
	if ttl <= 0 {
		ttl = cellnGatewayUnsupportedTTL
	}
	c.unsupportedUntil = now.Add(ttl)
}

// cellnGatewayCells lists every node through the gateway. ok is false when the
// caller should read the nodes' ConfigMap reports instead: no usable gateway,
// an older router (remembered for a while), or any failure to list.
func (s *Server) cellnGatewayCells(ctx context.Context, now time.Time) (nodes []CellnNodeCells, ok bool) {
	client := s.cellnCells.gateway(now)
	if client == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, cellnGatewayCellsTimeout)
	defer cancel()
	listing, err := client.ListCells(ctx, true, cellnGatewayCellsLimit)
	if errors.Is(err, celln.ErrCellsUnsupported) {
		s.cellnCells.markUnsupported(client, now)
		return nil, false
	}
	if err != nil {
		s.log.V(1).Info("Celln gateway cells listing failed; using node reports", "error", err.Error())
		return nil, false
	}
	return cellnNodesFromGateway(listing, now), true
}

// cellnNodesFromGateway decodes a gateway listing into node entries. A backend
// that gave a reason instead of a report becomes a node with Error set and a
// placeholder name, as the gateway does not say which node it is.
func cellnNodesFromGateway(listing celln.CellsListing, now time.Time) []CellnNodeCells {
	out := make([]CellnNodeCells, 0, len(listing.Nodes))
	seen := map[string]bool{}
	for _, item := range listing.Nodes {
		entry := CellnNodeCells{Node: fmt.Sprintf("node-%d", item.Index), ReportedMs: now.UnixMilli(), Source: cellnSourceGateway}
		if item.Report == nil {
			entry.Error = "gateway: " + cellnGatewayReason(item.Reason)
			out = append(out, entry)
			continue
		}
		// Fleet dispatchers run with --node-name set to their Kubernetes
		// node, so the report names the node the ConfigMap also uses.
		if name := item.Report.Node; validCellnNodeName(name) && !seen[name] {
			entry.Node = name
		}
		seen[entry.Node] = true
		for _, c := range item.Report.Cells {
			entry.Cells = append(entry.Cells, CellnCell{
				ID: c.ID, Description: c.Description, Status: c.Status, Backend: c.Backend,
				StartedMs: c.StartedMs, FinishedMs: c.FinishedMs, DurationMs: c.DurationMs, Error: c.Error, Tools: c.Tools,
			})
		}
		for _, p := range item.Report.Parents {
			live := p.Live
			parent := CellnNodeParent{Incarnation: p.Incarnation, Status: p.Status, StatusLive: &live, Turns: make([]CellnParentTurn, 0, len(p.Turns))}
			if p.UpdatedMs != nil {
				parent.UpdatedMs = *p.UpdatedMs
			}
			for _, t := range p.Turns {
				parent.Turns = append(parent.Turns, CellnParentTurn{TurnID: t.TurnID, Stage: normalizeCellnStage(t.Stage), Child: t.Child, Succeeded: t.Succeeded, TimeoutMs: t.TimeoutMs})
			}
			entry.Parents = append(entry.Parents, parent)
		}
		out = append(out, entry)
	}
	return out
}

// fillCellnUnreportedNodes replaces gateway entries that carry only a reason
// with the ConfigMap reports of nodes the gateway did not list. The gateway
// does not name a failing backend, so a node report stands in for one exactly
// when no listed node has its name; fresh reports are preferred. Entries left
// over keep their error.
func (s *Server) fillCellnUnreportedNodes(ctx context.Context, nodes []CellnNodeCells, now time.Time) []CellnNodeCells {
	missing := 0
	listed := map[string]bool{}
	for _, n := range nodes {
		if n.Error != "" {
			missing++
		} else {
			listed[n.Node] = true
		}
	}
	if missing == 0 {
		return nodes
	}
	data, err := s.cellnNodeReports(ctx)
	if err != nil || len(data) == 0 {
		return nodes
	}
	var spare []CellnNodeCells
	for _, report := range decodeCellnNodeReports(data, now) {
		if !listed[report.Node] && report.Error == "" {
			spare = append(spare, report)
		}
	}
	sort.Slice(spare, func(i, j int) bool {
		if spare[i].Stale != spare[j].Stale {
			return !spare[i].Stale
		}
		return spare[i].Node < spare[j].Node
	})
	for i := range nodes {
		if nodes[i].Error == "" || len(spare) == 0 {
			continue
		}
		nodes[i], spare = spare[0], spare[1:]
	}
	return nodes
}

func validCellnNodeName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_') {
			return false
		}
	}
	return true
}

// cellnGatewayReason keeps a backend's reason code short and printable.
func cellnGatewayReason(reason string) string {
	var b strings.Builder
	for _, r := range reason {
		if b.Len() >= 120 {
			break
		}
		if r < 32 || r == 127 {
			r = ' '
		}
		b.WriteRune(r)
	}
	return strings.ReplaceAll(b.String(), "_", " ")
}
