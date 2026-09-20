package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"k8s.io/apimachinery/pkg/types"
)

// Model backends of the Celln fleet, readable and extendable through the API.
// Adding one publishes its key, appends it to the fleet's extra list, rolls
// the configure DaemonSet so every node configures it from the admitted
// package, and, once the nodes have published its configuration, installs
// its profile, policy route and wrappers. Owners and their conversations
// are untouched, exactly as with the installer's --celln-fleet-backend.

// CellnFleetBackend describes one backend and how far an added one got.
type CellnFleetBackend struct {
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Protocol      string `json:"protocol"`
	Endpoint      string `json:"endpoint"`
	Model         string `json:"model"`
	AllowInsecure bool   `json:"allowInsecure"`
	// Parameters are what the Celln host merges into every provider request
	// of this backend; absent when it has none.
	Parameters map[string]any `json:"parameters,omitempty"`
	// MaxOutputTokens is the backend's output cap per model request; absent
	// when it is Celln's default (512). A turn reserves 6 requests of it.
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
	// Warning is set on the answer to an add when the new backend's turns
	// cost more than the scope's ceilings were sized for, so conversations on
	// it get fewer turns than the usual session.
	Warning string `json:"warning,omitempty"`
	// Source is install (the chart's list) or added (through this API).
	Source string `json:"source"`
	// Profile is the runtime profile every namespace's wrapper binds to.
	Profile string `json:"profile"`
	// State is ready when the profile exists, otherwise the added backend's
	// progress: pending…, configuring…, or error: ….
	State string `json:"state"`
}

// AddCellnFleetBackendRequest is what a client sends to add a backend.
type AddCellnFleetBackendRequest struct {
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Model         string `json:"model,omitempty"`
	Endpoint      string `json:"endpoint,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	AllowInsecure bool   `json:"allowInsecure,omitempty"`
	// Credential is the provider key, published once as the backend's entry
	// in the fleet's credential Secret and never returned.
	Credential string `json:"credential,omitempty"`
	// SkipPreflight skips the one-token probe, for endpoints only the nodes
	// can reach.
	SkipPreflight bool `json:"skipPreflight,omitempty"`
	// Parameters is an optional JSON object the Celln host merges into every
	// provider request of this backend (cellninstall.ValidateModelParameters).
	// They need a Celln newer than v0.5.22 on the nodes and cannot change once
	// the backend exists.
	Parameters map[string]any `json:"parameters,omitempty"`
	// MaxOutputTokens is the most output tokens one model request of this
	// backend may produce (cellninstall.ValidateModelMaxOutputTokens: 256–4096,
	// absent or 0 for Celln's default 512). A turn reserves 6 requests of it
	// from the scope's ceilings. A non-default value needs a Celln newer than
	// v0.5.23 on the nodes and a starter package built by it, and cannot
	// change once the backend exists.
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
}

const extraBackendCompletionTimeout = 25 * time.Minute

func (s *Server) listCellnFleetBackends(w http.ResponseWriter, r *http.Request) {
	facts, err := cellninstall.ReadFleetFacts(r.Context(), s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	extra, states, err := cellninstall.ReadExtraBackends(r.Context(), s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	var profiles api.CellnRuntimeProfileList
	if err := s.client.List(r.Context(), &profiles); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	present := map[string]bool{}
	for _, p := range profiles.Items {
		present[p.Name] = true
	}
	out := make([]CellnFleetBackend, 0, len(facts.InstallBackends)+len(extra))
	for _, b := range facts.InstallBackends {
		profile := cellninstall.PlatformProfileName(facts.Scope, b.Name)
		state := "pending: waiting for the nodes to configure it"
		if present[profile] {
			state = "ready"
		}
		out = append(out, CellnFleetBackend{Name: b.Name, Provider: b.Provider, Protocol: b.Protocol, Endpoint: b.Endpoint, Model: b.Model, AllowInsecure: b.AllowInsecure, Parameters: b.Parameters, MaxOutputTokens: b.MaxOutputTokens, Source: "install", Profile: profile, State: state})
	}
	for _, b := range extra {
		profile := cellninstall.PlatformProfileName(facts.Scope, b.Name)
		state := states[b.Name]
		if present[profile] {
			state = "ready"
		} else if state == "" {
			state = "pending: waiting for the nodes to configure it"
		}
		out = append(out, CellnFleetBackend{Name: b.Name, Provider: b.Provider, Protocol: b.Protocol, Endpoint: b.Endpoint, Model: b.Model, AllowInsecure: b.AllowInsecure, Parameters: b.Parameters, MaxOutputTokens: b.MaxOutputTokens, Source: "added", Profile: profile, State: state})
	}
	writeJSON(w, out)
}

func (s *Server) addCellnFleetBackend(w http.ResponseWriter, r *http.Request) {
	var req AddCellnFleetBackendRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16384))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Provider) == "" {
		http.Error(w, "name and provider are required", http.StatusBadRequest)
		return
	}
	facts, err := cellninstall.ReadFleetFacts(r.Context(), s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	// Checked before anything is probed or published, with the precise rule.
	if err := cellninstall.ValidateModelParameters(req.Parameters); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := cellninstall.ValidateModelMaxOutputTokens(req.MaxOutputTokens); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The scope's one policy was sized at install. A backend whose single
	// turn it cannot pay for would be refused by every node; one it pays for
	// fewer turns of than the usual session is added with a warning.
	warning := ""
	if ceilings, ok := s.cellnFleetCeilings(r.Context(), facts.Scope); ok {
		if turn := cellninstall.TurnOutputTokensFor(req.MaxOutputTokens); ceilings.MaxOutputTokens < turn {
			http.Error(w, fmt.Sprintf("max output tokens %d per request makes one turn reserve %d output tokens (6 requests), more than the %d this fleet allows one conversation; choose at most %d, or raise the fleet's ceilings ('sympozium doctor' shows how)", cellninstall.EffectiveModelMaxOutputTokens(req.MaxOutputTokens), turn, ceilings.MaxOutputTokens, ceilings.MaxOutputTokens/api.TurnModelRequests), http.StatusBadRequest)
			return
		}
		warning = cellninstall.FewerSessionTurnsWarning(req.Name, req.MaxOutputTokens, ceilings)
	}
	model := cellninstall.FleetModel{Provider: strings.TrimSpace(req.Provider), Protocol: req.Protocol, Endpoint: strings.TrimSpace(req.Endpoint), Name: strings.TrimSpace(req.Model), AllowInsecure: req.AllowInsecure, Parameters: req.Parameters, MaxOutputTokens: req.MaxOutputTokens}
	// An OpenAI-compatible server given only by its address names its own
	// model; a local llama-server serves exactly one.
	if model.Name == "" && model.Endpoint != "" && (model.Protocol == "" || model.Protocol == "openai-chat") {
		if req.SkipPreflight {
			http.Error(w, "give the model name when the probe is skipped", http.StatusBadRequest)
			return
		}
		detected, err := cellninstall.DetectModel(r.Context(), nil, cellninstall.CompleteModelEndpoint("openai-chat", model.Endpoint), strings.TrimSpace(req.Credential))
		if err != nil {
			http.Error(w, "model detection failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		model.Name = detected
	}
	resolved, err := model.Resolve(cellninstall.CredentialProfileFor(facts.Scope, req.Name))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	backend := cellninstall.FleetBackend{Name: req.Name, Model: resolved}
	if resolved.NeedsCredential() && strings.TrimSpace(req.Credential) == "" {
		http.Error(w, "provider "+resolved.Provider+" needs a credential", http.StatusBadRequest)
		return
	}
	if !req.SkipPreflight {
		if err := cellninstall.PreflightBackend(r.Context(), nil, backend, strings.TrimSpace(req.Credential)); err != nil {
			http.Error(w, "backend probe failed: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := cellninstall.PublishFleetBackendCredentialValue(r.Context(), s.client, req.Name, resolved, req.Credential); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	revision, err := cellninstall.AppendExtraBackend(r.Context(), s.client, facts, cellninstall.ExtraBackendFor(backend))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := cellninstall.RolloutConfigure(r.Context(), s.client, revision); err != nil {
		http.Error(w, "backend recorded but the configure rollout failed: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	profile := cellninstall.PlatformProfileName(facts.Scope, req.Name)
	complete := s.completeCellnBackend
	if complete == nil {
		complete = s.completeCellnFleetBackend
	}
	go complete(req.Name, facts, cellninstall.HintForBackendModel(len(resolved.Parameters) != 0, resolved.MaxOutputTokens))
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, CellnFleetBackend{Name: req.Name, Provider: resolved.Provider, Protocol: resolved.Protocol, Endpoint: resolved.Endpoint, Model: resolved.Name, AllowInsecure: resolved.AllowInsecure, Parameters: resolved.Parameters, MaxOutputTokens: resolved.MaxOutputTokens, Warning: warning, Source: "added", Profile: profile, State: "pending: waiting for the nodes to configure it"})
}

// cellnFleetCeilings reads the ceilings of the scope's policy; false when the
// policy is not there (yet), which leaves the judgement to the nodes.
func (s *Server) cellnFleetCeilings(ctx context.Context, scope string) (api.CellnExecutionPolicyCeilings, bool) {
	_, policyName, _ := cellninstall.PlatformCatalogueNames(scope)
	var policy api.CellnExecutionPolicy
	if err := s.client.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		return api.CellnExecutionPolicyCeilings{}, false
	}
	return policy.Spec.Ceilings, true
}

// completeCellnFleetBackend waits for the nodes to publish the added
// backend's configuration, then installs its profile, route and wrappers
// the way the installer does, recording progress on the extra list. cellnHint
// is the Celln release the backend's settings need, named if the nodes never
// publish it.
func (s *Server) completeCellnFleetBackend(name string, facts cellninstall.FleetFacts, cellnHint string) {
	ctx, cancel := context.WithTimeout(context.Background(), extraBackendCompletionTimeout)
	defer cancel()
	record := func(state string) {
		if err := cellninstall.RecordExtraBackendState(ctx, s.client, name, state); err != nil {
			slog.Warn("celln.backend.state", "backend", name, "state", state, "error", err)
		}
	}
	fail := func(err error) {
		slog.Warn("celln.backend.add-failed", "backend", name, "error", err)
		record("error: " + err.Error())
	}
	expected := append([]string{}, facts.Backends...)
	extra, _, err := cellninstall.ReadExtraBackends(ctx, s.client)
	if err != nil {
		fail(err)
		return
	}
	for _, b := range extra {
		expected = append(expected, b.Name)
	}
	dir, err := os.MkdirTemp("", "celln-backend-")
	if err != nil {
		fail(err)
		return
	}
	defer os.RemoveAll(dir)
	configuration := filepath.Join(dir, "configuration")
	for {
		published, err := cellninstall.PublishedBackendNames(ctx, s.client)
		if err != nil {
			slog.Warn("celln.backend.publication", "backend", name, "error", err)
		}
		done := false
		for _, p := range published {
			done = done || p == name
		}
		if done {
			break
		}
		select {
		case <-ctx.Done():
			hint := ""
			if cellnHint != "" {
				hint = "; " + cellnHint
			}
			fail(fmt.Errorf("the nodes did not publish backend %s within %s; check the celln-node-configure logs in celln-system%s", name, extraBackendCompletionTimeout, hint))
			return
		case <-time.After(10 * time.Second):
		}
	}
	record("configuring: nodes published it; reading the fleet configuration")
	if ok, err := cellninstall.ReadFleetConfigurationFor(ctx, s.client, configuration, facts.PackageHash, expected); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("published configuration incomplete")
		}
		fail(err)
		return
	}
	// The running owners' mount of the credential Secret follows the kubelet
	// sync period; give it that before offering the backend. The state says so:
	// a silent wait looks stuck. Clients key on the "configuring" prefix only.
	record(fmt.Sprintf("configuring: waiting %.0fs for the credential to reach running dispatchers", cellninstall.FleetCredentialPropagationGrace.Seconds()))
	select {
	case <-ctx.Done():
		fail(ctx.Err())
		return
	case <-time.After(cellninstall.FleetCredentialPropagationGrace):
	}
	record("configuring: installing its profile and wrappers")
	// The options come from the cluster's own record, the operator's mediation
	// declaration included, so an added backend neither drops nor forgets it.
	options, err := cellninstall.PlatformOptionsFromCluster(ctx, s.client, facts, configuration, filepath.Join(dir, "installation"), systemNamespace)
	if err != nil {
		fail(err)
		return
	}
	if err := cellninstall.InstallPlatform(ctx, s.client, options); err != nil {
		fail(err)
		return
	}
	record("ready")
	slog.Info("celln.backend.added", "backend", name, "scope", facts.Scope, "namespace", options.Namespace)
}
