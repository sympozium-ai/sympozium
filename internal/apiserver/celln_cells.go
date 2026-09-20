package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// The cells a fleet node runs come from one of two sources. The Celln gateway
// lists them itself (`GET /v1/cells`, see celln_cells_gateway.go) once the
// installed Celln release serves that; until then each node reports what
// `celln ps -a` shows for its authority root, and its recent parents, to one
// ConfigMap (charts/sympozium/files/celln/fleet-cells.py). Both are decoded
// into []CellnNodeCells, and attributeCellnCells joins cells and parents to the
// AgentRuns they belong to.

const (
	cellnSourceGateway    = "gateway"
	cellnSourceNodeReport = "node-report"
)

// Turn stages, in the one vocabulary this API answers with. The node reporter
// already uses it; the gateway's "parent-committed" is mapped onto it.
const (
	cellnStageReserved       = "reserved"
	cellnStageChildDestroyed = "child-destroyed"
	cellnStageCommitted      = "committed"
)

// cellnNodeStaleAfter marks a node whose last report is older than this.
const cellnNodeStaleAfter = 90 * time.Second

// CellnCell is one cell as `celln ps -a` reports it.
type CellnCell struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Backend     string   `json:"backend"`
	StartedMs   int64    `json:"started_ms"`
	FinishedMs  *int64   `json:"finished_ms"`
	DurationMs  *int64   `json:"duration_ms"`
	Error       *string  `json:"error"`
	Tools       []string `json:"tools"`
	// Run is the AgentRun whose parent ran this cell as a turn, when known.
	Run *CellnRunRef `json:"run,omitempty"`
	// Parent is the parent incarnation the cell ran a turn for.
	Parent string `json:"parent,omitempty"`
	Turn   string `json:"turn,omitempty"`
}

// CellnParentTurn is one turn recorded in a parent's journal.
type CellnParentTurn struct {
	TurnID    string `json:"turnId"`
	Stage     string `json:"stage"`
	Child     string `json:"child,omitempty"`
	Succeeded *bool  `json:"succeeded,omitempty"`
	TimeoutMs int64  `json:"timeoutMs,omitempty"`
}

// CellnNodeParent is one parent a node's journal records.
type CellnNodeParent struct {
	Incarnation string            `json:"incarnation"`
	UpdatedMs   int64             `json:"updatedMs"`
	Turns       []CellnParentTurn `json:"turns"`
	Run         *CellnRunRef      `json:"run,omitempty"`
	// Status is the owner's observation of the parent (Ready, TurnActive,
	// ContextLost, ...). Only the gateway reports it; StatusLive says whether
	// it was observed live rather than read back from the journal.
	Status     string `json:"status,omitempty"`
	StatusLive *bool  `json:"statusLive,omitempty"`
}

// CellnRunRef names an AgentRun and its state.
type CellnRunRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Agent     string `json:"agent"`
	Phase     string `json:"phase"`
	// Live is true while the run is not finished, i.e. its parent should
	// still hold a context on the node.
	Live bool `json:"live"`
}

// CellnNodeCells is one node's cells and parents.
type CellnNodeCells struct {
	Node       string `json:"node"`
	ReportedMs int64  `json:"reportedMs"`
	Stale      bool   `json:"stale"`
	Error      string `json:"error,omitempty"`
	// Source is where this node's data came from: the Celln gateway's
	// `/v1/cells` ("gateway"), or the node's own report in the fleet cells
	// ConfigMap ("node-report").
	Source  string            `json:"source,omitempty"`
	Cells   []CellnCell       `json:"cells"`
	Parents []CellnNodeParent `json:"parents"`
}

type cellnNodeReport struct {
	APIVersion string            `json:"apiVersion"`
	Node       string            `json:"node"`
	ReportedMs int64             `json:"reportedMs"`
	Cells      []CellnCell       `json:"cells"`
	Parents    []CellnNodeParent `json:"parents"`
}

func (s *Server) listCellnFleetCells(w http.ResponseWriter, r *http.Request) {
	now := s.cellnCells.clock()
	nodes, fromGateway := s.cellnGatewayCells(r.Context(), now)
	if fromGateway {
		nodes = s.fillCellnUnreportedNodes(r.Context(), nodes, now)
	} else {
		data, err := s.cellnNodeReports(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		nodes = decodeCellnNodeReports(data, now)
	}
	if len(nodes) == 0 {
		writeJSON(w, []CellnNodeCells{})
		return
	}
	var runs api.AgentRunList
	if err := s.client.List(r.Context(), &runs); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, attributeCellnCells(nodes, runs.Items))
}

// cellnNodeReports reads every node's report; no ConfigMap means no reports.
func (s *Server) cellnNodeReports(ctx context.Context) (map[string]string, error) {
	var cm corev1.ConfigMap
	err := s.client.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: cellninstall.FleetCellsConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return cm.Data, nil
}

// joinCellnCells decodes every node's ConfigMap report and attributes it.
func joinCellnCells(data map[string]string, runs []api.AgentRun, now time.Time) []CellnNodeCells {
	return attributeCellnCells(decodeCellnNodeReports(data, now), runs)
}

// decodeCellnNodeReports turns the ConfigMap's per-node JSON into node entries.
func decodeCellnNodeReports(data map[string]string, now time.Time) []CellnNodeCells {
	out := make([]CellnNodeCells, 0, len(data))
	for node, raw := range data {
		entry := CellnNodeCells{Node: node, Source: cellnSourceNodeReport}
		var report cellnNodeReport
		if err := json.Unmarshal([]byte(raw), &report); err != nil || report.APIVersion != "sympozium.ai/celln-node-cells-v1" {
			entry.Error = "unreadable report"
			entry.Stale = true
			out = append(out, entry)
			continue
		}
		entry.ReportedMs = report.ReportedMs
		entry.Stale = now.Sub(time.UnixMilli(report.ReportedMs)) > cellnNodeStaleAfter
		entry.Cells, entry.Parents = report.Cells, report.Parents
		// A node writes this report: keep only what a node may say. Run
		// attribution and the owner's status are never taken from it.
		for i := range entry.Parents {
			p := &entry.Parents[i]
			p.Run, p.Status, p.StatusLive = nil, "", nil
			for j := range p.Turns {
				p.Turns[j].Stage = normalizeCellnStage(p.Turns[j].Stage)
			}
		}
		for i := range entry.Cells {
			c := &entry.Cells[i]
			c.Run, c.Parent, c.Turn = nil, "", ""
		}
		out = append(out, entry)
	}
	return out
}

// normalizeCellnStage maps either source's stage name onto one vocabulary.
func normalizeCellnStage(stage string) string {
	switch stage {
	case "committed", "parent-committed":
		return cellnStageCommitted
	case "child-destroyed", "destroyed":
		return cellnStageChildDestroyed
	case "reserved":
		return cellnStageReserved
	}
	return stage
}

// attributeCellnCells attributes every node's parents, and the cells their
// turns ran in, to AgentRuns by parent incarnation and child hash. It works on
// the decoded model, so both sources share it. Nodes come back sorted by name.
func attributeCellnCells(nodes []CellnNodeCells, runs []api.AgentRun) []CellnNodeCells {
	type turnRef struct {
		run          *CellnRunRef
		parent, turn string
	}
	byIncarnation := map[string]*CellnRunRef{}
	for i := range runs {
		run := &runs[i]
		parent := run.Status.CellnParent
		if parent == nil || parent.Binding.Incarnation == "" {
			continue
		}
		phase := string(run.Status.Phase)
		byIncarnation[parent.Binding.Incarnation] = &CellnRunRef{
			Namespace: run.Namespace, Name: run.Name, Agent: run.Spec.AgentRef, Phase: phase,
			Live: run.DeletionTimestamp == nil && phase != "Succeeded" && phase != "Failed",
		}
	}
	for n := range nodes {
		entry := &nodes[n]
		if entry.Cells == nil {
			entry.Cells = []CellnCell{}
		}
		if entry.Parents == nil {
			entry.Parents = []CellnNodeParent{}
		}
		// ps truncates a cell's description (the child hash) with an
		// ellipsis; match turns on that prefix.
		children := map[string]turnRef{}
		for i := range entry.Parents {
			p := &entry.Parents[i]
			p.Run = byIncarnation[p.Incarnation]
			if p.Turns == nil {
				p.Turns = []CellnParentTurn{}
			}
			for _, t := range p.Turns {
				if t.Child != "" {
					children[t.Child] = turnRef{run: p.Run, parent: p.Incarnation, turn: t.TurnID}
				}
			}
		}
		for i := range entry.Cells {
			c := &entry.Cells[i]
			prefix := strings.TrimSuffix(c.Description, "…")
			if len(prefix) > len("blake3:") {
				for child, ref := range children {
					if strings.HasPrefix(child, prefix) {
						c.Run, c.Parent, c.Turn = ref.run, ref.parent, ref.turn
						break
					}
				}
			}
			if c.Tools == nil {
				c.Tools = []string{}
			}
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Node < nodes[j].Node })
	return nodes
}
