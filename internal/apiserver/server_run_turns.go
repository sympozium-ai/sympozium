package apiserver

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *Server) turnParent(w http.ResponseWriter, r *http.Request) *api.AgentRun {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	var run api.AgentRun
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: r.PathValue("name")}, &run); err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, "run not found", http.StatusNotFound)
		} else {
			http.Error(w, "run storage unavailable", http.StatusServiceUnavailable)
		}
		return nil
	}
	return &run
}

func (s *Server) createRunTurn(w http.ResponseWriter, r *http.Request) {
	run := s.turnParent(w, r)
	if run == nil {
		return
	}
	var input struct {
		RequestID string `json:"requestId"`
		RunUID    string `json:"runUID"`
		Message   string `json:"message"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.RequestID == "" || len(input.RequestID) > 64 || input.RunUID != string(run.UID) {
		http.Error(w, "invalid turn input or stale run identity", 400)
		return
	}
	for _, c := range []byte(input.RequestID) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			http.Error(w, "invalid request ID", 400)
			return
		}
	}
	wire, _ := json.Marshal([]string{input.RunUID, input.RequestID})
	name := fmt.Sprintf("turn-%x", sha256.Sum256(wire))
	turn, err := cellnparent.NewTurn(run, name, input.Message)
	if err != nil {
		http.Error(w, "turn requires bounded text and an enduring run", 400)
		return
	}
	// A repeated request may observe its original record even if the run later
	// stopped. It must not create a new record against an inactive parent.
	var existing api.AgentRunTurn
	key := client.ObjectKeyFromObject(turn)
	if err := s.client.Get(r.Context(), key, &existing); err == nil {
		if existing.Spec.RunName != turn.Spec.RunName || existing.Spec.RunUID != turn.Spec.RunUID || existing.Spec.Message != turn.Spec.Message || existing.DeletionTimestamp != nil {
			http.Error(w, "turn identity conflict; reconcile original request", 409)
			return
		}
		writeJSON(w, existing)
		return
	} else if !apierrors.IsNotFound(err) {
		http.Error(w, "turn storage unavailable", 503)
		return
	}
	// This rejects a known occupied slot, not simultaneous admission races.
	// The controller's parent-status CAS remains the authoritative serializer.
	// Original-request observation above stays available while another turn runs.
	if run.Status.CellnParent != nil && run.Status.CellnParent.ActiveTurn != nil {
		http.Error(w, "parent already owns an active turn; reconcile it before new work", http.StatusConflict)
		return
	}
	if run.Status.Phase != api.AgentRunPhaseRunning || run.Status.CellnParent == nil || !run.Status.CellnParent.CreateAttempted || run.Status.CellnParent.InitialTurn == nil || run.Status.CellnParent.InitialTurn.Result == nil || !run.Status.CellnParent.InitialTurn.Result.Succeeded || run.Status.CellnParent.AcceptedTurns >= run.Spec.Enduring.MaxTurns-1 {
		http.Error(w, "parent is not ready for subsequent turns", 409)
		return
	}
	ready := meta.FindStatusCondition(run.Status.Conditions, "CellnParentReady")
	if ready == nil || ready.Status != "True" || run.Generation < 1 || ready.ObservedGeneration != run.Generation || cellnparent.ValidateAdmission(run, run.Status.CellnParent.Binding) != nil {
		http.Error(w, "parent readiness does not match current run intent", http.StatusConflict)
		return
	}
	if err := s.client.Create(r.Context(), turn); err != nil {
		http.Error(w, "turn creation uncertain; query the original request", 409)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, turn)
}

// cancelRunTurn records intent only. The controller owns delivery and durable
// outcome reconciliation; neither this API nor the caller selects a new owner.
func (s *Server) cancelRunTurn(w http.ResponseWriter, r *http.Request) {
	run := s.turnParent(w, r)
	if run == nil {
		return
	}
	var input struct {
		RunUID  string `json:"runUID"`
		TurnUID string `json:"turnUID"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.RunUID == "" || input.RunUID != string(run.UID) || input.TurnUID == "" || len(input.TurnUID) > 128 {
		http.Error(w, "invalid cancellation input or stale run identity", http.StatusBadRequest)
		return
	}
	var turn api.AgentRunTurn
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: run.Namespace, Name: r.PathValue("turn")}, &turn); err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, "turn not found", http.StatusNotFound)
		} else {
			http.Error(w, "turn storage unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	if string(turn.UID) != input.TurnUID {
		http.Error(w, "stale turn identity", http.StatusConflict)
		return
	}
	if _, err := cellnparent.BindTurn(run, &turn); err != nil {
		http.Error(w, "turn does not bind the original parent", http.StatusConflict)
		return
	}
	if turn.Status.Execution == nil || !turn.Status.Execution.Attempted {
		http.Error(w, "turn has no recorded dispatch attempt", http.StatusConflict)
		return
	}
	if turn.Spec.CancelRequested || turn.Status.Execution.Result != nil {
		writeJSON(w, turn)
		return
	}
	active := run.Status.CellnParent.ActiveTurn
	if active == nil || active.Name != turn.Name || active.UID != string(turn.UID) {
		http.Error(w, "turn no longer owns the parent slot", http.StatusConflict)
		return
	}
	turn.Spec.CancelRequested = true
	// ResourceVersion guards a concurrent replacement, completion or request.
	if err := s.client.Update(r.Context(), &turn); err != nil {
		http.Error(w, "cancellation persistence uncertain; inspect the original turn", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, turn)
}

func (s *Server) listRunTurns(w http.ResponseWriter, r *http.Request) {
	run := s.turnParent(w, r)
	if run == nil {
		return
	}
	var list api.AgentRunTurnList
	// Bound each response; pagination is namespace-scoped and filtered by immutable
	// parent UID rather than trusting mutable labels or a reused run name.
	if err := s.client.List(r.Context(), &list, client.InNamespace(run.Namespace), client.Limit(256), client.Continue(r.URL.Query().Get("continue"))); err != nil {
		http.Error(w, "turn history unavailable", 503)
		return
	}
	items := make([]api.AgentRunTurn, 0)
	for _, turn := range list.Items {
		if turn.Spec.RunName == run.Name && turn.Spec.RunUID == string(run.UID) {
			items = append(items, turn)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreationTimestamp.Equal(&items[j].CreationTimestamp) {
			return items[i].Name < items[j].Name
		}
		return items[i].CreationTimestamp.Before(&items[j].CreationTimestamp)
	})
	writeJSON(w, map[string]any{"runUID": string(run.UID), "items": items, "continue": list.Continue})
}
