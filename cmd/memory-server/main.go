// memory-server runs as a standalone Deployment per Agent.
// It provides persistent memory for Sympozium agents via an HTTP API,
// backed by SQLite with FTS5 for full-text search.
//
// The SQLite database lives on a PersistentVolume so data survives across
// ephemeral agent pod runs. Agent pods call this server over HTTP via a
// ClusterIP Service.
//
// Membrane extensions add visibility-based permeability (public/trusted/private),
// provenance tracking (source_agent, parent_id), event sequencing, and time decay.
//
// Storage is append-only. Every row is one version of a memory: `id` is the
// memory's stable id, shared by all its versions, and the version with the
// highest `seq` is the current one. An update appends a version with new
// content; a forget appends a version with NULL content. Search, list and
// provenance only see current versions with content, and the FTS index holds
// exactly those rows. Every version stays readable through the admin-only
// GET /history endpoint.
package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sympozium-ai/sympozium/pkg/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"
	_ "modernc.org/sqlite"
)

// defaultDBPath is the configured database path (MEMORY_DB_PATH). It names
// the database of older releases; the server works on versionedDBPath of it.
const defaultDBPath = "/data/memory.db"

// memObservability holds the OTel instruments for the memory sidecar. Agents
// in an Ensemble read and write a shared memory store, but that interaction was
// previously unmeasurable (ISI-1406 gap 6). These counters expose read/write
// volume per agent and operation so memory traffic is visible in Dynatrace.
type memObservability struct {
	enabled  bool
	shutdown func(context.Context) error
	reads    metric.Int64Counter
	writes   metric.Int64Counter
}

var memObs = &memObservability{shutdown: func(context.Context) error { return nil }}

// agentName is the owning agent's name, stamped on every memory metric so a
// shared-memory Ensemble can be broken down per agent.
var agentName = envOr("MEMORY_AGENT", "")

// initMemObservability bootstraps OTel via the shared pkg/telemetry path when an
// OTLP endpoint is configured. It is a no-op (counters stay nil) otherwise, so
// the sidecar runs unchanged in environments without observability.
func initMemObservability(ctx context.Context) {
	endpoint := firstNonEmptyEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "SYMPOZIUM_OTEL_OTLP_ENDPOINT")
	if endpoint == "" {
		return
	}
	serviceName := firstNonEmptyEnv("OTEL_SERVICE_NAME", "SYMPOZIUM_OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "sympozium-memory-server"
	}
	tel, err := telemetry.Init(ctx, telemetry.Config{
		ServiceName:     serviceName,
		BatchTimeout:    1 * time.Second,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		log.Printf("[memory-server] OTel init failed, metrics disabled: %v", err)
		return
	}
	meter := otel.Meter("sympozium.ai/memory-server")
	reads, err := meter.Int64Counter("sympozium.memory.read",
		metric.WithUnit("{op}"), metric.WithDescription("Memory read operations (search/list/provenance)"))
	if err != nil {
		log.Printf("[memory-server] failed creating sympozium.memory.read: %v", err)
	}
	writes, err := meter.Int64Counter("sympozium.memory.write",
		metric.WithUnit("{op}"), metric.WithDescription("Memory write operations (store)"))
	if err != nil {
		log.Printf("[memory-server] failed creating sympozium.memory.write: %v", err)
	}
	memObs = &memObservability{enabled: true, shutdown: tel.Shutdown, reads: reads, writes: writes}
	log.Printf("[memory-server] OTel metrics enabled, endpoint=%s service=%s", endpoint, serviceName)
}

// recordRead increments the read counter. op is the read kind (search/list/...),
// status is "ok" or "error". caller is the requesting agent when provided.
func (o *memObservability) recordRead(ctx context.Context, op, status, caller string) {
	if o == nil || !o.enabled || o.reads == nil {
		return
	}
	o.reads.Add(ctx, 1, metric.WithAttributes(
		attribute.String("op", op),
		attribute.String("status", status),
		attribute.String("agent", agentName),
	))
	// caller_agent is caller-controlled (read from the request body/query on a
	// plain HTTP endpoint), so it stays off the bounded counter and goes on the
	// span instead, where high cardinality is acceptable.
	if caller != "" {
		oteltrace.SpanFromContext(ctx).SetAttributes(attribute.String("caller_agent", caller))
	}
}

// recordWrite increments the write counter. status is "ok" or "error", source
// is the agent that produced the entry when provided.
func (o *memObservability) recordWrite(ctx context.Context, status, source string) {
	if o == nil || !o.enabled || o.writes == nil {
		return
	}
	o.writes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("status", status),
		attribute.String("agent", agentName),
	))
	// source_agent is caller-controlled; keep it on the span, not the counter.
	if source != "" {
		oteltrace.SpanFromContext(ctx).SetAttributes(attribute.String("source_agent", source))
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// apiResponse is the standard JSON response format.
type apiResponse struct {
	Success bool   `json:"success"`
	Content any    `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

// memoryEntry represents a stored memory.
type memoryEntry struct {
	ID          int64          `json:"id"`
	Content     string         `json:"content"`
	Forgotten   bool           `json:"forgotten,omitempty"` // content is NULL; only seen in /history
	Tags        []string       `json:"tags,omitempty"`
	Visibility  string         `json:"visibility,omitempty"`
	SourceAgent string         `json:"source_agent,omitempty"`
	ParentID    int64          `json:"parent_id,omitempty"`
	Seq         int64          `json:"seq,omitempty"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
	Evidence    *EvidenceTrace `json:"evidence,omitempty"`
}

// EvidenceTrace captures how a memory entry was derived, enabling
// quality-based filtering and provenance auditing.
type EvidenceTrace struct {
	Kind        string  `json:"kind"`                   // tool_result, external_source, llm_interpretation, agent_opinion
	ToolCall    string  `json:"tool_call,omitempty"`    // tool name + args that produced this
	RawResult   string  `json:"raw_result,omitempty"`   // unmodified tool output (truncated)
	Source      string  `json:"source,omitempty"`       // URL, doc ref, or upstream entry ID
	Confidence  float64 `json:"confidence,omitempty"`   // 0.0-1.0, set by producing agent
	DerivedFrom []int64 `json:"derived_from,omitempty"` // entry IDs this was derived from
}

func main() {
	dbPath := envOr("MEMORY_DB_PATH", defaultDBPath)
	port := envOr("MEMORY_PORT", "8080")

	// Admin-only endpoints (DELETE /delete, GET /history) are gated on a
	// bearer token supplied via a Secret and injected only into this pod's env
	// (never into agent pods). When unset, both are disabled, so existing
	// deployments are unaffected.
	adminToken := os.Getenv("MEMORY_ADMIN_TOKEN")

	// Writes (POST /store, /update, /forget) are gated on a separate writer
	// token. The controller injects it into this pod and into the agent-runner
	// container only, never into skill sidecars, so a model running commands in
	// a sidecar cannot write or forget entries under another agent's name.
	// When unset (memory-server run outside the controller), writes are open.
	writerToken := os.Getenv("MEMORY_WRITER_TOKEN")
	if err := checkTokens(adminToken, writerToken); err != nil {
		log.Fatalf("%v", err)
	}
	if writerToken == "" {
		log.Printf("[memory-server] MEMORY_WRITER_TOKEN not set: writes are not authenticated")
	}

	// Ensure database directory exists.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("failed to create db directory: %v", err)
	}

	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("%v", err)
	}
	defer db.Close()

	// Bootstrap OTel (ISI-1406 gap 6). No-op when no OTLP endpoint is set.
	initMemObservability(context.Background())
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = memObs.shutdown(shutdownCtx)
	}()

	mux := newMux(db, adminToken, writerToken)

	// Wrap the router with otelhttp so each memory operation emits a server
	// span (ISI-1406 gap 6 — "spans on the memory part"). The handler extracts
	// the W3C traceparent from the incoming request (the agent-runner injects it
	// via its otelhttp client), so memory reads/writes nest under the agent's
	// run trace and the full BMAD chain instead of appearing as orphans. Spans
	// are named by route (e.g. "memory POST /store"); /health is left
	// uninstrumented to avoid probe noise. No-op when OTel is disabled — the
	// global TracerProvider is then a noop and spans are dropped cheaply.
	// Cap request bodies so a single caller cannot exhaust the backing
	// PersistentVolume (or the process's memory) with an oversized /store
	// payload. 4 MiB comfortably exceeds any legitimate memory entry.
	const maxBodyBytes = 4 << 20
	bodyLimited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		mux.ServeHTTP(w, r)
	})

	handler := otelhttp.NewHandler(bodyLimited, "memory-server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return "memory " + r.Method + " " + r.URL.Path
		}),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/health"
		}),
	)

	addr := ":" + port
	log.Printf("[memory-server] listening on %s, db=%s", addr, dbPath)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func searchHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query       string   `json:"query"`
			TopK        int      `json:"top_k"`
			CallerAgent string   `json:"caller_agent"`
			TrustPeers  []string `json:"trust_peers"`
			AcceptTags  []string `json:"accept_tags"`
			MaxAge      string   `json:"max_age"`
			MinKind     string   `json:"min_kind"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[search] bad request: %v", err)
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
			return
		}
		if req.Query == "" {
			log.Printf("[search] rejected: empty query")
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'query' is required"})
			return
		}
		if req.TopK <= 0 {
			req.TopK = 5
		}

		log.Printf("[search] query=%q top_k=%d caller=%s", truncateLog(req.Query, 120), req.TopK, req.CallerAgent)
		results, err := searchMemories(db, req.Query, req.TopK, req.CallerAgent, req.TrustPeers, req.AcceptTags, req.MaxAge, req.MinKind)
		if err != nil {
			log.Printf("[search] error: %v", err)
			memObs.recordRead(r.Context(), "search", "error", req.CallerAgent)
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		log.Printf("[search] returned %d result(s)", len(results))
		memObs.recordRead(r.Context(), "search", "ok", req.CallerAgent)
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Content: results})
	}
}

func storeHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Content     string         `json:"content"`
			Tags        []string       `json:"tags"`
			Visibility  string         `json:"visibility"`
			SourceAgent string         `json:"source_agent"`
			ParentID    int64          `json:"parent_id"`
			Evidence    *EvidenceTrace `json:"evidence"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[store] bad request: %v", err)
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
			return
		}
		if req.Content == "" {
			log.Printf("[store] rejected: empty content")
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'content' is required"})
			return
		}
		if req.Visibility == "" {
			req.Visibility = "public"
		}

		log.Printf("[store] content=%d bytes tags=%v visibility=%s source=%s parent=%d",
			len(req.Content), req.Tags, req.Visibility, req.SourceAgent, req.ParentID)
		id, seq, storedAt, err := storeMemory(db, req.Content, req.Tags, req.Visibility, req.SourceAgent, req.ParentID, req.Evidence)
		if err != nil {
			log.Printf("[store] error: %v", err)
			memObs.recordWrite(r.Context(), "error", req.SourceAgent)
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		log.Printf("[store] saved id=%d seq=%d at=%s", id, seq, storedAt)
		memObs.recordWrite(r.Context(), "ok", req.SourceAgent)
		writeJSON(w, http.StatusOK, apiResponse{
			Success: true,
			Content: map[string]any{"id": id, "seq": seq, "stored_at": storedAt},
		})
	}
}

// updateHandler replaces the content of an existing memory. It appends a new
// version with the same id; the old content drops out of search and list.
func updateHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID          int64           `json:"id"`
			Content     string          `json:"content"`
			Tags        []string        `json:"tags"`
			Visibility  string          `json:"visibility"`
			SourceAgent string          `json:"source_agent"`
			Evidence    json.RawMessage `json:"evidence"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[update] bad request: %v", err)
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
			return
		}
		evidence, clearEvidence, err := parseEvidenceUpdate(req.Evidence)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'evidence' must be an object or null"})
			return
		}
		if req.ID <= 0 {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'id' is required"})
			return
		}
		if req.Content == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'content' is required (use /forget to remove an entry)"})
			return
		}

		log.Printf("[update] id=%d content=%d bytes source=%s", req.ID, len(req.Content), req.SourceAgent)
		writeNewVersion(w, r, "update", db, versionRequest{
			ID:            req.ID,
			Content:       req.Content,
			Tags:          req.Tags,
			Visibility:    req.Visibility,
			SourceAgent:   req.SourceAgent,
			Evidence:      evidence,
			ClearEvidence: clearEvidence,
		})
	}
}

// parseEvidenceUpdate reads the evidence field of an update. An omitted field
// returns (nil, false): keep the current evidence. null or an empty object
// returns (nil, true): clear it. Anything else must be an evidence object.
func parseEvidenceUpdate(raw json.RawMessage) (*EvidenceTrace, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var ev EvidenceTrace
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, false, err
	}
	if ev.Kind == "" && ev.ToolCall == "" && ev.RawResult == "" && ev.Source == "" && ev.Confidence == 0 && len(ev.DerivedFrom) == 0 {
		return nil, true, nil
	}
	return &ev, false, nil
}

// forgetHandler removes a memory from search and list. It appends a version
// with NULL content, so the history is kept.
func forgetHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID          int64  `json:"id"`
			SourceAgent string `json:"source_agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[forget] bad request: %v", err)
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
			return
		}
		if req.ID <= 0 {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'id' is required"})
			return
		}

		log.Printf("[forget] id=%d source=%s", req.ID, req.SourceAgent)
		writeNewVersion(w, r, "forget", db, versionRequest{
			ID:            req.ID,
			SourceAgent:   req.SourceAgent,
			ClearEvidence: true,
		})
	}
}

// writeNewVersion runs appendVersion and maps its errors to HTTP statuses.
func writeNewVersion(w http.ResponseWriter, r *http.Request, op string, db *sql.DB, req versionRequest) {
	seq, storedAt, err := appendVersion(db, req)
	switch {
	case errors.Is(err, errMemoryNotFound):
		writeJSON(w, http.StatusNotFound, apiResponse{Error: fmt.Sprintf("no memory entry with id %d", req.ID)})
		return
	case errors.Is(err, errMemoryForgotten):
		writeJSON(w, http.StatusConflict, apiResponse{Error: fmt.Sprintf("memory entry %d has been forgotten; store a new entry instead", req.ID)})
		return
	case errors.Is(err, errMemoryConflict):
		writeJSON(w, http.StatusConflict, apiResponse{Error: fmt.Sprintf("memory entry %d was changed concurrently; retry", req.ID)})
		return
	case err != nil:
		log.Printf("[%s] error: %v", op, err)
		memObs.recordWrite(r.Context(), "error", req.SourceAgent)
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	log.Printf("[%s] saved id=%d seq=%d", op, req.ID, seq)
	memObs.recordWrite(r.Context(), op, req.SourceAgent)
	writeJSON(w, http.StatusOK, apiResponse{
		Success: true,
		Content: map[string]any{"id": req.ID, "seq": seq, "stored_at": storedAt},
	})
}

func listHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tags := r.URL.Query().Get("tags")
		callerAgent := r.URL.Query().Get("caller_agent")
		trustPeersStr := r.URL.Query().Get("trust_peers")
		maxAge := r.URL.Query().Get("max_age")
		minKind := r.URL.Query().Get("min_kind")
		sourceAgent := r.URL.Query().Get("source_agent")
		limit := 20
		if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}

		var trustPeers []string
		if trustPeersStr != "" {
			trustPeers = strings.Split(trustPeersStr, ",")
		}

		log.Printf("[list] tags=%q caller=%s limit=%d", tags, callerAgent, limit)
		results, err := listMemories(db, tags, limit, callerAgent, trustPeers, maxAge, minKind, sourceAgent)
		if err != nil {
			log.Printf("[list] error: %v", err)
			memObs.recordRead(r.Context(), "list", "error", callerAgent)
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		log.Printf("[list] returned %d entry/entries", len(results))
		memObs.recordRead(r.Context(), "list", "ok", callerAgent)
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Content: results})
	}
}

func statsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type agentStats struct {
			SourceAgent string `json:"source_agent"`
			Visibility  string `json:"visibility"`
			Count       int    `json:"count"`
		}
		// Count memories, not versions: only current versions with content.
		rows, err := db.Query(`
			SELECT COALESCE(m.source_agent, '') as source_agent,
			       COALESCE(m.visibility, 'public') as visibility,
			       COUNT(*) as count
			FROM memories m
			WHERE 1=1 ` + currentOnlyFilter + `
			GROUP BY m.source_agent, m.visibility
			ORDER BY count DESC
		`)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		defer rows.Close()

		var stats []agentStats
		for rows.Next() {
			var s agentStats
			if err := rows.Scan(&s.SourceAgent, &s.Visibility, &s.Count); err != nil {
				continue
			}
			stats = append(stats, s)
		}
		if stats == nil {
			stats = []agentStats{}
		}

		var maxSeq int64
		_ = db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM memories`).Scan(&maxSeq)

		writeJSON(w, http.StatusOK, apiResponse{
			Success: true,
			Content: map[string]any{
				"by_agent_visibility": stats,
				"max_seq":             maxSeq,
			},
		})
	}
}

func provenanceHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'id' query parameter is required"})
			return
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid 'id' parameter"})
			return
		}

		chain, err := getProvenanceChain(db, id)
		if err != nil {
			memObs.recordRead(r.Context(), "provenance", "error", r.URL.Query().Get("caller_agent"))
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		memObs.recordRead(r.Context(), "provenance", "ok", r.URL.Query().Get("caller_agent"))
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Content: chain})
	}
}

// authorizeAdmin checks the admin bearer token. It writes the error response
// and returns false when the request must be rejected. An empty adminToken
// disables the endpoint.
func authorizeAdmin(w http.ResponseWriter, r *http.Request, op, adminToken string) bool {
	if adminToken == "" {
		log.Printf("[%s] rejected: MEMORY_ADMIN_TOKEN not configured", op)
		writeJSON(w, http.StatusForbidden, apiResponse{Error: op + " is disabled: MEMORY_ADMIN_TOKEN is not configured"})
		return false
	}
	if !bearerMatches(r, adminToken) {
		log.Printf("[%s] rejected: invalid or missing bearer token", op)
		writeJSON(w, http.StatusUnauthorized, apiResponse{Error: "unauthorized"})
		return false
	}
	return true
}

// requireWriter wraps a write handler so it only runs for requests carrying
// the writer token. An empty writerToken leaves the handler open, for a
// memory-server run outside the controller. The admin token is not accepted
// here: each route takes exactly one token.
func requireWriter(writerToken string, next http.HandlerFunc) http.HandlerFunc {
	if writerToken == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !bearerMatches(r, writerToken) {
			log.Printf("[%s] rejected: invalid or missing writer token", r.URL.Path)
			writeJSON(w, http.StatusUnauthorized, apiResponse{Error: "unauthorized"})
			return
		}
		next(w, r)
	}
}

// bearerMatches reports whether the request's bearer token equals want, in
// constant time.
func bearerMatches(r *http.Request, want string) bool {
	token := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

// checkTokens rejects a configuration where the writer token equals the admin
// token. Agent-runner pods hold the writer token; sharing the value would let
// them call the admin-only /delete and /history endpoints.
func checkTokens(adminToken, writerToken string) error {
	if writerToken != "" && subtle.ConstantTimeCompare([]byte(writerToken), []byte(adminToken)) == 1 {
		return errors.New("MEMORY_WRITER_TOKEN must differ from MEMORY_ADMIN_TOKEN")
	}
	return nil
}

// newMux builds the memory server's routes. Writes take the writer token,
// /delete and /history take the admin token, and reads are open.
func newMux(db *sql.DB, adminToken, writerToken string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /search", searchHandler(db))
	mux.HandleFunc("POST /store", requireWriter(writerToken, storeHandler(db)))
	mux.HandleFunc("POST /update", requireWriter(writerToken, updateHandler(db)))
	mux.HandleFunc("POST /forget", requireWriter(writerToken, forgetHandler(db)))
	mux.HandleFunc("GET /list", listHandler(db))
	mux.HandleFunc("GET /stats", statsHandler(db))
	mux.HandleFunc("GET /provenance", provenanceHandler(db))
	mux.HandleFunc("DELETE /delete", deleteHandler(db, adminToken))
	mux.HandleFunc("GET /history", historyHandler(db, adminToken))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	return mux
}

// historyHandler returns every version of the memory with the given id,
// oldest first, including replaced content and forget versions. It is the
// audit view of the append-only store, so it shares the admin token with
// /delete and is never exposed to agents.
func historyHandler(db *sql.DB, adminToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAdmin(w, r, "history", adminToken) {
			return
		}
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "valid 'id' query parameter is required"})
			return
		}

		versions, err := getMemoryHistory(db, id)
		if err != nil {
			log.Printf("[history] error: %v", err)
			memObs.recordRead(r.Context(), "history", "error", "")
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		if len(versions) == 0 {
			writeJSON(w, http.StatusNotFound, apiResponse{Error: fmt.Sprintf("no memory entry with id %d", id)})
			return
		}
		memObs.recordRead(r.Context(), "history", "ok", "")
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Content: versions})
	}
}

// deleteHandler hard-deletes versions of a memory: one version with
// ?id=N&seq=S, or every version with ?id=N. It is a manual admin backstop
// (corrupted or sensitive entries) — not wired to agents or skills. Access is
// gated on a bearer token injected only into this pod's env; when the token is
// unset the endpoint is disabled, so existing deployments are unaffected. The
// memories_ad trigger keeps memories_fts in sync: deleting the current version
// makes the previous version current and searchable again.
func deleteHandler(db *sql.DB, adminToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeAdmin(w, r, "delete", adminToken) {
			return
		}

		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'id' query parameter is required"})
			return
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid 'id' parameter"})
			return
		}
		var seq int64 // 0 = all versions
		if seqStr := r.URL.Query().Get("seq"); seqStr != "" {
			if seq, err = strconv.ParseInt(seqStr, 10, 64); err != nil || seq <= 0 {
				writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid 'seq' parameter"})
				return
			}
		}

		deleted, err := deleteMemory(db, id, seq)
		if err != nil {
			log.Printf("[delete] error: %v", err)
			memObs.recordWrite(r.Context(), "error", "")
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		if len(deleted) == 0 {
			writeJSON(w, http.StatusNotFound, apiResponse{Error: fmt.Sprintf("no memory entry with id %d", id)})
			return
		}
		for _, e := range deleted {
			log.Printf("[delete] removed id=%d seq=%d source=%s", e.ID, e.Seq, e.SourceAgent)
			memObs.recordWrite(r.Context(), "delete", e.SourceAgent)
		}
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Content: map[string]any{"deleted": deleted}})
	}
}

// --- Core database operations ---

// currentOnlyFilter keeps only the current version of each memory, and only
// if it has content (not forgotten). The (id, seq) unique index serves the
// MAX lookup. Queries using it must alias memories as m.
const currentOnlyFilter = `AND m.content IS NOT NULL AND m.seq = (SELECT MAX(x.seq) FROM memories x WHERE x.id = m.id)`

// memoryColumns is the column list read by scanEntry, qualified with alias m.
const memoryColumns = `m.id, m.content, m.tags, m.visibility, m.source_agent, m.parent_id, m.seq, m.created_at, m.updated_at, m.evidence`

func searchMemories(db *sql.DB, query string, topK int, callerAgent string, trustPeers, acceptTags []string, maxAge, minKind string) ([]memoryEntry, error) {
	// Build visibility filter.
	visFilter, visArgs := buildVisibilityFilter(callerAgent, trustPeers)

	// Build time decay filter.
	ageFilter, ageArgs := buildTimeDecayFilter(maxAge)

	// Build accept tags filter.
	tagFilter, tagArgs := buildAcceptTagsFilter(acceptTags)

	// Build min kind filter.
	kindFilter, kindArgs := buildMinKindFilter(minKind)

	// FTS5 search with ranking + membrane filters.
	allArgs := []any{fts5Query(query)}
	allArgs = append(allArgs, visArgs...)
	allArgs = append(allArgs, ageArgs...)
	allArgs = append(allArgs, tagArgs...)
	allArgs = append(allArgs, kindArgs...)
	allArgs = append(allArgs, topK)

	q := fmt.Sprintf(`
		SELECT %s
		FROM memories_fts fts
		JOIN memories m ON m.version_id = fts.rowid
		WHERE memories_fts MATCH ?
		%s %s %s %s %s
		ORDER BY rank
		LIMIT ?
	`, memoryColumns, currentOnlyFilter, visFilter, ageFilter, tagFilter, kindFilter)

	rows, err := db.Query(q, allArgs...)
	if err != nil {
		// Fallback to LIKE search with same membrane filters.
		allArgs = []any{"%" + query + "%"}
		allArgs = append(allArgs, visArgs...)
		allArgs = append(allArgs, ageArgs...)
		allArgs = append(allArgs, tagArgs...)
		allArgs = append(allArgs, kindArgs...)
		allArgs = append(allArgs, topK)

		q = fmt.Sprintf(`
			SELECT %s
			FROM memories m
			WHERE content LIKE ?
			%s %s %s %s %s
			ORDER BY updated_at DESC
			LIMIT ?
		`, memoryColumns, currentOnlyFilter, visFilter, ageFilter, tagFilter, kindFilter)

		rows, err = db.Query(q, allArgs...)
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
	}
	defer rows.Close()
	return scanEntries(rows)
}

func storeMemory(db *sql.DB, content string, tags []string, visibility, sourceAgent string, parentID int64, evidence *EvidenceTrace) (int64, int64, string, error) {
	tagsStr := strings.Join(tags, ",")
	now := time.Now().UTC().Format(time.RFC3339)

	evidenceStr, err := marshalEvidence(evidence)
	if err != nil {
		return 0, 0, "", err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, 0, "", fmt.Errorf("store begin tx: %w", err)
	}
	defer tx.Rollback()

	// Allocate a new memory id. The counter never goes back, so an id freed
	// by an admin delete is never handed to a different memory.
	var id int64
	if err := tx.QueryRow(`UPDATE memory_ids SET next = next + 1 RETURNING next - 1`).Scan(&id); err != nil {
		return 0, 0, "", fmt.Errorf("store allocate id: %w", err)
	}

	// Get next monotonic sequence number.
	var nextSeq int64
	_ = tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM memories`).Scan(&nextSeq)

	_, err = tx.Exec(`
		INSERT INTO memories (id, seq, content, tags, visibility, source_agent, parent_id, created_at, updated_at, evidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, nextSeq, content, tagsStr, visibility, sourceAgent, parentID, now, now, evidenceStr)
	if err != nil {
		return 0, 0, "", fmt.Errorf("store failed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, "", fmt.Errorf("store commit: %w", err)
	}
	return id, nextSeq, now, nil
}

func marshalEvidence(evidence *EvidenceTrace) (string, error) {
	if evidence == nil {
		return "", nil
	}
	b, err := json.Marshal(evidence)
	if err != nil {
		return "", fmt.Errorf("marshal evidence: %w", err)
	}
	return string(b), nil
}

func listMemories(db *sql.DB, tags string, limit int, callerAgent string, trustPeers []string, maxAge, minKind, sourceAgent string) ([]memoryEntry, error) {
	visFilter, visArgs := buildVisibilityFilter(callerAgent, trustPeers)
	ageFilter, ageArgs := buildTimeDecayFilter(maxAge)
	kindFilter, kindArgs := buildMinKindFilter(minKind)

	var conditions []string
	var args []any

	if tags != "" {
		conditions = append(conditions, "tags LIKE ?")
		args = append(args, "%"+tags+"%")
	}
	if sourceAgent != "" {
		conditions = append(conditions, "source_agent = ?")
		args = append(args, sourceAgent)
	}

	args = append(args, visArgs...)
	args = append(args, ageArgs...)
	args = append(args, kindArgs...)

	// currentOnlyFilter, visFilter, ageFilter, and kindFilter all start with
	// "AND", so the WHERE clause always needs a leading condition.
	where := "WHERE 1=1"
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	args = append(args, limit)

	q := fmt.Sprintf(`
		SELECT %s
		FROM memories m
		%s %s %s %s %s
		ORDER BY updated_at DESC
		LIMIT ?
	`, memoryColumns, where, currentOnlyFilter, visFilter, ageFilter, kindFilter)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list failed: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// getProvenanceChain walks parent_id links from id back to the root and
// returns the chain root first. parent_id holds memory ids, so each step reads
// the current version of that memory. Forgotten memories are walked through but
// left out, so their content cannot be read back through provenance.
func getProvenanceChain(db *sql.DB, id int64) ([]memoryEntry, error) {
	chain := []memoryEntry{}
	seen := map[int64]bool{}
	current := id

	for current > 0 && !seen[current] {
		seen[current] = true
		e, found, err := getMemoryByID(db, current)
		if err != nil || !found {
			break
		}
		if !e.Forgotten {
			chain = append(chain, e)
		}
		current = e.ParentID
	}

	// Reverse so root is first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// getMemoryByID fetches the current version of a memory, including a forget
// version (Forgotten set). found is false (with a nil error) when no version
// exists.
func getMemoryByID(db *sql.DB, id int64) (memoryEntry, bool, error) {
	return latestVersion(db, id)
}

// latestVersion reads the current version of memory id through q, which is a
// *sql.DB or *sql.Tx.
func latestVersion(q interface {
	QueryRow(string, ...any) *sql.Row
}, id int64) (memoryEntry, bool, error) {
	var e memoryEntry
	err := scanEntry(q.QueryRow(`SELECT `+memoryColumns+` FROM memories m WHERE m.id = ? ORDER BY m.seq DESC LIMIT 1`, id), &e)
	if err == sql.ErrNoRows {
		return memoryEntry{}, false, nil
	}
	if err != nil {
		return memoryEntry{}, false, fmt.Errorf("lookup by id: %w", err)
	}
	return e, true, nil
}

// getMemoryHistory returns every version of memory id, oldest first. It
// returns an empty slice when id does not exist.
func getMemoryHistory(db *sql.DB, id int64) ([]memoryEntry, error) {
	rows, err := db.Query(`SELECT `+memoryColumns+` FROM memories m WHERE m.id = ? ORDER BY m.seq`, id)
	if err != nil {
		return nil, fmt.Errorf("history: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

var (
	// errMemoryNotFound is returned when the memory does not exist or belongs
	// to a different source agent. Both cases share one error so a caller
	// cannot probe for other agents' private entries.
	errMemoryNotFound = errors.New("memory entry not found")
	// errMemoryForgotten is returned when the current version is a forget.
	errMemoryForgotten = errors.New("memory entry has been forgotten")
	// errMemoryConflict is returned when another write to the same memory
	// took the (id, seq) slot first.
	errMemoryConflict = errors.New("concurrent write to memory entry")
)

// versionRequest describes a new version of an existing memory. An empty
// Content makes it a forget (stored as NULL content). Nil Tags, empty
// Visibility and nil Evidence keep the current version's values;
// ClearEvidence stores the new version without evidence.
type versionRequest struct {
	ID            int64
	Content       string
	Tags          []string
	Visibility    string
	SourceAgent   string
	Evidence      *EvidenceTrace
	ClearEvidence bool
}

// appendVersion adds a new version of memory req.ID and returns its seq and
// timestamp. The memory's current version must have content and the same
// source_agent as the caller. The new version keeps the memory's parent_id.
// No existing row is changed; the FTS triggers move the index to the new
// version.
func appendVersion(db *sql.DB, req versionRequest) (int64, string, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := db.Begin()
	if err != nil {
		return 0, "", fmt.Errorf("version begin tx: %w", err)
	}
	defer tx.Rollback()

	current, found, err := latestVersion(tx, req.ID)
	if err != nil {
		return 0, "", err
	}
	if !found || current.SourceAgent != req.SourceAgent {
		return 0, "", errMemoryNotFound
	}
	if current.Forgotten {
		return 0, "", errMemoryForgotten
	}

	tags := req.Tags
	if tags == nil {
		tags = current.Tags
	}
	visibility := req.Visibility
	if visibility == "" {
		visibility = current.Visibility
	}
	evidence := req.Evidence
	if evidence == nil && !req.ClearEvidence {
		evidence = current.Evidence
	}
	evidenceStr, err := marshalEvidence(evidence)
	if err != nil {
		return 0, "", err
	}
	content := sql.NullString{String: req.Content, Valid: req.Content != ""}

	var nextSeq int64
	_ = tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM memories`).Scan(&nextSeq)

	_, err = tx.Exec(`
		INSERT INTO memories (id, seq, content, tags, visibility, source_agent, parent_id, created_at, updated_at, evidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, req.ID, nextSeq, content, strings.Join(tags, ","), visibility, current.SourceAgent, current.ParentID, now, now, evidenceStr)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return 0, "", errMemoryConflict
		}
		return 0, "", fmt.Errorf("version insert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("version commit: %w", err)
	}
	return nextSeq, now, nil
}

// deleteMemory hard-deletes versions of memory id: the version with the given
// seq, or every version when seq is 0. It returns the deleted rows. The
// memories_ad trigger keeps memories_fts in sync.
func deleteMemory(db *sql.DB, id, seq int64) ([]memoryEntry, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("delete begin tx: %w", err)
	}
	defer tx.Rollback()

	where := `m.id = ?`
	args := []any{id}
	if seq > 0 {
		where += ` AND m.seq = ?`
		args = append(args, seq)
	}
	rows, err := tx.Query(`SELECT `+memoryColumns+` FROM memories m WHERE `+where+` ORDER BY m.seq`, args...)
	if err != nil {
		return nil, fmt.Errorf("delete lookup: %w", err)
	}
	deleted, err := scanEntries(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM memories AS m WHERE `+where, args...); err != nil {
		return nil, fmt.Errorf("delete failed: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("delete commit: %w", err)
	}
	return deleted, nil
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanEntry reads the memoryColumns list into e.
func scanEntry(row rowScanner, e *memoryEntry) error {
	var content sql.NullString
	var tags string
	var evidenceStr string
	if err := row.Scan(&e.ID, &content, &tags, &e.Visibility, &e.SourceAgent, &e.ParentID, &e.Seq, &e.CreatedAt, &e.UpdatedAt, &evidenceStr); err != nil {
		return err
	}
	e.Content = content.String
	e.Forgotten = !content.Valid
	if tags != "" {
		e.Tags = strings.Split(tags, ",")
	}
	if evidenceStr != "" {
		var ev EvidenceTrace
		if err := json.Unmarshal([]byte(evidenceStr), &ev); err == nil {
			e.Evidence = &ev
		}
	}
	return nil
}

func scanEntries(rows *sql.Rows) ([]memoryEntry, error) {
	results := []memoryEntry{}
	for rows.Next() {
		var e memoryEntry
		if err := scanEntry(rows, &e); err != nil {
			continue
		}
		results = append(results, e)
	}
	return results, nil
}

// --- Membrane filter builders ---

// buildVisibilityFilter returns an SQL fragment and args that enforce
// three-tier permeability. If callerAgent is empty, no filtering is applied
// (backward-compatible with pre-membrane deployments).
func buildVisibilityFilter(callerAgent string, trustPeers []string) (string, []any) {
	if callerAgent == "" {
		return "", nil
	}

	// Caller can always see:
	// 1. Public entries
	// 2. Trusted entries from their trust peers
	// 3. Their own entries (any visibility)
	peers := append(trustPeers, callerAgent)
	placeholders := make([]string, len(peers))
	args := make([]any, 0, len(peers)+1)
	for i, p := range peers {
		placeholders[i] = "?"
		args = append(args, p)
	}
	args = append(args, callerAgent)

	filter := fmt.Sprintf(
		`AND (visibility = 'public' OR (visibility = 'trusted' AND source_agent IN (%s)) OR source_agent = ?)`,
		strings.Join(placeholders, ","),
	)
	return filter, args
}

// buildTimeDecayFilter returns an SQL fragment that excludes entries older
// than maxAge. maxAge should be a Go duration string (e.g., "24h", "168h").
func buildTimeDecayFilter(maxAge string) (string, []any) {
	if maxAge == "" {
		return "", nil
	}
	d, err := time.ParseDuration(maxAge)
	if err != nil {
		return "", nil
	}
	cutoff := time.Now().UTC().Add(-d).Format(time.RFC3339)
	return "AND created_at > ?", []any{cutoff}
}

// buildAcceptTagsFilter returns an SQL fragment that filters entries to only
// those with at least one matching tag from acceptTags.
func buildAcceptTagsFilter(acceptTags []string) (string, []any) {
	if len(acceptTags) == 0 {
		return "", nil
	}
	conditions := make([]string, len(acceptTags))
	args := make([]any, len(acceptTags))
	for i, tag := range acceptTags {
		conditions[i] = "tags LIKE ?"
		args[i] = "%" + tag + "%"
	}
	return "AND (" + strings.Join(conditions, " OR ") + ")", args
}

// --- Schema ---

// memoriesTableSQL is the versioned memories table. Every row is one version
// of a memory: id is shared by all versions of a memory, and the row with the
// highest seq for an id is its current version. NULL content is a forget.
// version_id is internal: it is the FTS rowid, and an explicit INTEGER PRIMARY
// KEY so VACUUM cannot renumber it.
const memoriesTableSQL = `
	CREATE TABLE IF NOT EXISTS %s (
		version_id   INTEGER PRIMARY KEY AUTOINCREMENT,
		id           INTEGER NOT NULL,
		seq          INTEGER NOT NULL,
		content      TEXT,
		tags         TEXT DEFAULT '',
		visibility   TEXT DEFAULT 'public',
		source_agent TEXT DEFAULT '',
		parent_id    INTEGER DEFAULT 0,
		evidence     TEXT DEFAULT '',
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL,
		UNIQUE (id, seq)
	)`

// openDB opens the memory database for the configured path dbPath
// (MEMORY_DB_PATH) and brings its schema up to date.
//
// The server never writes to dbPath itself. That file is the database of
// older releases, and older images cannot use the versioned schema, so it is
// left exactly as it is: a `helm rollback` then needs no restore. The server
// works on versionedDBPath(dbPath) instead. On first start it creates that
// file as a migrated copy of dbPath (see copyLegacyDB), or as a fresh
// database when dbPath does not exist. On every later start it uses the file
// as it is, and warns when dbPath has changed since the copy was made.
//
// MEMORY_DB_PATH keeps naming the old file on purpose: the controller only
// sets it when it creates a memory Deployment, so after a rollback the older
// release would still see a changed value and open the migrated file.
//
// Transactions start with BEGIN IMMEDIATE (_txlock), so a write transaction
// takes the write lock before its first read. Concurrent writers then wait on
// busy_timeout instead of failing with SQLITE_BUSY when a read-then-write
// transaction (MAX(seq), current version) tries to upgrade its lock.
func openDB(dbPath string) (*sql.DB, error) {
	path := versionedDBPath(dbPath)
	if err := copyLegacyDB(dbPath, path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	if err := migrateSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	warnIfLegacyChanged(db, dbPath, path)
	return db, nil
}

// migrateSchema runs every schema step in order. Each step is idempotent.
func migrateSchema(db *sql.DB) error {
	for _, step := range []struct {
		name string
		run  func(*sql.DB) error
	}{
		{"initialize schema", initSchema},
		{"run membrane migration", migrateMembraneColumns},
		{"run evidence migration", migrateEvidenceColumn},
		{"run versioned schema migration", migrateVersionedSchema},
		{"set up search index", ensureSearchIndex},
		{"set up copy record", initCopyRecord},
	} {
		if err := step.run(db); err != nil {
			return fmt.Errorf("failed to %s: %w", step.name, err)
		}
	}
	return nil
}

// versionedDBPath returns the path of the versioned database for the legacy
// path dbPath: /data/memory.db becomes /data/memory.v2.db.
func versionedDBPath(dbPath string) string {
	ext := filepath.Ext(dbPath)
	return strings.TrimSuffix(dbPath, ext) + ".v2" + ext
}

// copyLegacyDB creates the versioned database at path from the legacy
// database at legacyPath, when path does not exist yet and legacyPath does.
//
// It copies legacyPath with VACUUM INTO over a read-only connection. That is
// a consistent snapshot, including writes still in the WAL of a server that
// was killed, and it leaves legacyPath and its side files unchanged. The copy
// is written to path+".tmp", migrated there, and only then renamed to path, so
// path never holds a partial copy. A ".tmp" file from an interrupted start is
// discarded first. The copy is migrated without WAL, so it is a single file
// when renamed.
func copyLegacyDB(legacyPath, path string) error {
	tmp := path + ".tmp"
	for _, f := range []string{tmp, tmp + "-journal", tmp + "-wal", tmp + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove partial copy %s: %w", f, err)
		}
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check %s: %w", path, err)
	}
	// path does not exist, so any side files with its name are leftovers of a
	// deleted database (for example a WAL from a killed server). SQLite would
	// replay a leftover WAL into the new file, so remove them first.
	for _, f := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove leftover %s: %w", f, err)
		}
	}
	if _, err := os.Stat(legacyPath); errors.Is(err, os.ErrNotExist) {
		return nil // Fresh install: openDB creates path.
	} else if err != nil {
		return fmt.Errorf("check %s: %w", legacyPath, err)
	}

	log.Printf("[memory-server] copying %s to %s; %s is left unchanged", legacyPath, path, legacyPath)
	fp, err := snapshotLegacyDB(legacyPath, tmp)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := migrateCopy(tmp, legacyPath, fp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("move %s into place: %w", tmp, err)
	}
	log.Printf("[memory-server] %s is ready", path)
	return nil
}

// legacyFingerprint summarises the legacy memories table, so a later start
// can tell whether an older server wrote to it after the copy was made.
type legacyFingerprint struct {
	Rows         int64
	MaxID        int64
	MaxUpdatedAt string
}

// openReadOnly opens path read-only; nothing it does can change the file.
func openReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	return db, nil
}

// snapshotLegacyDB writes a copy of legacyPath to dst and returns the
// fingerprint of the copied data, read in the same read transaction.
func snapshotLegacyDB(legacyPath, dst string) (legacyFingerprint, error) {
	src, err := openReadOnly(legacyPath)
	if err != nil {
		return legacyFingerprint{}, err
	}
	defer src.Close()
	if _, err := src.Exec(`VACUUM INTO ?`, dst); err != nil {
		return legacyFingerprint{}, fmt.Errorf("copy %s: %w", legacyPath, err)
	}
	// Fingerprint the copy rather than the source: it is exactly the data
	// that was copied, even if the source changed in between.
	cp, err := openReadOnly(dst)
	if err != nil {
		return legacyFingerprint{}, err
	}
	defer cp.Close()
	return fingerprint(cp)
}

// fingerprint reads the legacyFingerprint of db. A database without a
// memories table has the zero fingerprint.
func fingerprint(db *sql.DB) (legacyFingerprint, error) {
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'memories'`).Scan(&tables); err != nil {
		return legacyFingerprint{}, fmt.Errorf("fingerprint: %w", err)
	}
	var fp legacyFingerprint
	if tables == 0 {
		return fp, nil
	}
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(id), 0), COALESCE(MAX(updated_at), '') FROM memories`).
		Scan(&fp.Rows, &fp.MaxID, &fp.MaxUpdatedAt)
	if err != nil {
		return legacyFingerprint{}, fmt.Errorf("fingerprint: %w", err)
	}
	return fp, nil
}

// migrateCopy migrates the copied database at tmp and records where it was
// copied from.
func migrateCopy(tmp, legacyPath string, fp legacyFingerprint) error {
	db, err := sql.Open("sqlite", tmp+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return fmt.Errorf("open copy %s: %w", tmp, err)
	}
	defer db.Close()
	if err := migrateSchema(db); err != nil {
		return err
	}
	_, err = db.Exec(`
		INSERT INTO copied_from (source, rows, max_id, max_updated_at, copied_at)
		VALUES (?, ?, ?, ?, ?)
	`, legacyPath, fp.Rows, fp.MaxID, fp.MaxUpdatedAt, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("record copy source: %w", err)
	}
	return db.Close()
}

// initCopyRecord creates the copied_from table. It holds one row when the
// database was created as a copy of a legacy database, and none when it
// started fresh.
func initCopyRecord(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS copied_from (
			source         TEXT NOT NULL,
			rows           INTEGER NOT NULL,
			max_id         INTEGER NOT NULL,
			max_updated_at TEXT NOT NULL,
			copied_at      TEXT NOT NULL
		)`)
	return err
}

// warnIfLegacyChanged logs a warning when the legacy database at legacyPath
// differs from the one path was copied from. That happens when an older
// memory server wrote to it after a rollback: those writes are not in path,
// and this server will not pick them up. It never changes either file.
func warnIfLegacyChanged(db *sql.DB, legacyPath, path string) {
	var want legacyFingerprint
	var copiedAt string
	err := db.QueryRow(`SELECT rows, max_id, max_updated_at, copied_at FROM copied_from LIMIT 1`).
		Scan(&want.Rows, &want.MaxID, &want.MaxUpdatedAt, &copiedAt)
	if err != nil {
		return // Not a copy (fresh database), or unreadable: nothing to compare.
	}
	if _, err := os.Stat(legacyPath); err != nil {
		return // The old file was removed after the upgrade.
	}
	legacy, err := openReadOnly(legacyPath)
	if err != nil {
		log.Printf("[memory-server] cannot check %s for changes: %v", legacyPath, err)
		return
	}
	defer legacy.Close()
	got, err := fingerprint(legacy)
	if err != nil {
		log.Printf("[memory-server] cannot check %s for changes: %v", legacyPath, err)
		return
	}
	if got != want {
		log.Printf("[memory-server] WARNING: %s has changed since it was copied to %s at %s "+
			"(an older memory server wrote to it, probably after a rollback). Those writes are not in %s. "+
			"To start over from %s instead, stop this server and delete %s; the writes made since %s will then be lost.",
			legacyPath, path, copiedAt, path, legacyPath, path, copiedAt)
	}
}

// initSchema creates the versioned memories table on a fresh database. On a
// database from an older release the table already exists and is left alone;
// the migrations below bring it up to date.
func initSchema(db *sql.DB) error {
	if _, err := db.Exec(fmt.Sprintf(memoriesTableSQL, "memories")); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS memory_ids (next INTEGER NOT NULL)`)
	return err
}

// migrateMembraneColumns adds membrane columns to an existing database.
// Safe to call on fresh databases (columns won't exist yet) and idempotent
// on already-migrated databases.
func migrateMembraneColumns(db *sql.DB) error {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name='visibility'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("membrane migration check: %w", err)
	}
	if count > 0 {
		return nil // already migrated
	}

	log.Printf("[memory-server] running membrane schema migration")
	for _, stmt := range []string{
		`ALTER TABLE memories ADD COLUMN visibility TEXT DEFAULT 'public'`,
		`ALTER TABLE memories ADD COLUMN source_agent TEXT DEFAULT ''`,
		`ALTER TABLE memories ADD COLUMN parent_id INTEGER DEFAULT 0`,
		`ALTER TABLE memories ADD COLUMN seq INTEGER DEFAULT 0`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("membrane migration: %w", err)
		}
	}

	// Create indexes for membrane columns.
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_memories_visibility ON memories(visibility)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_source_agent ON memories(source_agent)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_seq ON memories(seq DESC)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			log.Printf("[memory-server] warning: index creation: %v", err)
		}
	}
	log.Printf("[memory-server] membrane migration complete")
	return nil
}

// migrateEvidenceColumn adds the evidence column to an existing database.
// Idempotent: safe to call on fresh or already-migrated databases.
func migrateEvidenceColumn(db *sql.DB) error {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name='evidence'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("evidence migration check: %w", err)
	}
	if count > 0 {
		return nil // already migrated
	}

	log.Printf("[memory-server] running evidence schema migration")
	if _, err := db.Exec(`ALTER TABLE memories ADD COLUMN evidence TEXT DEFAULT ''`); err != nil {
		return fmt.Errorf("evidence migration: %w", err)
	}
	log.Printf("[memory-server] evidence migration complete")
	return nil
}

// migrateVersionedSchema rebuilds a memories table from an older release (id
// as the primary key, one row per memory) into the versioned table. Each old
// row becomes version seq of memory id, so ids agents already know keep
// working. It then makes sure the indexes and the id counter exist.
// Idempotent: the rebuild is skipped once version_id exists.
//
// The rebuild is one-way: older memory-server images cannot write to the new
// table. openDB therefore only runs it on a copy of the old database, never
// on the old database itself (see copyLegacyDB).
func migrateVersionedSchema(db *sql.DB) error {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name='version_id'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("versioned migration check: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("versioned migration begin tx: %w", err)
	}
	defer tx.Rollback()

	if count == 0 {
		log.Printf("[memory-server] running versioned schema migration")
		for _, stmt := range []string{
			fmt.Sprintf(memoriesTableSQL, "memories_new"),
			`INSERT INTO memories_new (version_id, id, seq, content, tags, visibility, source_agent, parent_id, evidence, created_at, updated_at)
			 SELECT id, id, COALESCE(seq, 0), content, COALESCE(tags, ''), COALESCE(visibility, 'public'),
			        COALESCE(source_agent, ''), COALESCE(parent_id, 0), COALESCE(evidence, ''), created_at, updated_at
			 FROM memories`,
			// Seed the id counter past every id the old AUTOINCREMENT handed
			// out, including deleted ones above MAX(id). sqlite_sequence
			// loses that row when the old table is dropped, so read it first.
			`CREATE TABLE IF NOT EXISTS memory_ids (next INTEGER NOT NULL)`,
			`INSERT INTO memory_ids (next)
			 SELECT MAX(COALESCE((SELECT MAX(id) FROM memories), 0),
			            COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'memories'), 0)) + 1
			 WHERE NOT EXISTS (SELECT 1 FROM memory_ids)`,
			// The old FTS table uses memories as external content; it is
			// recreated on the new table by ensureSearchIndex. Dropping the
			// old table also drops its triggers and indexes.
			`DROP TABLE IF EXISTS memories_fts`,
			`DROP TABLE memories`,
			`ALTER TABLE memories_new RENAME TO memories`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("versioned migration: %w", err)
			}
		}
	}

	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_memories_updated ON memories(updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_tags ON memories(tags)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_visibility ON memories(visibility)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_source_agent ON memories(source_agent)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_seq ON memories(seq DESC)`,
		`CREATE TABLE IF NOT EXISTS memory_ids (next INTEGER NOT NULL)`,
		`INSERT INTO memory_ids (next)
		 SELECT COALESCE((SELECT MAX(id) FROM memories), 0) + 1
		 WHERE NOT EXISTS (SELECT 1 FROM memory_ids)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("versioned schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("versioned migration commit: %w", err)
	}
	if count == 0 {
		log.Printf("[memory-server] versioned schema migration complete")
	}
	return nil
}

// searchIndexSQL creates the FTS index over exactly the current versions that
// have content. memories_current is the FTS external content, so 'rebuild'
// and 'integrity-check' work against that set. The triggers keep the index in
// step with inserts and admin deletes, and only issue an FTS 'delete' for a
// row that is indexed, which keeps the index from being corrupted. Rows are
// never updated; memories_no_update enforces that.
const searchIndexSQL = `
	CREATE VIEW memories_current AS
		SELECT m.version_id, m.content FROM memories m
		WHERE m.content IS NOT NULL
		  AND m.seq = (SELECT MAX(x.seq) FROM memories x WHERE x.id = m.id);

	CREATE VIRTUAL TABLE memories_fts USING fts5(
		content,
		content='memories_current',
		content_rowid='version_id',
		tokenize='porter unicode61'
	);

	-- A new version that is now the latest replaces the previous latest.
	CREATE TRIGGER memories_ai AFTER INSERT ON memories
	WHEN NOT EXISTS (SELECT 1 FROM memories x WHERE x.id = new.id AND x.seq > new.seq)
	BEGIN
		INSERT INTO memories_fts(memories_fts, rowid, content)
			SELECT 'delete', p.version_id, p.content FROM memories p
			WHERE p.id = new.id AND p.content IS NOT NULL
			  AND p.seq = (SELECT MAX(x.seq) FROM memories x WHERE x.id = new.id AND x.seq < new.seq);
		INSERT INTO memories_fts(rowid, content)
			SELECT new.version_id, new.content WHERE new.content IS NOT NULL;
	END;

	-- Deleting the latest version makes the previous version current.
	CREATE TRIGGER memories_ad AFTER DELETE ON memories
	WHEN NOT EXISTS (SELECT 1 FROM memories x WHERE x.id = old.id AND x.seq > old.seq)
	BEGIN
		INSERT INTO memories_fts(memories_fts, rowid, content)
			SELECT 'delete', old.version_id, old.content WHERE old.content IS NOT NULL;
		INSERT INTO memories_fts(rowid, content)
			SELECT p.version_id, p.content FROM memories p
			WHERE p.id = old.id AND p.content IS NOT NULL
			  AND p.seq = (SELECT MAX(x.seq) FROM memories x WHERE x.id = old.id);
	END;

	CREATE TRIGGER memories_no_update BEFORE UPDATE ON memories
	BEGIN
		SELECT RAISE(ABORT, 'memories is append-only');
	END;

	INSERT INTO memories_fts(memories_fts) VALUES('rebuild');
`

// ensureSearchIndex creates the FTS index, its content view and triggers when
// they are missing or still in the pre-versioning form, and rebuilds the
// index from memories_current. Idempotent.
func ensureSearchIndex(db *sql.DB) error {
	var ftsSQL string
	err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'memories_fts'`).Scan(&ftsSQL)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("search index check: %w", err)
	}
	if strings.Contains(ftsSQL, "memories_current") {
		return nil
	}

	log.Printf("[memory-server] building search index")
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("search index begin tx: %w", err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS memories_fts`,
		`DROP VIEW IF EXISTS memories_current`,
		`DROP TRIGGER IF EXISTS memories_ai`,
		`DROP TRIGGER IF EXISTS memories_ad`,
		`DROP TRIGGER IF EXISTS memories_au`,
		`DROP TRIGGER IF EXISTS memories_no_update`,
		searchIndexSQL,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("search index: %w", err)
		}
	}
	return tx.Commit()
}

// evidenceKindRank maps evidence kinds to quality ranks for filtering.
var evidenceKindRank = map[string]int{
	"tool_result":        4,
	"external_source":    3,
	"llm_interpretation": 2,
	"agent_opinion":      1,
}

func buildMinKindFilter(minKind string) (string, []any) {
	if minKind == "" {
		return "", nil
	}
	rank, ok := evidenceKindRank[minKind]
	if !ok {
		return "", nil
	}
	// Build SQL that checks the JSON 'kind' field in the evidence column.
	// Entries with no evidence pass through (backward compatible).
	var kinds []string
	for k, r := range evidenceKindRank {
		if r >= rank {
			kinds = append(kinds, k)
		}
	}
	conditions := make([]string, len(kinds))
	args := make([]any, len(kinds))
	for i, k := range kinds {
		conditions[i] = "json_extract(evidence, '$.kind') = ?"
		args[i] = k
	}
	return fmt.Sprintf("AND (evidence = '' OR %s)", strings.Join(conditions, " OR ")), args
}

// fts5Query converts a natural language query into an FTS5 query.
// Each word becomes a prefix search term joined with AND.
func fts5Query(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return query
	}
	terms := make([]string, 0, len(words))
	for _, w := range words {
		// Strip special FTS5 characters to prevent syntax errors.
		w = strings.Map(func(r rune) rune {
			if r == '"' || r == '*' || r == '+' || r == '-' || r == '(' || r == ')' || r == ':' || r == '^' {
				return -1
			}
			return r
		}, w)
		if w != "" {
			terms = append(terms, w+"*")
		}
	}
	if len(terms) == 0 {
		return query
	}
	return strings.Join(terms, " AND ")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func truncateLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
