package cellnparent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Launched only by Celln's opted-in KVM/model proof with a freshly provisioned
// launch. No httptest server or fabricated responses. This tests the real Go
// client, not a Kubernetes controller deployment.
func TestLiveCellnParentClient(t *testing.T) {
	origin := os.Getenv("CELLN_INTEROP_ORIGIN")
	if origin == "" {
		t.Skip("requires Celln parent hardware proof launcher")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if os.Getenv("CELLN_INTEROP_COORDINATOR") == "1" {
		proveLiveCoordinator(t, ctx)
		return
	}
	c, err := New(Options{URL: origin, TokenFile: os.Getenv("CELLN_INTEROP_TOKEN_FILE")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id := os.Getenv("CELLN_INTEROP_PARENT")
	if err := c.Create(ctx, os.Getenv("CELLN_INTEROP_LAUNCH"), id); err != nil {
		t.Fatalf("real parent create: %v", err)
	}
	for {
		status, err := c.Status(ctx, id)
		if err != nil {
			t.Fatalf("real parent status: %v", err)
		}
		if status.Live && status.Status == "Ready" {
			break
		}
		if status.Status != "Initializing" {
			t.Fatalf("unexpected startup state: %s", status.Status)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	messages := []string{"My value is violet. Remember it.", "What was my original value? Reply with that value only."}
	if os.Getenv("CELLN_INTEROP_BORROWED") == "true" {
		messages = []string{"My value is violet. Call uppercase with my value and answer only the tool result text.", "Find the value stated in the first user message of this conversation. Call uppercase on that value now. Answer only the tool result text."}
	}
	results := make([]TurnResult, 0, 2)
	children := map[string]bool{}
	for index, turn := range []string{"one", "two"} {
		result, err := c.Submit(ctx, id, turn, messages[index])
		if err != nil {
			t.Fatalf("real turn submission %s: %v (no retry)", turn, err)
		}
		for {
			evidence, err := c.Turn(ctx, id, turn)
			if err != nil {
				t.Fatalf("real turn evidence: %v", err)
			}
			if evidence.Turn.Stage == "parent-committed" {
				var committed struct {
					Child     string `json:"child"`
					Succeeded bool   `json:"succeeded"`
					Answer    string `json:"answer"`
				}
				if err := json.Unmarshal(evidence.Turn.Record, &committed); err != nil {
					t.Fatal(err)
				}
				if committed.Child != childIdentity(id, turn) || children[committed.Child] || !committed.Succeeded {
					t.Fatal("invalid child identity or unsuccessful commit")
				}
				children[committed.Child] = true
				if result.Pending {
					result = TurnResult{Kind: "completed", Version: "celln.parent-context/v1", TurnID: turn, Succeeded: &committed.Succeeded, Answer: committed.Answer}
				}
				if result.Succeeded == nil || !*result.Succeeded || result.Answer != committed.Answer {
					t.Fatal("response differs from committed answer")
				}
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
		if index == 1 && !strings.Contains(strings.ToLower(result.Answer), "violet") {
			t.Fatal("second turn did not retain original value")
		}
		results = append(results, result)
	}
	status, err := c.Status(ctx, id)
	if err != nil || !status.Live || status.Status != "Ready" {
		t.Fatalf("parent did not survive turns: %v %+v", err, status)
	}
	raw, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CELLN_INTEROP_RESULT"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
