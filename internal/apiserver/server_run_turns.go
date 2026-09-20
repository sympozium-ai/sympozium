package apiserver

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	scopedCondition := meta.FindStatusCondition(run.Status.Conditions, "CellnScopedExecution")
	scopedReady := run.Status.CellnScoped != nil && run.Status.CellnScoped.StartAttempted && run.Status.CellnScoped.ParentIncarnation != "" && run.Status.CellnScoped.NativePhase == "Running" && scopedCondition != nil && scopedCondition.Status == metav1.ConditionTrue && scopedCondition.Reason == "EnduringParentReady" && scopedCondition.ObservedGeneration == run.Generation
	if run.Status.CellnParent != nil || run.Status.CellnScoped == nil {
		// A native parent: cellnparent.TurnReadiness is the one rule, shared
		// with the controller's slot claim. This rejects known state, not
		// simultaneous admission races; the controller's parent-status CAS
		// remains the authoritative serializer. Original-request observation
		// above stays available whatever this decides.
		if err := cellnparent.TurnReadiness(run, nil, time.Now().UTC()); err != nil {
			var refusal *cellnparent.TurnRefusal
			if errors.As(err, &refusal) {
				w.Header().Set("X-Sympozium-Turn-Refusal", refusal.Reason)
			}
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	} else {
		if run.Status.Phase != api.AgentRunPhaseRunning || !scopedReady {
			http.Error(w, "scoped parent is not ready for subsequent turns", http.StatusConflict)
			return
		}
		var prior api.AgentRunTurnList
		if err := s.client.List(r.Context(), &prior, client.InNamespace(run.Namespace), client.Limit(1024)); err != nil {
			http.Error(w, "turn history unavailable", http.StatusServiceUnavailable)
			return
		}
		count := int32(0)
		for i := range prior.Items {
			if prior.Items[i].Spec.RunUID == string(run.UID) && prior.Items[i].Spec.RunName == run.Name {
				count++
			}
		}
		if count >= run.Spec.Enduring.MaxTurns-1 {
			http.Error(w, "parent turn allowance is exhausted", http.StatusConflict)
			return
		}
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
	scoped := run.Status.CellnScoped != nil && turn.Status.CellnScoped != nil && turn.Status.ParentIncarnation == run.Status.CellnScoped.ParentIncarnation && turn.Status.CellnScoped.ParentIncarnation == run.Status.CellnScoped.ParentIncarnation && turn.Status.CellnScoped.TurnID == string(turn.UID)
	if !scoped {
		if _, err := cellnparent.BindTurn(run, &turn); err != nil {
			http.Error(w, "turn does not bind the original parent", http.StatusConflict)
			return
		}
	}
	if (turn.Status.Execution == nil || !turn.Status.Execution.Attempted) && (turn.Status.CellnScoped == nil || !turn.Status.CellnScoped.StartAttempted) {
		http.Error(w, "turn has no recorded dispatch attempt", http.StatusConflict)
		return
	}
	if turn.Spec.CancelRequested || (turn.Status.Execution != nil && turn.Status.Execution.Result != nil) || (turn.Status.CellnScoped != nil && turn.Status.CellnScoped.CleanupConfirmed) {
		writeJSON(w, turn)
		return
	}
	if !scoped {
		active := run.Status.CellnParent.ActiveTurn
		if active == nil || active.Name != turn.Name || active.UID != string(turn.UID) {
			http.Error(w, "turn no longer owns the parent slot", http.StatusConflict)
			return
		}
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

// continueRun restarts an enduring conversation in a new run: the transcript
// recorded so far becomes the new run's seed, the platform places its parent
// on any node with capacity, and the previous run is deleted (pass keep=true
// to leave it). The caller must name the run's observed UID so a reused name
// cannot be retargeted.
func (s *Server) continueRun(w http.ResponseWriter, r *http.Request) {
	run := s.turnParent(w, r)
	if run == nil {
		return
	}
	uid := r.URL.Query().Get("uid")
	if uid == "" || uid != string(run.UID) {
		http.Error(w, "the run's observed uid is required", http.StatusBadRequest)
		return
	}
	if run.Spec.ExecutionLifecycle != "enduring" || run.DeletionTimestamp != nil {
		http.Error(w, "only a live enduring conversation can be continued", http.StatusBadRequest)
		return
	}
	budget := cellnparent.SeedBudgetFor(r.Context(), s.client, run)
	seed, err := cellnparent.Transcript(r.Context(), s.client, run, budget)
	if err != nil {
		http.Error(w, "turn history unavailable", http.StatusServiceUnavailable)
		return
	}
	next, err := cellnparent.Continuation(run, seed, cellnparent.ContinuationOriginRequested, budget)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.client.Create(r.Context(), next); err != nil {
		http.Error(w, "could not create the continuation: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	if r.URL.Query().Get("keep") != "true" {
		previous := types.UID(run.UID)
		if err := s.client.Delete(r.Context(), run, client.Preconditions{UID: &previous}); err != nil && !apierrors.IsNotFound(err) {
			// The continuation exists and carries the transcript; report the
			// leftover rather than failing the restart.
			next.Annotations["sympozium.ai/previous-run-retained"] = err.Error()
		}
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, next)
}
