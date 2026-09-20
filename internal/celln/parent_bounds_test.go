package celln

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boundsClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("parent-bounds-test-credential-long"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{BaseURL: server.URL, TokenFile: token})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// The bounds mirror the Celln host's parent protocol.
func TestParentBoundsMirrorTheHost(t *testing.T) {
	if MaxParentMessageBytes != 2048 || MaxParentAnswerBytes != 8192 || MaxParentResponseBytes != 131072 {
		t.Fatalf("bounds: message %d answer %d response %d", MaxParentMessageBytes, MaxParentAnswerBytes, MaxParentResponseBytes)
	}
}

// A committed answer may fill the 8192-byte bound; one byte more is an
// outcome to reconcile, never a truncated answer. The message keeps 2048.
func TestParentAnswerBound(t *testing.T) {
	id := "blake3:" + strings.Repeat("a", 64)
	for name, tc := range map[string]struct {
		answer int
		ok     bool
	}{
		"old bound":      {2048, true},
		"past old bound": {2049, true},
		"at bound":       {8192, true},
		"past bound":     {8193, false},
	} {
		t.Run(name, func(t *testing.T) {
			client := boundsClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"kind": "completed", "apiVersion": "celln.parent-context/v1", "turnId": "turn-1", "succeeded": true, "answer": strings.Repeat("a", tc.answer)})
			})
			result, err := client.SubmitParentTurn(context.Background(), id, "turn-1", "hello")
			if tc.ok && (err != nil || len(result.Answer) != tc.answer) {
				t.Fatalf("%d-byte answer refused: %v", tc.answer, err)
			}
			if !tc.ok && (!errors.Is(err, ErrReconcile) || result.Answer != "") {
				t.Fatalf("%d-byte answer accepted: %v", tc.answer, err)
			}
		})
	}
	called := false
	client := boundsClient(t, func(http.ResponseWriter, *http.Request) { called = true })
	if _, err := client.SubmitParentTurn(context.Background(), id, "turn-1", strings.Repeat("m", 2049)); err == nil || called {
		t.Fatalf("2049-byte message sent: %v", err)
	}
}

// A turn-status response carries the journal reservation, which holds the
// whole worker task: tens of kilobytes. It is read whole up to the response
// bound and refused beyond it.
func TestParentTurnStatusResponseBound(t *testing.T) {
	id := "blake3:" + strings.Repeat("a", 64)
	child := "blake3:" + strings.Repeat("c", 64)
	body := func(total int) []byte {
		t.Helper()
		build := func(task string) []byte {
			record := map[string]any{"version": 1, "parent": id, "child": child, "request": map[string]any{"apiVersion": "celln.parent-turn/v1", "turnId": "turn-1", "task": task}}
			raw, err := json.Marshal(map[string]any{"turn": map[string]any{"stage": "reserved", "record": record}, "retryAuthorized": false, "ownerStatus": "TurnActive"})
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}
		raw := build(strings.Repeat("t", total-len(build(""))))
		if len(raw) != total {
			t.Fatalf("built %d bytes, want %d", len(raw), total)
		}
		return raw
	}
	for name, tc := range map[string]struct {
		size int
		ok   bool
	}{
		"past the old 32768 bound": {32769, true},
		"100 KB":                   {100 * 1024, true},
		"at bound":                 {MaxParentResponseBytes, true},
		"past bound":               {MaxParentResponseBytes + 1, false},
	} {
		t.Run(name, func(t *testing.T) {
			raw := body(tc.size)
			client := boundsClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) })
			evidence, err := client.ParentTurn(context.Background(), id, "turn-1")
			if tc.ok && (err != nil || evidence.Turn.Stage != "reserved" || evidence.OwnerStatus != "TurnActive") {
				t.Fatalf("%d-byte turn status refused: %v", tc.size, err)
			}
			if !tc.ok && !errors.Is(err, ErrReconcile) {
				t.Fatalf("%d-byte turn status accepted: %v", tc.size, err)
			}
		})
	}
}
