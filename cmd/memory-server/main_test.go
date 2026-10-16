package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedTestDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := migrateSchema(db); err != nil {
		t.Fatalf("migrateSchema: %v", err)
	}
}

// setupTestDB is an alias that includes all migrations (used by evidence tests).
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	seedTestDB(t, db)
	return db
}

// helper to store with defaults for backward-compat tests.
func storeDefault(t *testing.T, db *sql.DB, content string, tags []string) int64 {
	t.Helper()
	id, _, _, err := storeMemory(db, content, tags, "public", "", 0, nil)
	if err != nil {
		t.Fatalf("storeMemory: %v", err)
	}
	return id
}

// ── initSchema tests ─────────────────────────────────────────────────────────

func TestInitSchema(t *testing.T) {
	db := openTestDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	// Verify the memories table exists.
	var tableName string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='memories'`).Scan(&tableName)
	if err != nil {
		t.Fatalf("memories table not found: %v", err)
	}

	// Verify idempotency -- calling again should not fail.
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema (idempotent): %v", err)
	}

	// The FTS5 virtual table is created by ensureSearchIndex.
	if err := ensureSearchIndex(db); err != nil {
		t.Fatalf("ensureSearchIndex: %v", err)
	}
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='memories_fts'`).Scan(&tableName)
	if err != nil {
		t.Fatalf("memories_fts virtual table not found: %v", err)
	}
}

// ── Membrane migration tests ────────────────────────────────────────────────

func TestMigrateMembraneColumns(t *testing.T) {
	db := openTestDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	if err := migrateMembraneColumns(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// Verify columns exist.
	for _, col := range []string{"visibility", "source_agent", "parent_id", "seq"} {
		var count int
		err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name=?`, col).Scan(&count)
		if err != nil || count != 1 {
			t.Errorf("column %q missing after migration", col)
		}
	}
}

func TestMigrateMembraneColumns_Idempotent(t *testing.T) {
	db := openTestDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}
	if err := migrateMembraneColumns(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := migrateMembraneColumns(db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}
}

// ── Evidence migration tests ─────────────────────────────────────────────────

func TestMigrateEvidenceColumn_Idempotent(t *testing.T) {
	db := setupTestDB(t)
	// Second call should be no-op
	if err := migrateEvidenceColumn(db); err != nil {
		t.Fatalf("second migration: %v", err)
	}
}

// ── Core database function tests ─────────────────────────────────────────────

func TestStoreMemory(t *testing.T) {
	db := setupTestDB(t)

	id, seq, storedAt, err := storeMemory(db, "Kafka consumer lag detected", []string{"kafka", "payments"}, "public", "researcher", 0, nil)
	if err != nil {
		t.Fatalf("storeMemory: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}
	if seq <= 0 {
		t.Errorf("expected positive seq, got %d", seq)
	}
	if storedAt == "" {
		t.Error("expected non-empty stored_at")
	}
}

func TestStoreMemory_WithVisibility(t *testing.T) {
	db := setupTestDB(t)

	for _, vis := range []string{"public", "trusted", "private"} {
		id, _, _, err := storeMemory(db, "entry-"+vis, nil, vis, "agent-a", 0, nil)
		if err != nil {
			t.Fatalf("storeMemory(%s): %v", vis, err)
		}

		var got string
		err = db.QueryRow(`SELECT visibility FROM memories WHERE id = ?`, id).Scan(&got)
		if err != nil {
			t.Fatalf("query visibility: %v", err)
		}
		if got != vis {
			t.Errorf("visibility = %q, want %q", got, vis)
		}
	}
}

func TestStoreMemory_SequenceNumbers(t *testing.T) {
	db := setupTestDB(t)

	_, seq1, _, _ := storeMemory(db, "first", nil, "public", "", 0, nil)
	_, seq2, _, _ := storeMemory(db, "second", nil, "public", "", 0, nil)
	_, seq3, _, _ := storeMemory(db, "third", nil, "public", "", 0, nil)

	if seq2 != seq1+1 || seq3 != seq2+1 {
		t.Errorf("expected monotonic seq: %d, %d, %d", seq1, seq2, seq3)
	}
}

func TestStoreMemory_WithEvidence(t *testing.T) {
	db := setupTestDB(t)
	ev := &EvidenceTrace{
		Kind:       "tool_result",
		ToolCall:   "web_search(query='kubernetes')",
		RawResult:  "Found 10 results",
		Source:     "https://k8s.io",
		Confidence: 0.9,
	}
	id, seq, _, err := storeMemory(db, "k8s findings", []string{"k8s"}, "public", "researcher", 0, ev)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if id <= 0 || seq <= 0 {
		t.Fatalf("expected positive id/seq, got id=%d seq=%d", id, seq)
	}

	// Verify evidence was stored
	chain, err := getProvenanceChain(db, id)
	if err != nil {
		t.Fatalf("provenance: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(chain))
	}
	if chain[0].Evidence == nil {
		t.Fatal("expected evidence to be set")
	}
	if chain[0].Evidence.Kind != "tool_result" {
		t.Errorf("kind = %q, want tool_result", chain[0].Evidence.Kind)
	}
	if chain[0].Evidence.Confidence != 0.9 {
		t.Errorf("confidence = %f, want 0.9", chain[0].Evidence.Confidence)
	}
}

func TestStoreMemory_WithoutEvidence(t *testing.T) {
	db := setupTestDB(t)
	id, _, _, err := storeMemory(db, "no evidence entry", nil, "public", "agent", 0, nil)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	chain, _ := getProvenanceChain(db, id)
	if len(chain) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(chain))
	}
	if chain[0].Evidence != nil {
		t.Error("expected nil evidence for entry stored without evidence")
	}
}

func TestStoreMemory_EvidenceDerivedFrom(t *testing.T) {
	db := setupTestDB(t)
	// Store parent
	parentID, _, _, _ := storeMemory(db, "parent finding", nil, "public", "a", 0, &EvidenceTrace{Kind: "tool_result"})
	// Store child with derived_from
	childID, _, _, _ := storeMemory(db, "derived finding", nil, "public", "b", parentID, &EvidenceTrace{
		Kind:        "llm_interpretation",
		DerivedFrom: []int64{parentID},
		Confidence:  0.6,
	})

	chain, _ := getProvenanceChain(db, childID)
	if len(chain) != 2 {
		t.Fatalf("expected 2 entries in provenance chain, got %d", len(chain))
	}
	// Root first
	if chain[0].ID != parentID {
		t.Errorf("first in chain should be parent, got id=%d", chain[0].ID)
	}
	if chain[1].Evidence.DerivedFrom[0] != parentID {
		t.Errorf("derived_from should reference parent")
	}
}

// ── buildMinKindFilter tests ─────────────────────────────────────────────────

func TestBuildMinKindFilter_NoFilter(t *testing.T) {
	sql, args := buildMinKindFilter("")
	if sql != "" || args != nil {
		t.Errorf("empty minKind should produce no filter, got sql=%q args=%v", sql, args)
	}
}

func TestBuildMinKindFilter_InvalidKind(t *testing.T) {
	sql, args := buildMinKindFilter("invalid")
	if sql != "" || args != nil {
		t.Errorf("invalid minKind should produce no filter")
	}
}

func TestBuildMinKindFilter_ToolResult(t *testing.T) {
	sql, args := buildMinKindFilter("tool_result")
	if sql == "" {
		t.Fatal("expected filter for tool_result")
	}
	// Only tool_result should pass (rank 4, only rank >= 4)
	if len(args) != 1 {
		t.Errorf("expected 1 arg for tool_result filter, got %d", len(args))
	}
}

func TestBuildMinKindFilter_ExternalSource(t *testing.T) {
	_, args := buildMinKindFilter("external_source")
	// external_source (3) and tool_result (4) should pass
	if len(args) != 2 {
		t.Errorf("expected 2 args for external_source filter, got %d", len(args))
	}
}

func TestBuildMinKindFilter_AgentOpinion(t *testing.T) {
	_, args := buildMinKindFilter("agent_opinion")
	// All 4 kinds should pass
	if len(args) != 4 {
		t.Errorf("expected 4 args for agent_opinion filter, got %d", len(args))
	}
}

// ── Search with min_kind filter ──────────────────────────────────────────────

func TestSearchMemories(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "Kafka consumer lag detected in payments namespace", []string{"kafka"})
	storeDefault(t, db, "OOM crash in checkout service", []string{"oom"})
	storeDefault(t, db, "Deployment rollback completed for auth service", nil)

	results, err := searchMemories(db, "kafka consumer", 5, "", nil, nil, "", "")
	if err != nil {
		t.Fatalf("searchMemories: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 search result")
	}
	if results[0].Content != "Kafka consumer lag detected in payments namespace" {
		t.Errorf("first result content = %q", results[0].Content)
	}
}

func TestSearchMemories_VisibilityFilter(t *testing.T) {
	db := setupTestDB(t)

	storeMemory(db, "kafka public finding", nil, "public", "agent-a", 0, nil)
	storeMemory(db, "kafka trusted secret", nil, "trusted", "agent-a", 0, nil)
	storeMemory(db, "kafka private note", nil, "private", "agent-a", 0, nil)

	// agent-b with no trust relationship should only see public
	results, err := searchMemories(db, "kafka", 10, "agent-b", nil, nil, "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (public only), got %d", len(results))
	}
	if results[0].Content != "kafka public finding" {
		t.Errorf("expected kafka public finding, got %q", results[0].Content)
	}
}

func TestSearchMemories_TrustPeers(t *testing.T) {
	db := setupTestDB(t)

	storeMemory(db, "kafka public finding", nil, "public", "agent-a", 0, nil)
	storeMemory(db, "kafka trusted secret", nil, "trusted", "agent-a", 0, nil)
	storeMemory(db, "kafka private note", nil, "private", "agent-a", 0, nil)

	// agent-b trusts agent-a -> should see public + trusted
	results, err := searchMemories(db, "kafka", 10, "agent-b", []string{"agent-a"}, nil, "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (public + trusted), got %d", len(results))
	}
}

func TestSearchMemories_CallerSeesOwnPrivate(t *testing.T) {
	db := setupTestDB(t)

	storeMemory(db, "kafka private note", nil, "private", "agent-a", 0, nil)

	// agent-a should see their own private entries
	results, err := searchMemories(db, "kafka", 10, "agent-a", nil, nil, "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (own private), got %d", len(results))
	}
}

func TestSearchMemories_TimeDecay(t *testing.T) {
	db := setupTestDB(t)

	// Store an entry with a very old timestamp.
	_, err := db.Exec(`
		INSERT INTO memories (id, content, tags, visibility, source_agent, parent_id, seq, created_at, updated_at, evidence)
		VALUES (1000, ?, '', 'public', '', 0, 1, '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z', '')
	`, "ancient entry")
	if err != nil {
		t.Fatalf("insert old entry: %v", err)
	}
	// FTS trigger fires on INSERT, so the entry is indexed.

	storeMemory(db, "recent entry", nil, "public", "", 0, nil)

	// With max_age=24h, only the recent entry should appear.
	results, err := searchMemories(db, "entry", 10, "", nil, nil, "24h", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (recent only), got %d", len(results))
	}
	if results[0].Content != "recent entry" {
		t.Errorf("expected recent entry, got %q", results[0].Content)
	}
}

func TestSearchMemories_Fallback(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "the payments service had an OOM kill event", nil)

	results, err := searchMemories(db, "OOM kill", 5, "", nil, nil, "", "")
	if err != nil {
		t.Fatalf("searchMemories fallback: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected LIKE fallback to find the entry")
	}
}

func TestSearchMemories_MinKindFilter(t *testing.T) {
	db := setupTestDB(t)
	// Store entries with different evidence kinds
	storeMemory(db, "tool result finding about kubernetes", []string{"k8s"}, "public", "a", 0, &EvidenceTrace{Kind: "tool_result", Confidence: 0.9})
	storeMemory(db, "agent opinion about kubernetes", []string{"k8s"}, "public", "a", 0, &EvidenceTrace{Kind: "agent_opinion", Confidence: 0.3})
	storeMemory(db, "no evidence kubernetes entry", []string{"k8s"}, "public", "a", 0, nil)

	// Search without filter - should get all 3
	results, err := searchMemories(db, "kubernetes", 10, "", nil, nil, "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("no filter: expected 3 results, got %d", len(results))
	}

	// Search with min_kind=tool_result - should get tool_result + no-evidence (backward compat)
	results, err = searchMemories(db, "kubernetes", 10, "", nil, nil, "", "tool_result")
	if err != nil {
		t.Fatalf("search with min_kind: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("min_kind=tool_result: expected 2 results (tool_result + no-evidence), got %d", len(results))
	}
}

// ── List tests ───────────────────────────────────────────────────────────────

func TestListMemories(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "first entry", nil)
	storeDefault(t, db, "second entry", nil)
	storeDefault(t, db, "third entry", nil)

	results, err := listMemories(db, "", 20, "", nil, "", "", "")
	if err != nil {
		t.Fatalf("listMemories: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(results))
	}
}

func TestListMemories_WithTags(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "kafka issue", []string{"kafka", "infra"})
	storeDefault(t, db, "redis issue", []string{"redis", "infra"})
	storeDefault(t, db, "code review notes", []string{"review"})

	results, err := listMemories(db, "kafka", 20, "", nil, "", "", "")
	if err != nil {
		t.Fatalf("listMemories with tags: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 entry with kafka tag, got %d", len(results))
	}
	if results[0].Content != "kafka issue" {
		t.Errorf("entry content = %q", results[0].Content)
	}
}

func TestListMemories_SourceAgentFilter(t *testing.T) {
	db := setupTestDB(t)
	storeMemory(db, "from alpha", nil, "public", "alpha", 0, nil)
	storeMemory(db, "from beta", nil, "public", "beta", 0, nil)

	results, err := listMemories(db, "", 10, "", nil, "", "", "alpha")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for alpha, got %d", len(results))
	}
	if results[0].SourceAgent != "alpha" {
		t.Errorf("source_agent = %q, want alpha", results[0].SourceAgent)
	}
}

func TestListMemories_MinKindFilter(t *testing.T) {
	db := setupTestDB(t)
	storeMemory(db, "external source", nil, "public", "a", 0, &EvidenceTrace{Kind: "external_source"})
	storeMemory(db, "opinion", nil, "public", "a", 0, &EvidenceTrace{Kind: "agent_opinion"})

	results, _ := listMemories(db, "", 10, "", nil, "", "external_source", "")
	// Should get external_source but not agent_opinion
	if len(results) != 1 {
		t.Errorf("min_kind=external_source: expected 1 result, got %d", len(results))
	}
}

// ── Provenance tests ────────────────────────────────────────────────────────

func TestProvenanceChain(t *testing.T) {
	db := setupTestDB(t)

	// Create a chain: root -> child -> grandchild
	rootID, _, _, _ := storeMemory(db, "root finding", nil, "public", "agent-a", 0, nil)
	childID, _, _, _ := storeMemory(db, "derived insight", nil, "public", "agent-b", rootID, nil)
	grandchildID, _, _, _ := storeMemory(db, "final conclusion", nil, "public", "agent-c", childID, nil)

	chain, err := getProvenanceChain(db, grandchildID)
	if err != nil {
		t.Fatalf("getProvenanceChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("expected chain of 3, got %d", len(chain))
	}
	// Should be root-first order.
	if chain[0].ID != rootID {
		t.Errorf("chain[0].ID = %d, want root %d", chain[0].ID, rootID)
	}
	if chain[1].ID != childID {
		t.Errorf("chain[1].ID = %d, want child %d", chain[1].ID, childID)
	}
	if chain[2].ID != grandchildID {
		t.Errorf("chain[2].ID = %d, want grandchild %d", chain[2].ID, grandchildID)
	}
}

// ── fts5Query tests ──────────────────────────────────────────────────────────

func TestFts5Query(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"kafka consumer", "kafka* AND consumer*"},
		{"single", "single*"},
		{"", ""},
		{`special "chars" and (parens)`, "special* AND chars* AND and* AND parens*"},
		{"***", "***"}, // all chars stripped -> empty terms -> returns original
	}

	for _, tt := range tests {
		got := fts5Query(tt.input)
		if got != tt.want {
			t.Errorf("fts5Query(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ── HTTP handler tests ───────────────────────────────────────────────────────

func TestHealthHandler(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/health", nil)

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
	handler(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", w.Body.String())
	}
}

func TestStoreHandler(t *testing.T) {
	db := setupTestDB(t)

	body := `{"content":"Kafka lag in payments","tags":["kafka","payments"]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/store", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	storeHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Content should contain id, seq, and stored_at.
	contentBytes, _ := json.Marshal(resp.Content)
	var stored map[string]any
	if err := json.Unmarshal(contentBytes, &stored); err != nil {
		t.Fatalf("parse content: %v", err)
	}
	if id, ok := stored["id"].(float64); !ok || id <= 0 {
		t.Errorf("expected positive id, got %v", stored["id"])
	}
	if seq, ok := stored["seq"].(float64); !ok || seq <= 0 {
		t.Errorf("expected positive seq, got %v", stored["seq"])
	}
}

func TestStoreHandler_WithVisibility(t *testing.T) {
	db := setupTestDB(t)

	body := `{"content":"trusted finding","tags":["test"],"visibility":"trusted","source_agent":"researcher"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/store", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	storeHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Verify the stored entry has correct visibility.
	var vis, src string
	err := db.QueryRow(`SELECT visibility, source_agent FROM memories WHERE id = 1`).Scan(&vis, &src)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if vis != "trusted" {
		t.Errorf("visibility = %q, want trusted", vis)
	}
	if src != "researcher" {
		t.Errorf("source_agent = %q, want researcher", src)
	}
}

func TestStoreHandler_WithEvidence(t *testing.T) {
	db := setupTestDB(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /store", storeHandler(db))

	body, _ := json.Marshal(map[string]any{
		"content":      "test finding",
		"tags":         []string{"test"},
		"visibility":   "public",
		"source_agent": "tester",
		"evidence": map[string]any{
			"kind":       "tool_result",
			"tool_call":  "web_search(q='test')",
			"raw_result": "result data",
			"confidence": 0.85,
		},
	})

	req := httptest.NewRequest("POST", "/store", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("store returned %d: %s", rec.Code, rec.Body.String())
	}

	var resp apiResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Success {
		t.Fatalf("store not successful: %s", resp.Error)
	}
}

func TestStoreHandler_MissingContent(t *testing.T) {
	db := setupTestDB(t)

	body := `{}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/store", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	storeHandler(db)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestStoreHandler_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/store", bytes.NewBufferString("not json"))
	r.Header.Set("Content-Type", "application/json")

	storeHandler(db)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSearchHandler(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "Kafka consumer lag detected in payments namespace", []string{"kafka"})
	storeDefault(t, db, "OOM crash in checkout service", []string{"oom"})

	body := `{"query":"kafka consumer","top_k":5}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/search", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	searchHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	contentBytes, _ := json.Marshal(resp.Content)
	var entries []memoryEntry
	if err := json.Unmarshal(contentBytes, &entries); err != nil {
		t.Fatalf("parse content: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least 1 search result")
	}
	if entries[0].Content != "Kafka consumer lag detected in payments namespace" {
		t.Errorf("first result content = %q", entries[0].Content)
	}
}

func TestSearchHandler_MissingQuery(t *testing.T) {
	db := setupTestDB(t)

	body := `{}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/search", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	searchHandler(db)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSearchHandler_DefaultTopK(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "entry one", nil)

	body := `{"query":"entry"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/search", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	searchHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestSearchHandler_WithMinKind(t *testing.T) {
	db := setupTestDB(t)
	// Seed data
	storeMemory(db, "kubernetes tool result", nil, "public", "a", 0, &EvidenceTrace{Kind: "tool_result"})
	storeMemory(db, "kubernetes opinion", nil, "public", "a", 0, &EvidenceTrace{Kind: "agent_opinion"})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /search", searchHandler(db))

	body, _ := json.Marshal(map[string]any{
		"query":    "kubernetes",
		"top_k":    10,
		"min_kind": "tool_result",
	})

	req := httptest.NewRequest("POST", "/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("search returned %d: %s", rec.Code, rec.Body.String())
	}

	var resp apiResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Success {
		t.Fatalf("search not successful: %s", resp.Error)
	}

	var entries []memoryEntry
	raw, _ := json.Marshal(resp.Content)
	json.Unmarshal(raw, &entries)

	// Should only get tool_result (opinion filtered out)
	for _, e := range entries {
		if e.Evidence != nil && e.Evidence.Kind == "agent_opinion" {
			t.Error("agent_opinion should be filtered out with min_kind=tool_result")
		}
	}
}

func TestListHandler(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "first entry", nil)
	storeDefault(t, db, "second entry", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/list", nil)

	listHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	contentBytes, _ := json.Marshal(resp.Content)
	var entries []memoryEntry
	if err := json.Unmarshal(contentBytes, &entries); err != nil {
		t.Fatalf("parse content: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestListHandler_WithTags(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "kafka issue", []string{"kafka", "infra"})
	storeDefault(t, db, "redis issue", []string{"redis", "infra"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/list?tags=kafka", nil)

	listHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	contentBytes, _ := json.Marshal(resp.Content)
	var entries []memoryEntry
	json.Unmarshal(contentBytes, &entries)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry with kafka tag, got %d", len(entries))
	}
	if entries[0].Content != "kafka issue" {
		t.Errorf("entry content = %q", entries[0].Content)
	}
}

func TestListHandler_WithLimit(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "entry 1", nil)
	storeDefault(t, db, "entry 2", nil)
	storeDefault(t, db, "entry 3", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/list?limit=2", nil)

	listHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	contentBytes, _ := json.Marshal(resp.Content)
	var entries []memoryEntry
	json.Unmarshal(contentBytes, &entries)

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (limit=2), got %d", len(entries))
	}
}

func TestListHandler_SourceAgentParam(t *testing.T) {
	db := setupTestDB(t)
	storeMemory(db, "from alpha", nil, "public", "alpha", 0, nil)
	storeMemory(db, "from beta", nil, "public", "beta", 0, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /list", listHandler(db))

	req := httptest.NewRequest("GET", "/list?source_agent=alpha", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", rec.Code, rec.Body.String())
	}

	var resp apiResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	var entries []memoryEntry
	raw, _ := json.Marshal(resp.Content)
	json.Unmarshal(raw, &entries)

	if len(entries) != 1 {
		t.Errorf("expected 1 entry for alpha, got %d", len(entries))
	}
}

// ── Stats handler tests ─────────────────────────────────────────────────────

func TestStatsHandler(t *testing.T) {
	db := setupTestDB(t)

	storeMemory(db, "public entry", nil, "public", "agent-a", 0, nil)
	storeMemory(db, "trusted entry", nil, "trusted", "agent-a", 0, nil)
	storeMemory(db, "private entry", nil, "private", "agent-b", 0, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stats", nil)
	statsHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	contentBytes, _ := json.Marshal(resp.Content)
	var stats map[string]any
	json.Unmarshal(contentBytes, &stats)

	maxSeq, ok := stats["max_seq"].(float64)
	if !ok || maxSeq < 3 {
		t.Errorf("expected max_seq >= 3, got %v", stats["max_seq"])
	}
}

// ── Provenance handler tests ─────────────────────────────────────────────────

func TestProvenanceHandler(t *testing.T) {
	db := setupTestDB(t)

	rootID, _, _, _ := storeMemory(db, "root", nil, "public", "a", 0, nil)
	childID, _, _, _ := storeMemory(db, "child", nil, "public", "b", rootID, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/provenance?id="+itoa(childID), nil)
	provenanceHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success {
		t.Fatalf("expected success: %s", resp.Error)
	}

	contentBytes, _ := json.Marshal(resp.Content)
	var chain []memoryEntry
	json.Unmarshal(contentBytes, &chain)

	if len(chain) != 2 {
		t.Fatalf("expected chain of 2, got %d", len(chain))
	}
	if chain[0].Content != "root" || chain[1].Content != "child" {
		t.Errorf("chain = %v", chain)
	}
}

func TestProvenanceHandler_MissingID(t *testing.T) {
	db := setupTestDB(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/provenance", nil)
	provenanceHandler(db)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestProvenanceHandler_IncludesEvidence(t *testing.T) {
	db := setupTestDB(t)
	parentID, _, _, _ := storeMemory(db, "root", nil, "public", "a", 0, &EvidenceTrace{Kind: "tool_result"})
	childID, _, _, _ := storeMemory(db, "child", nil, "public", "b", parentID, &EvidenceTrace{Kind: "llm_interpretation"})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /provenance", provenanceHandler(db))

	req := httptest.NewRequest("GET", fmt.Sprintf("/provenance?id=%d", childID), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("provenance returned %d: %s", rec.Code, rec.Body.String())
	}

	var resp apiResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	var chain []memoryEntry
	raw, _ := json.Marshal(resp.Content)
	json.Unmarshal(raw, &chain)

	if len(chain) != 2 {
		t.Fatalf("expected 2 entries in chain, got %d", len(chain))
	}
	if chain[0].Evidence == nil || chain[0].Evidence.Kind != "tool_result" {
		t.Error("root should have tool_result evidence")
	}
	if chain[1].Evidence == nil || chain[1].Evidence.Kind != "llm_interpretation" {
		t.Error("child should have llm_interpretation evidence")
	}
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}

// ── deleteHandler tests ──────────────────────────────────────────────────────

func TestDeleteHandler_DisabledWithoutToken(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "corrupted entry", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/delete?id="+itoa(id), nil)
	deleteHandler(db, "")(w, r) // empty admin token → endpoint disabled

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	// The entry must remain untouched.
	if _, found, _ := getMemoryByID(db, id); !found {
		t.Error("entry should not have been deleted while endpoint is disabled")
	}
}

func TestDeleteHandler_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "sensitive entry", nil)

	cases := map[string]string{
		"missing header": "",
		"wrong token":    "Bearer nope",
		"not bearer":     "secret-token",
	}
	for name, authHeader := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("DELETE", "/delete?id="+itoa(id), nil)
			if authHeader != "" {
				r.Header.Set("Authorization", authHeader)
			}
			deleteHandler(db, "secret-token")(w, r)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
		})
	}
	if _, found, _ := getMemoryByID(db, id); !found {
		t.Error("entry should survive unauthorized delete attempts")
	}
}

func TestDeleteHandler_Success(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "delete me: kafka lag in payments", []string{"kafka"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/delete?id="+itoa(id), nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	deleteHandler(db, "secret-token")(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Row is gone from the base table.
	if _, found, err := getMemoryByID(db, id); err != nil || found {
		t.Fatalf("entry still present after delete (found=%v err=%v)", found, err)
	}
	// Gone from list.
	if list, _ := listMemories(db, "", 20, "", nil, "", "", ""); len(list) != 0 {
		t.Errorf("list returned %d entries after delete, want 0", len(list))
	}
	// Gone from FTS search too — proves the memories_ad trigger fired.
	if hits, _ := searchMemories(db, "kafka", 5, "", nil, nil, "", ""); len(hits) != 0 {
		t.Errorf("search returned %d hits after delete, want 0", len(hits))
	}
}

func TestDeleteHandler_NotFound(t *testing.T) {
	db := setupTestDB(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/delete?id=9999", nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	deleteHandler(db, "secret-token")(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDeleteHandler_BadID(t *testing.T) {
	db := setupTestDB(t)

	for _, q := range []string{"/delete", "/delete?id=abc"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("DELETE", q, nil)
		r.Header.Set("Authorization", "Bearer secret-token")
		deleteHandler(db, "secret-token")(w, r)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
}

// ── Versioning (update / forget) tests ──────────────────────────────────────

func mustVersion(t *testing.T, db *sql.DB, req versionRequest) int64 {
	t.Helper()
	seq, _, err := appendVersion(db, req)
	if err != nil {
		t.Fatalf("appendVersion(%+v): %v", req, err)
	}
	return seq
}

func listIDs(t *testing.T, entries []memoryEntry) []int64 {
	t.Helper()
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	return ids
}

// assertFTSIntegrity checks memories_fts against its content view
// memories_current: the index must hold exactly the current versions that
// have content, with the right tokens.
func assertFTSIntegrity(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO memories_fts(memories_fts, rank) VALUES('integrity-check', 1)`); err != nil {
		t.Fatalf("FTS integrity-check failed: %v", err)
	}
}

// ftsVersionIDs returns the version_ids the FTS index itself matches for term,
// without any query-time filtering.
func ftsVersionIDs(t *testing.T, db *sql.DB, term string) []int64 {
	t.Helper()
	rows, err := db.Query(`SELECT rowid FROM memories_fts WHERE memories_fts MATCH ?`, term)
	if err != nil {
		t.Fatalf("fts match: %v", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

// TestFTSIntegrityCheck_DetectsDrift proves assertFTSIntegrity is not
// vacuous: indexing a replaced version by hand must make it fail.
func TestFTSIntegrityCheck_DetectsDrift(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "stale content", nil)
	mustVersion(t, db, versionRequest{ID: id, Content: "fresh content"})

	var staleVersion int64
	db.QueryRow(`SELECT version_id FROM memories WHERE id = ? ORDER BY seq LIMIT 1`, id).Scan(&staleVersion)
	if _, err := db.Exec(`INSERT INTO memories_fts(rowid, content) VALUES (?, 'stale content')`, staleVersion); err != nil {
		t.Fatalf("inject drift: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO memories_fts(memories_fts, rank) VALUES('integrity-check', 1)`); err == nil {
		t.Fatal("integrity-check should fail when a replaced version is indexed")
	}
}

func TestMigrateVersionedSchema_Idempotent(t *testing.T) {
	db := setupTestDB(t) // already ran once
	storeDefault(t, db, "kept", nil)
	if err := migrateVersionedSchema(db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}
	if err := ensureSearchIndex(db); err != nil {
		t.Fatalf("second ensureSearchIndex should be idempotent: %v", err)
	}
	if hits, _ := searchMemories(db, "kept", 5, "", nil, nil, "", ""); len(hits) != 1 {
		t.Errorf("search after re-running migrations returned %d hits, want 1", len(hits))
	}
	assertFTSIntegrity(t, db)
}

func TestAppendVersion_UpdateKeepsID(t *testing.T) {
	db := setupTestDB(t)
	id, firstSeq, _, _ := storeMemory(db, "payments runs in namespace alpha", []string{"payments"}, "public", "", 0, nil)

	seq := mustVersion(t, db, versionRequest{ID: id, Content: "payments runs in namespace beta"})
	if seq <= firstSeq {
		t.Errorf("new seq %d should be greater than %d", seq, firstSeq)
	}

	hits, _ := searchMemories(db, "payments namespace", 10, "", nil, nil, "", "")
	if len(hits) != 1 || hits[0].ID != id || hits[0].Content != "payments runs in namespace beta" {
		t.Fatalf("search = %+v, want only the new content under id %d", hits, id)
	}
	list, _ := listMemories(db, "", 20, "", nil, "", "", "")
	if len(list) != 1 || list[0].ID != id || list[0].Seq != seq {
		t.Fatalf("list = %+v, want one entry id %d seq %d", list, id, seq)
	}
	// Omitted tags are inherited.
	if len(list[0].Tags) != 1 || list[0].Tags[0] != "payments" {
		t.Errorf("tags = %v, want [payments]", list[0].Tags)
	}
	// The old content is gone from the index itself, not just filtered.
	if got := ftsVersionIDs(t, db, "alpha"); len(got) != 0 {
		t.Errorf("FTS still indexes old content: %v", got)
	}
	assertFTSIntegrity(t, db)
}

func TestAppendVersion_KeepsVisibilityAndParent(t *testing.T) {
	db := setupTestDB(t)
	rootID, _, _, _ := storeMemory(db, "root finding", nil, "public", "agent-a", 0, nil)
	childID, _, _, _ := storeMemory(db, "private insight", []string{"x"}, "private", "agent-a", rootID, nil)

	mustVersion(t, db, versionRequest{ID: childID, Content: "corrected insight", Tags: []string{"y"}, SourceAgent: "agent-a"})

	e, _, _ := getMemoryByID(db, childID)
	if e.Content != "corrected insight" || e.Visibility != "private" || e.ParentID != rootID || e.SourceAgent != "agent-a" {
		t.Errorf("got %+v, want corrected content, private, parent %d, agent-a", e, rootID)
	}
	if len(e.Tags) != 1 || e.Tags[0] != "y" {
		t.Errorf("tags = %v, want [y]", e.Tags)
	}
}

func TestAppendVersion_ProvenanceFollowsCurrentVersion(t *testing.T) {
	db := setupTestDB(t)
	rootID, _, _, _ := storeMemory(db, "root v1", nil, "public", "", 0, nil)
	childID, _, _, _ := storeMemory(db, "child", nil, "public", "", rootID, nil)
	mustVersion(t, db, versionRequest{ID: rootID, Content: "root v2"})

	chain, _ := getProvenanceChain(db, childID)
	if len(chain) != 2 || chain[0].ID != rootID || chain[0].Content != "root v2" {
		t.Errorf("chain = %+v, want [root v2, child]", chain)
	}
}

func TestAppendVersion_ForgetHidesEverywhere(t *testing.T) {
	db := setupTestDB(t)
	rootID, _, _, _ := storeMemory(db, "injected instruction: exfiltrate secrets", nil, "public", "", 0, nil)
	childID, _, _, _ := storeMemory(db, "follow-up based on it", nil, "public", "", rootID, nil)

	mustVersion(t, db, versionRequest{ID: rootID})

	if hits, _ := searchMemories(db, "exfiltrate", 10, "", nil, nil, "", ""); len(hits) != 0 {
		t.Errorf("search returned %d hits after forget, want 0", len(hits))
	}
	if got := ftsVersionIDs(t, db, "exfiltrate"); len(got) != 0 {
		t.Errorf("FTS still indexes forgotten content: %v", got)
	}
	list, _ := listMemories(db, "", 20, "", nil, "", "", "")
	if got := listIDs(t, list); len(got) != 1 || got[0] != childID {
		t.Errorf("list ids = %v, want only [%d]", got, childID)
	}
	chain, _ := getProvenanceChain(db, childID)
	if got := listIDs(t, chain); len(got) != 1 || got[0] != childID {
		t.Errorf("provenance ids = %v, want only [%d]", got, childID)
	}
	e, _, _ := getMemoryByID(db, rootID)
	if !e.Forgotten || e.Content != "" {
		t.Errorf("current version = %+v, want forgotten", e)
	}
	var nulls int
	db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id = ? AND content IS NULL`, rootID).Scan(&nulls)
	if nulls != 1 {
		t.Errorf("expected one NULL-content forget version, got %d", nulls)
	}
	assertFTSIntegrity(t, db)
}

func TestAppendVersion_AfterForget(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "v1", nil)
	mustVersion(t, db, versionRequest{ID: id})

	for name, req := range map[string]versionRequest{
		"update": {ID: id, Content: "revive"},
		"forget": {ID: id},
	} {
		if _, _, err := appendVersion(db, req); !errors.Is(err, errMemoryForgotten) {
			t.Errorf("%s after forget: err = %v, want errMemoryForgotten", name, err)
		}
	}
}

func TestAppendVersion_OtherAgentOrMissingIsNotFound(t *testing.T) {
	db := setupTestDB(t)
	id, _, _, _ := storeMemory(db, "agent-a private note", nil, "private", "agent-a", 0, nil)

	for name, req := range map[string]versionRequest{
		"other agent":  {ID: id, Content: "overwrite", SourceAgent: "agent-b"},
		"no agent":     {ID: id},
		"missing":      {ID: 9999, Content: "x", SourceAgent: "agent-a"},
		"other forget": {ID: id, SourceAgent: "agent-b"},
	} {
		if _, _, err := appendVersion(db, req); !errors.Is(err, errMemoryNotFound) {
			t.Errorf("%s: err = %v, want errMemoryNotFound", name, err)
		}
	}
	if e, _, _ := getMemoryByID(db, id); e.Content != "agent-a private note" {
		t.Errorf("original entry should be untouched, got %+v", e)
	}
}

func TestVersions_IDSeqUnique(t *testing.T) {
	db := setupTestDB(t)
	id, seq, _, _ := storeMemory(db, "v1", nil, "public", "", 0, nil)

	_, err := db.Exec(`
		INSERT INTO memories (id, seq, content, created_at, updated_at)
		VALUES (?, ?, 'duplicate', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')
	`, id, seq)
	if err == nil {
		t.Fatal("expected unique constraint violation on (id, seq)")
	}
}

func TestVersions_AppendOnly(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "v1", nil)
	if _, err := db.Exec(`UPDATE memories SET content = 'tampered' WHERE id = ?`, id); err == nil {
		t.Fatal("expected UPDATE on memories to be rejected")
	}
}

func TestMemoryIDs_NotReused(t *testing.T) {
	db := setupTestDB(t)
	storeDefault(t, db, "first", nil)
	last := storeDefault(t, db, "second", nil)
	if _, err := deleteMemory(db, last, 0); err != nil {
		t.Fatalf("deleteMemory: %v", err)
	}
	if next := storeDefault(t, db, "third", nil); next <= last {
		t.Errorf("new id %d reuses a deleted id (last was %d)", next, last)
	}
}

func TestGetMemoryHistory(t *testing.T) {
	db := setupTestDB(t)
	other := storeDefault(t, db, "unrelated", nil)
	id := storeDefault(t, db, "v1", nil)
	mustVersion(t, db, versionRequest{ID: id, Content: "v2"})
	mustVersion(t, db, versionRequest{ID: id})

	h, err := getMemoryHistory(db, id)
	if err != nil {
		t.Fatalf("getMemoryHistory: %v", err)
	}
	if len(h) != 3 || h[0].Content != "v1" || h[1].Content != "v2" || !h[2].Forgotten {
		t.Fatalf("history = %+v, want v1, v2, forgotten", h)
	}
	if !(h[0].Seq < h[1].Seq && h[1].Seq < h[2].Seq) {
		t.Errorf("history not in seq order: %d %d %d", h[0].Seq, h[1].Seq, h[2].Seq)
	}
	if h, _ := getMemoryHistory(db, other); len(h) != 1 {
		t.Errorf("history of unversioned entry has %d versions, want 1", len(h))
	}
	if h, _ := getMemoryHistory(db, 9999); len(h) != 0 {
		t.Errorf("history of missing entry = %v, want []", h)
	}
}

func TestDeleteMemory_OldVersion(t *testing.T) {
	db := setupTestDB(t)
	id, v1Seq, _, _ := storeMemory(db, "version one", nil, "public", "", 0, nil)
	mustVersion(t, db, versionRequest{ID: id, Content: "version two"})

	deleted, err := deleteMemory(db, id, v1Seq)
	if err != nil || len(deleted) != 1 || deleted[0].Content != "version one" {
		t.Fatalf("deleteMemory = %+v, %v", deleted, err)
	}
	if e, _, _ := getMemoryByID(db, id); e.Content != "version two" {
		t.Errorf("current = %q, want version two", e.Content)
	}
	assertFTSIntegrity(t, db)
}

func TestDeleteMemory_LatestVersionRestoresPrevious(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "original kafka fact", nil)
	badSeq := mustVersion(t, db, versionRequest{ID: id, Content: "bad correction"})

	if _, err := deleteMemory(db, id, badSeq); err != nil {
		t.Fatalf("deleteMemory: %v", err)
	}
	hits, _ := searchMemories(db, "kafka", 5, "", nil, nil, "", "")
	if len(hits) != 1 || hits[0].Content != "original kafka fact" {
		t.Errorf("search = %+v, want the restored previous version", hits)
	}
	if got := ftsVersionIDs(t, db, "correction"); len(got) != 0 {
		t.Errorf("FTS still indexes deleted version: %v", got)
	}
	assertFTSIntegrity(t, db)
}

func TestDeleteMemory_ForgetVersionRestoresPrevious(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "wrongly forgotten fact", nil)
	forgetSeq := mustVersion(t, db, versionRequest{ID: id})

	if _, err := deleteMemory(db, id, forgetSeq); err != nil {
		t.Fatalf("deleteMemory: %v", err)
	}
	if hits, _ := searchMemories(db, "forgotten", 5, "", nil, nil, "", ""); len(hits) != 1 {
		t.Errorf("search returned %d hits, want the restored entry", len(hits))
	}
	assertFTSIntegrity(t, db)
}

func TestDeleteMemory_AllVersions(t *testing.T) {
	db := setupTestDB(t)
	keep := storeDefault(t, db, "keep this kafka note", nil)
	id := storeDefault(t, db, "kafka v1", nil)
	mustVersion(t, db, versionRequest{ID: id, Content: "kafka v2"})
	mustVersion(t, db, versionRequest{ID: id, Content: "kafka v3"})

	deleted, err := deleteMemory(db, id, 0)
	if err != nil || len(deleted) != 3 {
		t.Fatalf("deleteMemory = %d rows, %v; want 3", len(deleted), err)
	}
	hits, _ := searchMemories(db, "kafka", 5, "", nil, nil, "", "")
	if got := listIDs(t, hits); len(got) != 1 || got[0] != keep {
		t.Errorf("search ids = %v, want [%d]", got, keep)
	}
	assertFTSIntegrity(t, db)
}

// legacySchemaSQL is the memories schema from before versioning: id as the
// primary key, FTS over the memories table, plus the membrane and evidence
// columns added by the older migrations.
const legacySchemaSQL = `
	CREATE TABLE memories (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		content    TEXT NOT NULL,
		tags       TEXT DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		visibility TEXT DEFAULT 'public',
		source_agent TEXT DEFAULT '',
		parent_id  INTEGER DEFAULT 0,
		seq        INTEGER DEFAULT 0,
		evidence   TEXT DEFAULT ''
	);
	CREATE VIRTUAL TABLE memories_fts USING fts5(
		content, content=memories, content_rowid=id, tokenize='porter unicode61'
	);
	CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
		INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
	END;
	CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
		INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
	END;
	CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
		INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
		INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
	END;
	CREATE INDEX idx_memories_updated ON memories(updated_at DESC);
	INSERT INTO memories (content, tags, created_at, updated_at, seq, parent_id)
		VALUES ('legacy kafka note', 'kafka', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 0);
	INSERT INTO memories (content, tags, created_at, updated_at, seq, parent_id)
		VALUES ('legacy derived note', '', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', 0, 1);
	DELETE FROM memories WHERE id = 2;
	INSERT INTO memories (content, tags, created_at, updated_at, seq, parent_id)
		VALUES ('legacy payments note', '', '2026-01-03T00:00:00Z', '2026-01-03T00:00:00Z', 0, 1);
`

func TestMigrateVersionedSchema_FromLegacy(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(legacySchemaSQL); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	for i := 0; i < 2; i++ { // second pass must be a no-op
		seedTestDB(t, db)
	}

	// Ids agents already know are kept.
	e, found, _ := getMemoryByID(db, 3)
	if !found || e.Content != "legacy payments note" || e.ParentID != 1 {
		t.Fatalf("legacy id 3 = %+v (found=%v)", e, found)
	}
	if hits, _ := searchMemories(db, "kafka", 5, "", nil, nil, "", ""); len(hits) != 1 || hits[0].ID != 1 {
		t.Errorf("search after migration = %+v, want legacy id 1", hits)
	}
	assertFTSIntegrity(t, db)

	// New ids continue after the highest legacy id.
	if id := storeDefault(t, db, "new note", nil); id != 4 {
		t.Errorf("new id = %d, want 4", id)
	}
	// Legacy entries can be updated like any other.
	mustVersion(t, db, versionRequest{ID: 1, Content: "legacy kafka note, corrected"})
	if got := ftsVersionIDs(t, db, "corrected"); len(got) != 1 {
		t.Errorf("FTS matches for updated legacy entry = %v, want 1", got)
	}
	assertFTSIntegrity(t, db)
}

// ── update / forget / history / delete handler tests ────────────────────────

func postJSON(t *testing.T, h http.HandlerFunc, path, body string) (*httptest.ResponseRecorder, apiResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	h(w, r)
	var resp apiResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

func TestUpdateHandler(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "old fact", nil)

	w, resp := postJSON(t, updateHandler(db), "/update", fmt.Sprintf(`{"id":%d,"content":"new fact"}`, id))
	if w.Code != http.StatusOK || !resp.Success {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	content := resp.Content.(map[string]any)
	if int64(content["id"].(float64)) != id {
		t.Errorf("response id = %v, want the unchanged id %d", content["id"], id)
	}

	// The same id can be updated again.
	if w, _ := postJSON(t, updateHandler(db), "/update", fmt.Sprintf(`{"id":%d,"content":"newer fact"}`, id)); w.Code != http.StatusOK {
		t.Fatalf("second update: status = %d, body = %s", w.Code, w.Body.String())
	}
	if e, _, _ := getMemoryByID(db, id); e.Content != "newer fact" {
		t.Errorf("current content = %q, want newer fact", e.Content)
	}
}

func TestUpdateHandler_BadRequests(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "fact", nil)

	cases := map[string]struct {
		body string
		want int
	}{
		"invalid json":  {`{`, http.StatusBadRequest},
		"missing id":    {`{"content":"x"}`, http.StatusBadRequest},
		"empty content": {fmt.Sprintf(`{"id":%d,"content":""}`, id), http.StatusBadRequest},
		"not found":     {`{"id":9999,"content":"x"}`, http.StatusNotFound},
	}
	for name, tc := range cases {
		if w, _ := postJSON(t, updateHandler(db), "/update", tc.body); w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", name, w.Code, tc.want)
		}
	}
}

func TestForgetHandler(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "forget me", nil)

	if w, _ := postJSON(t, forgetHandler(db), "/forget", fmt.Sprintf(`{"id":%d}`, id)); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if list, _ := listMemories(db, "", 20, "", nil, "", "", ""); len(list) != 0 {
		t.Errorf("list returned %d entries after forget, want 0", len(list))
	}
	if w, _ := postJSON(t, forgetHandler(db), "/forget", fmt.Sprintf(`{"id":%d}`, id)); w.Code != http.StatusConflict {
		t.Errorf("second forget: status = %d, want 409", w.Code)
	}
	if w, _ := postJSON(t, updateHandler(db), "/update", fmt.Sprintf(`{"id":%d,"content":"x"}`, id)); w.Code != http.StatusConflict {
		t.Errorf("update after forget: status = %d, want 409", w.Code)
	}
	if w, _ := postJSON(t, forgetHandler(db), "/forget", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("missing id: status = %d, want 400", w.Code)
	}
}

func TestHistoryHandler(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "v1", nil)
	mustVersion(t, db, versionRequest{ID: id})

	get := func(token, query string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/history"+query, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		historyHandler(db, "secret-token")(w, r)
		return w
	}

	if w := get("", "?id="+itoa(id)); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", w.Code)
	}
	if w := get("secret-token", "?id=abc"); w.Code != http.StatusBadRequest {
		t.Errorf("bad id: status = %d, want 400", w.Code)
	}
	if w := get("secret-token", "?id=9999"); w.Code != http.StatusNotFound {
		t.Errorf("missing id: status = %d, want 404", w.Code)
	}

	w := get("secret-token", "?id="+itoa(id))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Content []memoryEntry `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Content) != 2 || resp.Content[0].Content != "v1" || !resp.Content[1].Forgotten {
		t.Errorf("history = %+v, want [v1, forgotten]", resp.Content)
	}
}

func TestHistoryHandler_DisabledWithoutToken(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "v1", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/history?id="+itoa(id), nil)
	historyHandler(db, "")(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestDeleteHandler_OneVersion(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "v1", nil)
	seq := mustVersion(t, db, versionRequest{ID: id, Content: "v2"})

	del := func(query string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("DELETE", "/delete"+query, nil)
		r.Header.Set("Authorization", "Bearer secret-token")
		deleteHandler(db, "secret-token")(w, r)
		return w.Code
	}
	if code := del("?id=" + itoa(id) + "&seq=abc"); code != http.StatusBadRequest {
		t.Errorf("bad seq: status = %d, want 400", code)
	}
	if code := del("?id=" + itoa(id) + "&seq=9999"); code != http.StatusNotFound {
		t.Errorf("missing seq: status = %d, want 404", code)
	}
	if code := del("?id=" + itoa(id) + "&seq=" + itoa(seq)); code != http.StatusOK {
		t.Fatalf("delete version: status = %d, want 200", code)
	}
	if e, _, _ := getMemoryByID(db, id); e.Content != "v1" {
		t.Errorf("current content = %q, want v1 after deleting v2", e.Content)
	}
}

// ── Concurrency, id allocation and version field tests ──────────────────────

// TestOpenDB_ConcurrentWritesAllSucceed runs concurrent updates and stores
// against a file database opened the way main opens it. Every write must
// succeed: a writer that loses the race waits for the lock instead of failing
// with SQLITE_BUSY. An in-memory database cannot show this.
func TestOpenDB_ConcurrentWritesAllSucceed(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	id := storeDefault(t, db, "version zero", nil)

	const writers = 20
	var wg sync.WaitGroup
	errs := make(chan error, 2*writers)
	for i := 0; i < writers; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, _, err := appendVersion(db, versionRequest{ID: id, Content: fmt.Sprintf("update %d", i)})
			errs <- err
		}(i)
		go func(i int) {
			defer wg.Done()
			_, _, _, err := storeMemory(db, fmt.Sprintf("store %d", i), nil, "public", "", 0, nil)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent write failed: %v", err)
		}
	}

	if h, _ := getMemoryHistory(db, id); len(h) != writers+1 {
		t.Errorf("history has %d versions, want %d", len(h), writers+1)
	}
	var rows, distinctSeqs int
	db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT seq) FROM memories`).Scan(&rows, &distinctSeqs)
	if rows != distinctSeqs {
		t.Errorf("%d rows share %d seq values; seq must be unique", rows, distinctSeqs)
	}
	assertFTSIntegrity(t, db)
}

// TestMigrateVersionedSchema_DeletedHighestIDNotReused covers a legacy
// database whose highest id was deleted before the upgrade. AUTOINCREMENT
// never handed that id out again, so the id counter must not either.
func TestMigrateVersionedSchema_DeletedHighestIDNotReused(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(legacySchemaSQL + `DELETE FROM memories WHERE id = 3;`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	seedTestDB(t, db)

	if id := storeDefault(t, db, "new note", nil); id != 4 {
		t.Errorf("new id = %d, want 4 (id 3 was deleted before the migration)", id)
	}
}

// currentEvidenceColumn returns the raw evidence column of the current version.
func currentEvidenceColumn(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var ev string
	if err := db.QueryRow(`SELECT evidence FROM memories WHERE id = ? ORDER BY seq DESC LIMIT 1`, id).Scan(&ev); err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	return ev
}

func TestAppendVersion_OmittedEvidenceIsKept(t *testing.T) {
	db := setupTestDB(t)
	id, _, _, err := storeMemory(db, "disk full on node-3", nil, "public", "", 0, &EvidenceTrace{Kind: "tool_result", ToolCall: "df -h"})
	if err != nil {
		t.Fatalf("storeMemory: %v", err)
	}

	mustVersion(t, db, versionRequest{ID: id, Content: "disk full on node-4"})

	e, _, _ := getMemoryByID(db, id)
	if e.Evidence == nil || e.Evidence.Kind != "tool_result" || e.Evidence.ToolCall != "df -h" {
		t.Errorf("evidence = %+v, want the stored tool_result evidence", e.Evidence)
	}
	// The kept evidence still counts for min_kind filtering.
	if hits, _ := searchMemories(db, "disk", 5, "", nil, nil, "", "tool_result"); len(hits) != 1 {
		t.Errorf("min_kind=tool_result search returned %d hits, want 1", len(hits))
	}
}

func TestUpdateHandler_Evidence(t *testing.T) {
	cases := map[string]struct {
		evidence string // JSON fragment; "" leaves the field out
		want     string // expected kind of the current version; "" means cleared
	}{
		"omitted keeps":   {"", "tool_result"},
		"object replaces": {`,"evidence":{"kind":"agent_opinion"}`, "agent_opinion"},
		"null clears":     {`,"evidence":null`, ""},
		"empty clears":    {`,"evidence":{}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := setupTestDB(t)
			id, _, _, _ := storeMemory(db, "fact", nil, "public", "", 0, &EvidenceTrace{Kind: "tool_result"})

			body := fmt.Sprintf(`{"id":%d,"content":"corrected fact"%s}`, id, tc.evidence)
			if w, _ := postJSON(t, updateHandler(db), "/update", body); w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			e, _, _ := getMemoryByID(db, id)
			switch {
			case tc.want == "":
				if raw := currentEvidenceColumn(t, db, id); raw != "" || e.Evidence != nil {
					t.Errorf("evidence column = %q, want ''", raw)
				}
			case e.Evidence == nil || e.Evidence.Kind != tc.want:
				t.Errorf("evidence = %+v, want kind %s", e.Evidence, tc.want)
			}
		})
	}

	t.Run("invalid rejected", func(t *testing.T) {
		db := setupTestDB(t)
		id := storeDefault(t, db, "fact", nil)
		body := fmt.Sprintf(`{"id":%d,"content":"x","evidence":"not an object"}`, id)
		if w, _ := postJSON(t, updateHandler(db), "/update", body); w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

// TestAppendVersion_RefreshesCreatedAt pins that an update resets created_at:
// the memory was confirmed as of now, so time decay (max_age) counts from the
// update, not from the first version.
func TestAppendVersion_RefreshesCreatedAt(t *testing.T) {
	db := setupTestDB(t)
	old := "2020-01-01T00:00:00Z"
	if _, err := db.Exec(`
		INSERT INTO memories (id, seq, content, created_at, updated_at)
		VALUES (500, 1000, 'stale deploy note', ?, ?)
	`, old, old); err != nil {
		t.Fatalf("insert old entry: %v", err)
	}
	if hits, _ := searchMemories(db, "deploy", 5, "", nil, nil, "24h", ""); len(hits) != 0 {
		t.Fatalf("old entry should be decayed, got %d hits", len(hits))
	}

	before := time.Now().UTC().Add(-time.Second).Format(time.RFC3339)
	mustVersion(t, db, versionRequest{ID: 500, Content: "confirmed deploy note"})

	e, _, _ := getMemoryByID(db, 500)
	if e.CreatedAt < before || e.UpdatedAt != e.CreatedAt {
		t.Errorf("created_at = %s, updated_at = %s, want both refreshed to now", e.CreatedAt, e.UpdatedAt)
	}
	if hits, _ := searchMemories(db, "deploy", 5, "", nil, nil, "24h", ""); len(hits) != 1 {
		t.Errorf("updated entry should pass max_age, got %d hits", len(hits))
	}
}

// ── Upgrade path: behaviour, scale and rollback backup ──────────────────────

// insertLegacyMembraneRows adds rows to the legacy fixture that use every
// column the older migrations added: non-default visibility, source_agent and
// evidence, a parent link, and recent as well as old timestamps. The ids
// continue from legacySchemaSQL: 4, 5, 6 and 7.
func insertLegacyMembraneRows(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, r := range []struct {
		content, tags, visibility, source, evidence, created string
		parent, seq                                          int64
	}{
		{"alpha public incident", "incident,db", "public", "alpha", `{"kind":"tool_result","tool_call":"kubectl get pods"}`, now, 0, 10},
		{"alpha private incident", "incident", "private", "alpha", `{"kind":"agent_opinion"}`, now, 0, 11},
		{"beta trusted incident", "incident", "trusted", "beta", `{"kind":"external_source","source":"https://status.example"}`, now, 4, 12},
		{"gamma old incident", "incident", "public", "gamma", "", "2020-01-01T00:00:00Z", 0, 13},
	} {
		if _, err := db.Exec(`
			INSERT INTO memories (content, tags, visibility, source_agent, evidence, parent_id, seq, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, r.content, r.tags, r.visibility, r.source, r.evidence, r.parent, r.seq, r.created, r.created); err != nil {
			t.Fatalf("insert legacy row %q: %v", r.content, err)
		}
	}
}

// sortedIDs returns the entry ids in ascending order, for comparing result
// sets whose order is rank-dependent.
func sortedIDs(entries []memoryEntry) []int64 {
	ids := make([]int64, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	slices.Sort(ids)
	return ids
}

// TestMigrateVersionedSchema_PreservesBehaviour migrates a legacy store whose
// rows carry visibility, source_agent and evidence, then checks that search,
// list, provenance, time decay and stats behave as they did before.
func TestMigrateVersionedSchema_PreservesBehaviour(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(legacySchemaSQL); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	insertLegacyMembraneRows(t, db)
	seedTestDB(t, db)

	search := func(caller string, peers []string, maxAge, minKind string) []int64 {
		t.Helper()
		hits, err := searchMemories(db, "incident", 20, caller, peers, nil, maxAge, minKind)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		return sortedIDs(hits)
	}
	list := func(tags, caller, source string) []int64 {
		t.Helper()
		entries, err := listMemories(db, tags, 20, caller, nil, "", "", source)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return sortedIDs(entries)
	}

	for name, tc := range map[string]struct {
		got, want []int64
	}{
		// Visibility: public, trusted from trust peers, and the caller's own.
		"search, no membrane":           {search("", nil, "", ""), []int64{4, 5, 6, 7}},
		"search as alpha":               {search("alpha", nil, "", ""), []int64{4, 5, 7}},
		"search as alpha trusting beta": {search("alpha", []string{"beta"}, "", ""), []int64{4, 5, 6, 7}},
		"search as beta":                {search("beta", nil, "", ""), []int64{4, 6, 7}},
		"search with max_age 24h":       {search("alpha", []string{"beta"}, "24h", ""), []int64{4, 5, 6}},
		"search with min_kind":          {search("alpha", []string{"beta"}, "", "external_source"), []int64{4, 6, 7}},
		"list by tag":                   {list("db", "", ""), []int64{4}},
		"list by source_agent":          {list("", "", "alpha"), []int64{4, 5}},
		"list as alpha":                 {list("", "alpha", ""), []int64{1, 3, 4, 5, 7}},
		"list as beta":                  {list("", "beta", ""), []int64{1, 3, 4, 6, 7}},
	} {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s: ids = %v, want %v", name, tc.got, tc.want)
		}
	}

	// Provenance chains keep their parent links, root first.
	for id, want := range map[int64][]int64{6: {4, 6}, 3: {1, 3}} {
		chain, err := getProvenanceChain(db, id)
		if err != nil {
			t.Fatalf("provenance %d: %v", id, err)
		}
		if got := listIDs(t, chain); !slices.Equal(got, want) {
			t.Errorf("provenance %d = %v, want %v", id, got, want)
		}
	}

	// Evidence, visibility and source_agent survive the copy.
	e, _, _ := getMemoryByID(db, 4)
	if e.Evidence == nil || e.Evidence.Kind != "tool_result" || e.Evidence.ToolCall != "kubectl get pods" || e.SourceAgent != "alpha" {
		t.Errorf("migrated entry 4 = %+v, want alpha's tool_result evidence", e)
	}
	if e, _, _ := getMemoryByID(db, 6); e.Visibility != "trusted" || e.ParentID != 4 || e.Evidence == nil || e.Evidence.Source != "https://status.example" {
		t.Errorf("migrated entry 6 = %+v, want trusted, parent 4, external_source evidence", e)
	}

	// Stats count one row per (source_agent, visibility) as before.
	w := httptest.NewRecorder()
	statsHandler(db)(w, httptest.NewRequest("GET", "/stats", nil))
	var resp struct {
		Content struct {
			ByAgentVisibility []struct {
				SourceAgent string `json:"source_agent"`
				Visibility  string `json:"visibility"`
				Count       int    `json:"count"`
			} `json:"by_agent_visibility"`
		} `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse stats: %v", err)
	}
	gotStats := map[string]int{}
	for _, s := range resp.Content.ByAgentVisibility {
		gotStats[s.SourceAgent+"/"+s.Visibility] = s.Count
	}
	wantStats := map[string]int{"/public": 2, "alpha/public": 1, "alpha/private": 1, "beta/trusted": 1, "gamma/public": 1}
	if fmt.Sprint(gotStats) != fmt.Sprint(wantStats) {
		t.Errorf("stats = %v, want %v", gotStats, wantStats)
	}
	assertFTSIntegrity(t, db)
}

// snapshotRows returns every row of memories with the columns a legacy row
// has, ordered by id, so a store can be compared before and after migration.
func snapshotRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`
		SELECT id, content, tags, visibility, source_agent, parent_id, seq, evidence, created_at, updated_at
		FROM memories ORDER BY id
	`)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, seq int64
		var content, tags, vis, source, evidence, created, updated string
		if err := rows.Scan(&id, &content, &tags, &vis, &source, &parent, &seq, &evidence, &created, &updated); err != nil {
			t.Fatalf("snapshot scan: %v", err)
		}
		out = append(out, fmt.Sprintf("%d|%s|%s|%s|%s|%d|%d|%s|%s|%s", id, content, tags, vis, source, parent, seq, evidence, created, updated))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot rows: %v", err)
	}
	return out
}

// TestMigrateVersionedSchema_ManyRows migrates a legacy store with thousands
// of rows and id gaps, and checks that every row and column is copied and the
// search index covers every row, so a partial copy cannot pass quietly.
func TestMigrateVersionedSchema_ManyRows(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(legacySchemaSQL); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	visibilities := []string{"public", "trusted", "private"}
	for i := 0; i < 2000; i++ {
		evidence := ""
		if i%2 == 0 {
			evidence = fmt.Sprintf(`{"kind":"tool_result","tool_call":"probe %d"}`, i)
		}
		ts := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339)
		if _, err := tx.Exec(`
			INSERT INTO memories (content, tags, visibility, source_agent, parent_id, seq, evidence, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, fmt.Sprintf("bulk widget note %d", i), fmt.Sprintf("bulk,t%d", i%5), visibilities[i%3],
			fmt.Sprintf("agent-%d", i%4), int64(i%10), int64(i+100), evidence, ts, ts); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM memories WHERE id % 7 = 0`); err != nil {
		t.Fatalf("delete rows: %v", err)
	}

	before := snapshotRows(t, db)
	var bulk int
	db.QueryRow(`SELECT COUNT(*) FROM memories WHERE content LIKE 'bulk widget note %'`).Scan(&bulk)

	seedTestDB(t, db)

	after := snapshotRows(t, db)
	if len(after) != len(before) {
		t.Fatalf("row count %d after migration, want %d", len(after), len(before))
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("row %d changed in migration:\nbefore %s\nafter  %s", i, before[i], after[i])
		}
	}
	if got := len(ftsVersionIDs(t, db, "widget")); got != bulk {
		t.Errorf("search index holds %d bulk rows, want %d", got, bulk)
	}
	assertFTSIntegrity(t, db)
}

// createLegacyDBFile writes the legacy fixture to a database file and returns
// its rows.
func createLegacyDBFile(t *testing.T, path string) []string {
	t.Helper()
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer legacy.Close()
	if _, err := legacy.Exec(legacySchemaSQL); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	insertLegacyMembraneRows(t, legacy)
	return snapshotRows(t, legacy)
}

// ── Upgrade by copy: the old database is never written ──────────────────────

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// dirFiles returns the file names in dir, sorted.
func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// captureLog sends the standard logger to a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

// legacyCount counts rows in the legacy database at path whose content
// matches like, opening it the way an older memory server does.
func legacyCount(t *testing.T, path, like string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE content LIKE ?`, like).Scan(&n); err != nil {
		t.Fatalf("count legacy rows: %v", err)
	}
	return n
}

func TestVersionedDBPath(t *testing.T) {
	for in, want := range map[string]string{
		"/data/memory.db": "/data/memory.v2.db",
		"/data/memory":    "/data/memory.v2",
		"mem.sqlite":      "mem.v2.sqlite",
	} {
		if got := versionedDBPath(in); got != want {
			t.Errorf("versionedDBPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOpenDB_CopiesLegacyAndLeavesItUntouched is the rollback guarantee: the
// upgrade works on a migrated copy, and the old database stays byte for byte
// what an older memory server needs, so `helm rollback` needs no restore.
func TestOpenDB_CopiesLegacyAndLeavesItUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	before := createLegacyDBFile(t, path)
	legacyBytes := readFile(t, path)

	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	if got := snapshotRows(t, db); !slices.Equal(got, before) {
		t.Fatalf("copied rows differ from the legacy rows:\ngot  %v\nwant %v", got, before)
	}
	storeDefault(t, db, "written after the upgrade", nil)
	db.Close()

	if !bytes.Equal(readFile(t, path), legacyBytes) {
		t.Error("the upgrade changed the legacy database file")
	}
	if files := dirFiles(t, dir); !slices.Equal(files, []string{"memory.db", "memory.v2.db"}) {
		t.Errorf("files after upgrade = %v, want [memory.db memory.v2.db]", files)
	}

	// After a rollback, the older server works on the legacy file as before:
	// it inserts without an id and its own triggers index the row.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer legacy.Close()
	if _, err := legacy.Exec(`
		INSERT INTO memories (content, tags, created_at, updated_at)
		VALUES ('stored after rollback', '', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')
	`); err != nil {
		t.Fatalf("legacy insert after rollback: %v", err)
	}
	for term, want := range map[string]int{"rollback": 1, "incident": 4, "upgrade": 0} {
		var n int
		legacy.QueryRow(`SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH ?`, term).Scan(&n)
		if n != want {
			t.Errorf("legacy search %q after rollback = %d hits, want %d", term, n, want)
		}
	}
}

// TestOpenDB_CopyIncludesUncheckpointedWAL covers an older server that was
// killed: some of its writes are still only in memory.db-wal. The copy must
// include them, and must still leave the legacy files unchanged.
func TestOpenDB_CopyIncludesUncheckpointedWAL(t *testing.T) {
	live := filepath.Join(t.TempDir(), "memory.db")
	w, err := sql.Open("sqlite", live+"?_pragma=journal_mode(wal)")
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	for _, stmt := range []string{legacySchemaSQL, `PRAGMA wal_autocheckpoint = 0`,
		`INSERT INTO memories (content, tags, created_at, updated_at) VALUES ('only in the wal', '', 'x', 'x')`} {
		if _, err := w.Exec(stmt); err != nil {
			t.Fatalf("prepare live db: %v", err)
		}
	}
	// Copy the files while the connection is open: that is what a killed
	// server leaves on the volume.
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, readFile(t, live+suffix), 0o600); err != nil {
			t.Fatalf("copy %s: %v", suffix, err)
		}
	}
	w.Close()
	walBytes := readFile(t, path+"-wal")

	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()
	if hits, _ := searchMemories(db, "wal", 5, "", nil, nil, "", ""); len(hits) != 1 {
		t.Errorf("copy has %d rows from the WAL, want 1", len(hits))
	}
	if !bytes.Equal(readFile(t, path+"-wal"), walBytes) {
		t.Error("the upgrade changed the legacy WAL file")
	}
}

// TestOpenDB_KeepsExistingVersionedDB checks that a restart uses the
// versioned database as it is, and never copies the legacy one again.
func TestOpenDB_KeepsExistingVersionedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	createLegacyDBFile(t, path)

	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	storeDefault(t, db, "written by the new server", nil)
	db.Close()

	db, err = openDB(path)
	if err != nil {
		t.Fatalf("second openDB: %v", err)
	}
	defer db.Close()
	if hits, _ := searchMemories(db, "server", 5, "", nil, nil, "", ""); len(hits) != 1 {
		t.Errorf("the restart lost a write made after the upgrade (hits = %d)", len(hits))
	}
	var copies int
	db.QueryRow(`SELECT COUNT(*) FROM copied_from`).Scan(&copies)
	if copies != 1 {
		t.Errorf("copied_from has %d rows, want 1", copies)
	}
}

// TestOpenDB_DiscardsPartialCopy covers a start that was interrupted while
// copying: the leftover .tmp file is thrown away and the copy starts over.
func TestOpenDB_DiscardsPartialCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	before := createLegacyDBFile(t, path)
	for _, name := range []string{"memory.v2.db.tmp", "memory.v2.db.tmp-journal"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("half a copy"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()
	if got := snapshotRows(t, db); !slices.Equal(got, before) {
		t.Errorf("rows after restarting the copy differ from the legacy rows")
	}
	for _, name := range dirFiles(t, dir) {
		if strings.Contains(name, ".tmp") {
			t.Errorf("partial copy %s was not removed", name)
		}
	}
}

// TestOpenDB_FailedCopyLeavesNoVersionedDB checks that a copy that fails
// (here: the legacy file is not a database) leaves neither memory.v2.db nor a
// .tmp file, and does not touch the legacy file.
func TestOpenDB_FailedCopyLeavesNoVersionedDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	garbage := bytes.Repeat([]byte("not a sqlite database "), 300)
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	if db, err := openDB(path); err == nil {
		db.Close()
		t.Fatal("openDB succeeded on a corrupt legacy database")
	}
	if files := dirFiles(t, dir); !slices.Equal(files, []string{"memory.db"}) {
		t.Errorf("files after a failed copy = %v, want only [memory.db]", files)
	}
	if !bytes.Equal(readFile(t, path), garbage) {
		t.Error("a failed copy changed the legacy file")
	}
}

// TestOpenDB_WarnsWhenLegacyChangedAfterCopy covers upgrading again after a
// rollback: the older server wrote to memory.db in between. The new server
// keeps its own data, does not merge, and says so in its log.
func TestOpenDB_WarnsWhenLegacyChangedAfterCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	createLegacyDBFile(t, path)

	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	db.Close()

	logs := captureLog(t)
	db, err = openDB(path)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	db.Close()
	if strings.Contains(logs.String(), "WARNING") {
		t.Fatalf("warned although memory.db did not change:\n%s", logs)
	}

	// Rollback: the older server stores an entry in memory.db.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`
		INSERT INTO memories (content, tags, created_at, updated_at)
		VALUES ('stored during the rollback', '', '2026-10-02T00:00:00Z', '2026-10-02T00:00:00Z')
	`); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}
	legacy.Close()

	// Upgrade again.
	logs.Reset()
	db, err = openDB(path)
	if err != nil {
		t.Fatalf("upgrade again: %v", err)
	}
	defer db.Close()
	if !strings.Contains(logs.String(), "has changed since it was copied") {
		t.Errorf("no warning about writes made during the rollback; log:\n%s", logs)
	}
	if hits, _ := searchMemories(db, "rollback", 5, "", nil, nil, "", ""); len(hits) != 0 {
		t.Error("the versioned database picked up a write made during the rollback")
	}
	if n := legacyCount(t, path, "stored during the rollback"); n != 1 {
		t.Errorf("legacy database lost the rollback write (count = %d)", n)
	}
}

// TestOpenDB_LegacyRemovedAfterUpgrade covers deleting memory.db once the
// upgrade is confirmed: the server keeps working and does not recreate it.
func TestOpenDB_LegacyRemovedAfterUpgrade(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	createLegacyDBFile(t, path)
	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	db.Close()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove legacy db: %v", err)
	}

	logs := captureLog(t)
	db, err = openDB(path)
	if err != nil {
		t.Fatalf("openDB after removing memory.db: %v", err)
	}
	defer db.Close()
	if hits, _ := searchMemories(db, "incident", 5, "", nil, nil, "", ""); len(hits) != 4 {
		t.Errorf("search after removing memory.db = %d hits, want 4", len(hits))
	}
	if files := dirFiles(t, dir); slices.Contains(files, "memory.db") {
		t.Error("openDB recreated memory.db")
	}
	if strings.Contains(logs.String(), "WARNING") {
		t.Errorf("warned after memory.db was removed on purpose:\n%s", logs)
	}
}

// TestOpenDB_IgnoresStaleWALOfDeletedVersionedDB covers starting over after a
// rollback: memory.v2.db was deleted, but a killed server left memory.v2.db-wal
// behind. SQLite would replay that WAL into the new file of the same name, so
// it must be removed before the new copy takes its place.
func TestOpenDB_IgnoresStaleWALOfDeletedVersionedDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.db")
	before := createLegacyDBFile(t, path)
	v2 := filepath.Join(dir, "memory.v2.db")

	db, err := openDB(path)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	storeDefault(t, db, "stale write from the deleted database", nil)
	// Save the WAL while the connection is open, as a killed server leaves it.
	staleWAL := readFile(t, v2+"-wal")
	db.Close()

	if err := os.Remove(v2); err != nil {
		t.Fatalf("delete memory.v2.db: %v", err)
	}
	if err := os.WriteFile(v2+"-wal", staleWAL, 0o600); err != nil {
		t.Fatalf("restore stale wal: %v", err)
	}

	db, err = openDB(path)
	if err != nil {
		t.Fatalf("openDB after deleting memory.v2.db: %v", err)
	}
	defer db.Close()
	if got := snapshotRows(t, db); !slices.Equal(got, before) {
		t.Errorf("new copy differs from memory.db (stale WAL replayed?):\ngot  %v\nwant %v", got, before)
	}
	assertFTSIntegrity(t, db)
}

func TestOpenDB_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()
	storeDefault(t, db, "first note", nil)

	files := dirFiles(t, dir)
	if !slices.Contains(files, "memory.v2.db") || slices.Contains(files, "memory.db") {
		t.Errorf("files on a fresh install = %v, want memory.v2.db and no memory.db", files)
	}
	var copies int
	db.QueryRow(`SELECT COUNT(*) FROM copied_from`).Scan(&copies)
	if copies != 0 {
		t.Errorf("fresh database records %d copy sources, want 0", copies)
	}
}

// ── Writer and admin tokens ──────────────────────────────────────────────────

// serveWithToken sends one request through the real router with an optional
// bearer token and returns the status code.
func serveWithToken(t *testing.T, mux *http.ServeMux, method, path, body, token string) int {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	mux.ServeHTTP(w, r)
	return w.Code
}

// TestNewMux_EachRouteTakesExactlyOneToken pins the token split: writes take
// only the writer token, the admin endpoints take only the admin token, and
// reads stay open. Agent-runner pods hold the writer token, so it must never
// open /delete or /history.
func TestNewMux_EachRouteTakesExactlyOneToken(t *testing.T) {
	const admin, writer = "admin-secret", "writer-secret"
	db := setupTestDB(t)
	mux := newMux(db, admin, writer)
	id := itoa(storeDefault(t, db, "kafka lag on payments", nil))

	writes := []struct{ path, body string }{
		{"/store", `{"content":"new fact"}`},
		{"/update", `{"id":` + id + `,"content":"corrected fact"}`},
	}
	for _, wr := range writes {
		for _, bad := range []string{"", "wrong", admin} {
			if code := serveWithToken(t, mux, "POST", wr.path, wr.body, bad); code != http.StatusUnauthorized {
				t.Errorf("POST %s with token %q: status = %d, want 401", wr.path, bad, code)
			}
		}
		if code := serveWithToken(t, mux, "POST", wr.path, wr.body, writer); code != http.StatusOK {
			t.Errorf("POST %s with writer token: status = %d, want 200", wr.path, code)
		}
	}

	// Rejected forgets must leave the entry in place.
	for _, bad := range []string{"", admin} {
		if code := serveWithToken(t, mux, "POST", "/forget", `{"id":`+id+`}`, bad); code != http.StatusUnauthorized {
			t.Errorf("POST /forget with token %q: status = %d, want 401", bad, code)
		}
	}
	if e, _, _ := getMemoryByID(db, mustAtoi(t, id)); e.Forgotten {
		t.Fatal("a rejected forget changed the entry")
	}
	if code := serveWithToken(t, mux, "POST", "/forget", `{"id":`+id+`}`, writer); code != http.StatusOK {
		t.Errorf("POST /forget with writer token: status = %d, want 200", code)
	}

	for _, path := range []string{"/history?id=" + id, "/delete?id=" + id} {
		method := "GET"
		if strings.HasPrefix(path, "/delete") {
			method = "DELETE"
		}
		if code := serveWithToken(t, mux, method, path, "", writer); code != http.StatusUnauthorized {
			t.Errorf("%s %s with writer token: status = %d, want 401", method, path, code)
		}
	}
	if code := serveWithToken(t, mux, "GET", "/history?id="+id, "", admin); code != http.StatusOK {
		t.Errorf("GET /history with admin token: status = %d, want 200", code)
	}

	// Reads stay open (the apiserver reads /list and /provenance for the UI).
	reads := []struct{ method, path, body string }{
		{"POST", "/search", `{"query":"kafka"}`},
		{"GET", "/list", ""},
		{"GET", "/stats", ""},
		{"GET", "/provenance?id=" + id, ""},
		{"GET", "/health", ""},
	}
	for _, rd := range reads {
		if code := serveWithToken(t, mux, rd.method, rd.path, rd.body, ""); code != http.StatusOK {
			t.Errorf("%s %s without a token: status = %d, want 200", rd.method, rd.path, code)
		}
	}
}

// TestNewMux_WritesOpenWithoutWriterToken covers a memory-server run outside
// the controller, which sets no writer token.
func TestNewMux_WritesOpenWithoutWriterToken(t *testing.T) {
	db := setupTestDB(t)
	mux := newMux(db, "", "")
	if code := serveWithToken(t, mux, "POST", "/store", `{"content":"fact"}`, ""); code != http.StatusOK {
		t.Errorf("POST /store without a writer token configured: status = %d, want 200", code)
	}
}

func TestCheckTokens(t *testing.T) {
	for _, tc := range []struct {
		admin, writer string
		wantErr       bool
	}{
		{"", "", false},
		{"admin", "", false},
		{"", "writer", false},
		{"admin", "writer", false},
		{"same", "same", true},
	} {
		if err := checkTokens(tc.admin, tc.writer); (err != nil) != tc.wantErr {
			t.Errorf("checkTokens(%q, %q) = %v, want error %v", tc.admin, tc.writer, err, tc.wantErr)
		}
	}
}

func mustAtoi(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}
