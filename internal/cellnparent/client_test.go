package cellnparent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const testID = "blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fixture(t *testing.T, handler http.HandlerFunc) (*Client, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("parent-only-credential-long-enough"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := New(Options{URL: server.URL, TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, path
}

func TestCancelTurnExactRouteAndConservativeAcknowledgement(t *testing.T) {
	for _, tc := range []struct {
		body     string
		code     int
		accepted bool
	}{
		{`{"cancellationRequested":true,"teardownConfirmed":false,"retryAuthorized":false}`, 202, true},
		{`{"cancellationRequested":true,"teardownConfirmed":true,"retryAuthorized":false}`, 202, false},
		{`{"cancellationRequested":true,"teardownConfirmed":false,"retryAuthorized":true}`, 202, false},
		{`{"cancellationRequested":true}`, 202, false},
		{`{"cancellationRequested":false,"teardownConfirmed":false,"retryAuthorized":false}`, 202, false},
		{`{}`, 409, false}, {`{}`, 307, false},
	} {
		t.Run(fmt.Sprintf("%d-%s", tc.code, tc.body), func(t *testing.T) {
			var calls atomic.Int32
			client, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1/parents/"+testID+"/turns/exact-turn/cancel" || r.ContentLength != 0 {
					t.Error("cancellation retargeted or carried a body")
				}
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			})
			err := client.CancelTurn(t.Context(), testID, "exact-turn")
			if (err == nil) != tc.accepted || calls.Load() != 1 {
				t.Fatalf("accepted=%v err=%v calls=%d", tc.accepted, err, calls.Load())
			}
			for _, turn := range []string{"", "../other", "one/cancel", "one?x=y", strings.Repeat("a", 65)} {
				if client.CancelTurn(t.Context(), testID, turn) == nil {
					t.Fatal("invalid turn accepted")
				}
			}
			if calls.Load() != 1 {
				t.Fatal("invalid turn reached network")
			}
		})
	}
}

func TestAsyncTurnRequestsPendingWithoutReplay(t *testing.T) {
	var calls atomic.Int32
	client, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Prefer") != "respond-async" || r.Method != "POST" || r.URL.Path != "/v1/parents/"+testID+"/turns" {
			t.Error("missing async preference or wrong endpoint")
		}
		w.WriteHeader(202)
		fmt.Fprint(w, `{"pending":true,"retryAuthorized":false}`)
	})
	result, err := client.SubmitAsync(t.Context(), testID, "one", "hello")
	if err != nil || !result.Pending || calls.Load() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestLifecycleAndCredentialRotation(t *testing.T) {
	var calls atomic.Int32
	client, path := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		token := "parent-only-credential-long-enough"
		if calls.Load() > 1 {
			token = "rotated-parent-credential-long-enough"
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong rotated credential")
		}
		switch {
		case r.URL.Path == "/v1/parents":
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"incarnation":%q,"initializationPending":true,"retryAuthorized":false}`, testID)
		case strings.HasSuffix(r.URL.Path, "/turns"):
			fmt.Fprint(w, `{"kind":"completed","apiVersion":"celln.parent-context/v1","turnId":"one","succeeded":true,"answer":"violet"}`)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			w.WriteHeader(202)
			fmt.Fprint(w, `{"cancellationRequested":true,"teardownConfirmed":false}`)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			fmt.Fprint(w, `{"teardownConfirmed":true}`)
		default:
			fmt.Fprintf(w, `{"incarnation":%q,"status":"Ready","statusIsLiveOwnerObservation":true,"retryAuthorized":false}`, testID)
		}
	})
	ctx := context.Background()
	if err := client.Create(ctx, testID, testID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("rotated-parent-credential-long-enough"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(ctx, testID)
	if err != nil || status.Status != "Ready" {
		t.Fatalf("%+v %v", status, err)
	}
	turn, err := client.Submit(ctx, testID, "one", "hello")
	if err != nil || turn.Answer != "violet" {
		t.Fatalf("%+v %v", turn, err)
	}
	if err := client.Cancel(ctx, testID); err != nil {
		t.Fatal(err)
	}
	if err := client.Stop(ctx, testID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 {
		t.Fatal(calls.Load())
	}
}

func TestAmbiguousCreationNeverRetriesOrAcceptsRedirect(t *testing.T) {
	for _, code := range []int{302, 307, 409, 500, 202} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var count atomic.Int32
			client, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("Location", "/retry")
				w.WriteHeader(code)
				fmt.Fprint(w, `{"incarnation":"wrong","initializationPending":true,"retryAuthorized":false}`)
			})
			if !errors.Is(client.Create(context.Background(), testID, testID), ErrReconcile) {
				t.Fatal("ambiguous creation accepted")
			}
			if count.Load() != 1 {
				t.Fatal("mutation repeated")
			}
		})
	}
}

func TestPendingTurnAndUnknownOutcome(t *testing.T) {
	for _, body := range []string{`{"pending":true,"retryAuthorized":false}`, `{"pending":true,"retryAuthorized":true}`, `{}`, strings.Repeat("x", 32769)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			var count atomic.Int32
			client, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1); w.WriteHeader(202); fmt.Fprint(w, body) })
			result, err := client.Submit(context.Background(), testID, "one", "hello")
			if strings.Contains(body, `"retryAuthorized":false`) {
				if err != nil || !result.Pending {
					t.Fatalf("%+v %v", result, err)
				}
			} else if !errors.Is(err, ErrReconcile) {
				t.Fatal("unsafe result accepted")
			}
			if count.Load() != 1 {
				t.Fatal("turn repeated")
			}
		})
	}
}

func TestHistoricalStatusCannotBecomeReady(t *testing.T) {
	for _, status := range []string{"ContextLost", "Ready"} {
		client, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"incarnation":%q,"status":%q,"statusIsLiveOwnerObservation":false,"retryAuthorized":false}`, testID, status)
		})
		_, err := client.Status(context.Background(), testID)
		if (err == nil) != (status == "ContextLost") {
			t.Fatalf("%s: %v", status, err)
		}
	}
}

func TestTurnEvidencePreservesLostContextAndRejectsMismatchedIdentity(t *testing.T) {
	for _, parent := range []string{testID, "blake3:" + strings.Repeat("b", 64)} {
		client, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"ownerStatus":"ContextLost","retryAuthorized":false,"turn":{"stage":"parent-committed","record":{"version":1,"parent":%q,"child":%q,"turnId":"one","succeeded":true,"answer":"violet"}}}`, parent, testID)
		})
		evidence, err := client.Turn(context.Background(), testID, "one")
		if parent == testID {
			if err != nil || evidence.OwnerStatus != "ContextLost" || evidence.Turn.Stage != "parent-committed" {
				t.Fatalf("%+v %v", evidence, err)
			}
		} else if !errors.Is(err, ErrReconcile) {
			t.Fatal("different parent evidence accepted")
		}
	}
}

func TestInvalidAuthorityAndPathsNeverReachServer(t *testing.T) {
	client, _ := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid input sent") })
	ctx := context.Background()
	if client.Create(ctx, "../policy", testID) == nil {
		t.Fatal("profile path accepted")
	}
	if _, err := client.Submit(ctx, testID, "../turn", "hello"); err == nil {
		t.Fatal("turn path accepted")
	}
	if _, err := client.Submit(ctx, testID, "one", strings.Repeat("a", 2049)); err == nil {
		t.Fatal("oversized turn accepted")
	}
	for _, origin := range []string{"http://remote.example", "https://user:secret@example.com", "https://example.com/path", "https://example.com?"} {
		if _, err := New(Options{URL: origin, TokenFile: "/operator/token"}); err == nil {
			t.Fatalf("unsafe origin %s", origin)
		}
	}
}
