package apiserver

import (
	"encoding/json"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestJoinCellnCellsAttributesTurnsToRuns(t *testing.T) {
	now := time.UnixMilli(1_000_000_000)
	child := "blake3:c8637381b4957825e2794a01e3fee6c6cb23c6be229dc6f1ff8322069b0ff61c"
	report := map[string]any{
		"apiVersion": "sympozium.ai/celln-node-cells-v1", "node": "framework", "reportedMs": now.UnixMilli() - 5000,
		"cells": []map[string]any{
			{"id": "2e1e3481f3b7", "description": "blake3:c8637381b4957825e2794a0…", "status": "running", "backend": "kvm", "started_ms": 1, "tools": []string{"/worker"}},
			{"id": "0000000000aa", "description": "blake3:ffff", "status": "dissolved", "backend": "kvm", "started_ms": 1, "finished_ms": 3, "duration_ms": 2},
		},
		"parents": []map[string]any{{"incarnation": "blake3:parent", "updatedMs": 1, "turns": []map[string]any{{"turnId": "initial", "stage": "reserved", "child": child}}}},
	}
	raw, _ := json.Marshal(report)
	stale := map[string]any{"apiVersion": "sympozium.ai/celln-node-cells-v1", "node": "old", "reportedMs": now.Add(-5 * time.Minute).UnixMilli()}
	staleRaw, _ := json.Marshal(stale)
	runs := []api.AgentRun{{
		ObjectMeta: metav1.ObjectMeta{Name: "hermes-abc", Namespace: "default"},
		Spec:       api.AgentRunSpec{AgentRef: "hermes"},
		Status:     api.AgentRunStatus{Phase: api.AgentRunPhaseRunning, CellnParent: &api.CellnParentStatus{Binding: api.CellnParentBinding{Incarnation: "blake3:parent"}}},
	}}

	nodes := joinCellnCells(map[string]string{"framework": string(raw), "old": string(staleRaw), "broken": "{"}, runs, now)
	if len(nodes) != 3 || nodes[0].Node != "broken" || nodes[0].Error == "" || nodes[1].Node != "framework" || nodes[2].Node != "old" || !nodes[2].Stale {
		t.Fatalf("nodes not decoded, sorted and flagged: %+v", nodes)
	}
	fw := nodes[1]
	if fw.Stale || len(fw.Cells) != 2 || len(fw.Parents) != 1 {
		t.Fatalf("framework report: %+v", fw)
	}
	cell := fw.Cells[0]
	if cell.Run == nil || cell.Run.Name != "hermes-abc" || cell.Run.Agent != "hermes" || !cell.Run.Live || cell.Parent != "blake3:parent" || cell.Turn != "initial" {
		t.Fatalf("turn cell not attributed to its run: %+v %+v", cell, cell.Run)
	}
	if fw.Cells[1].Run != nil || fw.Parents[0].Run == nil || fw.Parents[0].Run.Phase != "Running" {
		t.Fatalf("unrelated cell attributed or parent not joined: %+v %+v", fw.Cells[1], fw.Parents[0])
	}
}

// Mediated (scoped) runs record their parent incarnation and their cells in
// cellnScoped status, on the run for its first turn and on each AgentRunTurn
// for continuations; their cells are attributed to the run like any other.
func TestScopedCellsAreAttributedToTheirRunAndTurn(t *testing.T) {
	now := time.UnixMilli(1_000_000_000)
	followChild := "blake3:aa11bb22cc33dd44ee55ff66778899aabbccddeeff00112233445566778899aa"
	report := map[string]any{
		"apiVersion": "sympozium.ai/celln-node-cells-v1", "node": "node-b", "reportedMs": now.UnixMilli() - 1000,
		"cells": []map[string]any{
			{"id": "first-cell-01", "description": "blake3:0000…", "status": "dissolved", "backend": "kvm"},
			{"id": "unknown-cell", "description": "blake3:aa11bb22cc33dd44ee…", "status": "running", "backend": "kvm"},
			{"id": "stranger", "description": "blake3:9999…", "status": "running", "backend": "kvm"},
		},
		"parents": []map[string]any{{"incarnation": "blake3:scoped-parent", "updatedMs": 1}},
	}
	raw, _ := json.Marshal(report)
	nodes := decodeCellnNodeReports(map[string]string{"node-b": string(raw)}, now)
	runs := []api.AgentRun{{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-a-x7k2p", Namespace: "team-a", UID: "run-uid"},
		Spec:       api.AgentRunSpec{AgentRef: "agent-a"},
		Status: api.AgentRunStatus{Phase: api.AgentRunPhaseRunning, CellnScoped: &api.CellnScopedStatus{
			ParentIncarnation: "blake3:scoped-parent", TurnID: "initial", CellID: "first-cell-01", ChildID: "blake3:0000",
		}},
	}}
	turns := []api.AgentRunTurn{
		{ObjectMeta: metav1.ObjectMeta{Name: "recall", Namespace: "team-a"}, Spec: api.AgentRunTurnSpec{RunUID: "run-uid"},
			Status: api.AgentRunTurnStatus{ParentIncarnation: "blake3:scoped-parent", CellnScoped: &api.CellnScopedStatus{TurnID: "recall", ChildID: followChild}}},
		// A turn in another namespace claiming the run's UID is ignored.
		{ObjectMeta: metav1.ObjectMeta{Name: "spoof", Namespace: "team-b"}, Spec: api.AgentRunTurnSpec{RunUID: "run-uid"},
			Status: api.AgentRunTurnStatus{CellnScoped: &api.CellnScopedStatus{TurnID: "spoof", CellID: "stranger"}}},
	}
	out := attributeCellnCells(nodes, runs, turns)[0]
	if out.Parents[0].Run == nil || out.Parents[0].Run.Name != "agent-a-x7k2p" {
		t.Fatalf("scoped parent not joined to its run: %+v", out.Parents[0])
	}
	first, follow, stranger := out.Cells[0], out.Cells[1], out.Cells[2]
	if first.Run == nil || first.Run.Agent != "agent-a" || first.Turn != "initial" || first.Parent != "blake3:scoped-parent" {
		t.Fatalf("first cell (by cell id): %+v %+v", first, first.Run)
	}
	if follow.Run == nil || follow.Run.Name != "agent-a-x7k2p" || follow.Turn != "recall" {
		t.Fatalf("continuation cell (by child hash): %+v %+v", follow, follow.Run)
	}
	if stranger.Run != nil {
		t.Fatalf("a turn from another namespace attributed a cell: %+v", stranger.Run)
	}
}
