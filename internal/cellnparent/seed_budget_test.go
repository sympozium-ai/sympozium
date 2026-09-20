package cellnparent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/celln"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The API's conversation bounds and the owner client's are the same numbers.
func TestConversationBoundsAgreeWithTheOwnerClient(t *testing.T) {
	if api.MaxConversationMessageBytes != celln.MaxParentMessageBytes || api.MaxConversationAnswerBytes != celln.MaxParentAnswerBytes {
		t.Fatalf("api %d/%d, client %d/%d", api.MaxConversationMessageBytes, api.MaxConversationAnswerBytes, celln.MaxParentMessageBytes, celln.MaxParentAnswerBytes)
	}
}

// Only a runtime profile that states the current task bound widens the seed:
// an older guest refuses a seed over 1536 bytes and its parent is never
// created.
func TestSeedBudgetFollowsTheProfilesTaskBytes(t *testing.T) {
	for taskBytes, want := range map[int64]int{0: 1536, 1024: 1536, 2048: 1536, 8192: 1536, 16383: 1536, 16384: 15872, 32768: 32256} {
		if got := SeedBudget(taskBytes); got != want {
			t.Fatalf("taskBytes %d: budget %d, want %d", taskBytes, got, want)
		}
	}
	if LegacySeedBytes != 1536 {
		t.Fatalf("legacy budget %d", LegacySeedBytes)
	}
}

func withTaskBytes(objects []client.Object, taskBytes int64) []client.Object {
	for _, object := range objects {
		if profile, ok := object.(*api.CellnRuntimeProfile); ok {
			profile.Spec.Limits.TaskBytes = taskBytes
		}
	}
	return objects
}

func TestSeedBudgetForResolvesTheRunsProfile(t *testing.T) {
	ctx := context.Background()
	runOf := func(store client.Client) *api.AgentRun {
		var run api.AgentRun
		if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "conversation"}, &run); err != nil {
			t.Fatal(err)
		}
		return &run
	}
	current := platformStore(t, withTaskBytes(platformObjects("tenant-a"), 16384)...)
	if got := SeedBudgetFor(ctx, current, runOf(current)); got != 15872 {
		t.Fatalf("current package: %d", got)
	}
	// The wrapper named by the Agent when the run's selection names none.
	viaAgent := runOf(current)
	viaAgent.Spec.CellnSelection.RuntimeRef = ""
	if got := SeedBudgetFor(ctx, current, viaAgent); got != 15872 {
		t.Fatalf("wrapper through the Agent: %d", got)
	}
	old := platformStore(t, withTaskBytes(platformObjects("tenant-a"), 2048)...)
	if got := SeedBudgetFor(ctx, old, runOf(old)); got != LegacySeedBytes {
		t.Fatalf("old package: %d", got)
	}
	// Anything that cannot be established falls back to the old budget.
	for name, change := range map[string]func(store client.Client, run *api.AgentRun){
		"no selection":    func(_ client.Client, run *api.AgentRun) { run.Spec.CellnSelection, run.Spec.AgentRef = nil, "" },
		"missing wrapper": func(_ client.Client, run *api.AgentRun) { run.Spec.CellnSelection.RuntimeRef = "gone" },
		"missing profile": func(store client.Client, _ *api.AgentRun) {
			_ = store.Delete(ctx, &api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial"}})
		},
		"wrapper pins another revision": func(store client.Client, _ *api.AgentRun) {
			var wrapper api.AgentRuntime
			_ = store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "celln-native"}, &wrapper)
			wrapper.Spec.CellnProfileRef.Revision = "v0"
			_ = store.Update(ctx, &wrapper)
		},
		"legacy runtime without a shared profile": func(store client.Client, _ *api.AgentRun) {
			var wrapper api.AgentRuntime
			_ = store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "celln-native"}, &wrapper)
			wrapper.Spec.CellnProfileRef = nil
			_ = store.Update(ctx, &wrapper)
		},
	} {
		store := platformStore(t, withTaskBytes(platformObjects("tenant-a"), 16384)...)
		run := runOf(store)
		change(store, run)
		if got := SeedBudgetFor(ctx, store, run); got != LegacySeedBytes {
			t.Fatalf("%s: budget %d", name, got)
		}
	}
}

// The same conversation seeds a current fleet with all of its memory and an
// old one with only what its guest accepts.
func TestTranscriptAndContinuationHonourTheBudget(t *testing.T) {
	var exchanges []api.ConversationExchange
	for i := 0; i < 6; i++ {
		exchanges = append(exchanges, api.ConversationExchange{User: strings.Repeat("u", 300), Assistant: strings.Repeat("a", 1200)})
	}
	exchanges[5].User = "last"
	wide, narrow := TrimSeed(exchanges, SeedBudget(16384)), TrimSeed(exchanges, SeedBudget(2048))
	if len(wide) != 6 || !SeedFits(wide, 15872) {
		t.Fatalf("current budget kept %d of 6 exchanges", len(wide))
	}
	if len(narrow) != 1 || narrow[0].User != "last" || !SeedFits(narrow, LegacySeedBytes) || SeedFits(wide, LegacySeedBytes) {
		t.Fatalf("old budget kept %d exchanges", len(narrow))
	}
	// A full 8192-byte answer is ordinary memory on a current fleet and is
	// dropped, not truncated, on an old one.
	long := []api.ConversationExchange{{User: "q", Assistant: strings.Repeat("a", api.MaxConversationAnswerBytes)}}
	if got := TrimSeed(long, SeedBudget(16384)); len(got) != 1 || len(got[0].Assistant) != 8192 {
		t.Fatalf("8192-byte answer not kept: %d", len(got))
	}
	if got := TrimSeed(long, LegacySeedBytes); len(got) != 0 {
		t.Fatalf("old budget kept an 8192-byte answer")
	}
	if _, err := Continuation(enduringRun(), wide, ContinuationOriginAutomatic, SeedBudget(16384)); err != nil {
		t.Fatalf("current budget refused its own seed: %v", err)
	}
	if _, err := Continuation(enduringRun(), wide, ContinuationOriginAutomatic, LegacySeedBytes); err == nil {
		t.Fatal("a seed an old guest refuses was built for it")
	}
	// The exchange count is bounded whatever the bytes.
	many := make([]api.ConversationExchange, 20)
	for i := range many {
		many[i] = api.ConversationExchange{User: "u", Assistant: "a"}
	}
	if got := TrimSeed(many, SeedBudget(16384)); len(got) != 16 {
		t.Fatalf("kept %d exchanges", len(got))
	}
}

// The provision plan is where a seed reaches the owner: a seed beyond what
// the run's runtime profile takes never gets there.
func TestPlatformPlanBoundsTheSeedByTheProfilesTaskBytes(t *testing.T) {
	var seed []api.ConversationExchange
	for i := 0; i < 4; i++ {
		seed = append(seed, api.ConversationExchange{User: strings.Repeat("u", 200), Assistant: strings.Repeat("a", 1200)})
	}
	for name, tc := range map[string]struct {
		taskBytes int64
		admitted  bool
	}{"current package": {16384, true}, "old package": {2048, false}} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := platformStore(t, withTaskBytes(platformObjects("tenant-a"), tc.taskBytes)...)
			key := types.NamespacedName{Namespace: "tenant-a", Name: "conversation"}
			var run api.AgentRun
			if err := store.Get(ctx, key, &run); err != nil {
				t.Fatal(err)
			}
			run.Spec.Conversation = &api.ConversationSpec{Continuation: "automatic", ContinuesFrom: "earlier", Depth: 1, Seed: seed}
			if err := store.Update(ctx, &run); err != nil {
				t.Fatal(err)
			}
			expected, err := cellnauthority.ScopedParentIncarnation("cluster", "tenant-a-uid", "tenant-a-run")
			if err != nil {
				t.Fatal(err)
			}
			var lastPlan HostProvisionPlan
			var called atomic.Bool
			owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called.Store(true)
				body, _ := io.ReadAll(io.LimitReader(r.Body, 65537))
				if json.Unmarshal(body, &lastPlan) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"apiVersion": "celln.parent-provisioned/v1", "launchProfile": "blake3:" + strings.Repeat("e", 64), "incarnation": expected})
			}))
			defer owner.Close()
			root := t.TempDir()
			token := filepath.Join(root, "token")
			if err := os.WriteFile(token, []byte("platform-admission-test-credential"), 0600); err != nil {
				t.Fatal(err)
			}
			p := PlatformProvisioner{ClusterID: "cluster", Journal: filepath.Join(root, "journal"), Approvals: filepath.Join(root, "approvals"), Target: owner.URL, TokenFile: token}
			for _, dir := range []string{p.Journal, p.Approvals} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			err = p.Admit(ctx, store, key)
			if tc.admitted && (err != nil || len(lastPlan.History) != len(seed)) {
				t.Fatalf("seed within the profile's task bound refused: %v (%d exchanges)", err, len(lastPlan.History))
			}
			if !tc.admitted && (err == nil || called.Load() || !strings.Contains(err.Error(), "seed exceeds")) {
				t.Fatalf("oversized seed reached an old package's owner: %v", err)
			}
		})
	}
}
