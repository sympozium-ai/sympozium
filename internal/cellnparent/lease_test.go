package cellnparent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestParentLeaseExpiredBoundaries(t *testing.T) {
	base := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
	admitted := &api.CellnParentStatus{AdmittedAt: &metav1.Time{Time: base}}
	for _, tc := range []struct {
		name   string
		parent *api.CellnParentStatus
		lease  int64
		now    time.Time
		want   bool
	}{
		{"before-deadline", admitted, 60, base.Add(59*time.Second + 999999999*time.Nanosecond), false},
		{"exactly-at-deadline", admitted, 60, base.Add(60 * time.Second), true},
		{"after-deadline", admitted, 60, base.Add(61 * time.Second), true},
		{"long-after-deadline", admitted, 60, base.Add(time.Hour), true},
		{"nil-parent", nil, 60, base.Add(time.Hour), false},
		{"nil-stamp-predates-enforcement", &api.CellnParentStatus{}, 60, base.Add(time.Hour), false},
		{"zero-stamp", &api.CellnParentStatus{AdmittedAt: &metav1.Time{}}, 60, base.Add(time.Hour), false},
		{"non-positive-lease", admitted, 0, base.Add(time.Hour), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParentLeaseExpired(tc.parent, tc.lease, tc.now); got != tc.want {
				t.Fatalf("ParentLeaseExpired = %v, want %v", got, tc.want)
			}
		})
	}
}

// leaseClaimFixture builds a running parent with a committed initial turn and
// one subsequent turn waiting for a slot. admittedAt may be nil to represent a
// parent admitted before lease enforcement existed.
func leaseClaimFixture(t *testing.T, admittedAt *time.Time) (client.Client, types.NamespacedName, string) {
	t.Helper()
	run, binding := admissionFixture(t)
	run.Status.Phase = api.AgentRunPhaseRunning
	run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true, InitialTurn: &api.CellnParentTurnStatus{ID: "initial", Message: "hello", Child: testID, Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: true, Answer: "ready"}}}
	run.Generation = 1
	run.Status.Conditions = []metav1.Condition{{Type: "CellnParentReady", Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 1}}
	if admittedAt != nil {
		run.Status.CellnParent.AdmittedAt = &metav1.Time{Time: *admittedAt}
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	turn, err := NewTurn(run, "one", "follow up")
	if err != nil {
		t.Fatal(err)
	}
	turn.UID = types.UID("one-uid")
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}, &api.AgentRunTurn{}).WithObjects(run, turn).Build()
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	raw, err := json.Marshal(ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: filepath.Join(root, "unopened-token")}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return store, client.ObjectKeyFromObject(turn), path
}

func acceptedTurns(t *testing.T, store client.Client, namespace, name string) int32 {
	t.Helper()
	var saved api.AgentRun
	if err := store.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, &saved); err != nil {
		t.Fatal(err)
	}
	return saved.Status.CellnParent.AcceptedTurns
}

func TestClaimTurnSlotRefusesExpiredOriginalLease(t *testing.T) {
	base := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	t.Run("within-lease-claims", func(t *testing.T) {
		store, key, path := leaseClaimFixture(t, &base)
		if err := ClaimTurnSlot(ctx, store, store, key, path, base.Add(59*time.Second)); err != nil {
			t.Fatalf("claim within original lease refused: %v", err)
		}
		if got := acceptedTurns(t, store, "tenant", "run"); got != 1 {
			t.Fatalf("AcceptedTurns = %d, want 1", got)
		}
	})
	t.Run("at-and-after-deadline-refused-without-spending", func(t *testing.T) {
		for _, now := range []time.Time{base.Add(60 * time.Second), base.Add(time.Hour)} {
			store, key, path := leaseClaimFixture(t, &base)
			err := ClaimTurnSlot(ctx, store, store, key, path, now)
			if err == nil || !strings.Contains(err.Error(), "lease expired") {
				t.Fatalf("expired lease admitted new work at %v: %v", now, err)
			}
			if !errors.Is(err, ErrParentLeaseExpired) {
				t.Fatalf("expiry refusal is not recognizable: %v", err)
			}
			if got := acceptedTurns(t, store, "tenant", "run"); got != 0 {
				t.Fatalf("expired claim spent budget: AcceptedTurns = %d", got)
			}
		}
	})
	t.Run("missing-stamp-predates-enforcement", func(t *testing.T) {
		store, key, path := leaseClaimFixture(t, nil)
		if err := ClaimTurnSlot(ctx, store, store, key, path, base.Add(100*365*24*time.Hour)); err != nil {
			t.Fatalf("unstamped parent refused: %v", err)
		}
	})
	t.Run("owner-keeps-idempotent-reclaim-after-expiry", func(t *testing.T) {
		store, key, path := leaseClaimFixture(t, &base)
		if err := ClaimTurnSlot(ctx, store, store, key, path, base.Add(10*time.Second)); err != nil {
			t.Fatal(err)
		}
		// The owning turn's re-claim is reconciliation continuity, not new
		// work: expiry must not break the in-flight turn's own retries.
		if err := ClaimTurnSlot(ctx, store, store, key, path, base.Add(time.Hour)); err != nil {
			t.Fatalf("owner re-claim refused after expiry: %v", err)
		}
		if got := acceptedTurns(t, store, "tenant", "run"); got != 1 {
			t.Fatalf("re-claim consumed extra budget: AcceptedTurns = %d", got)
		}
	})
}

func TestCreateClaimStampsOriginalAdmissionOnce(t *testing.T) {
	run, approval := admissionFixture(t)
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}).WithObjects(run).Build()
	key := client.ObjectKeyFromObject(run)
	ctx := context.Background()
	if err := Prepare(ctx, store, store, key, approval); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	ok, err := ClaimCreate(ctx, store, store, key, approval)
	if err != nil || !ok {
		t.Fatalf("first create claim refused: ok=%v err=%v", ok, err)
	}
	after := time.Now().UTC()
	var saved api.AgentRun
	if err := store.Get(ctx, key, &saved); err != nil {
		t.Fatal(err)
	}
	stamp := saved.Status.CellnParent.AdmittedAt
	// Status persistence round-trips metav1.Time at second precision, so the
	// lower bound is truncated to the second before comparing.
	if stamp == nil || stamp.Time.Before(before.Truncate(time.Second)) || stamp.Time.After(after) {
		t.Fatalf("admission stamp not recorded at first attempt: %v", stamp)
	}
	// A repeat claim — the shape of a retried token or recovered controller —
	// must neither authorize work nor move the original lease anchor.
	if ok, err := ClaimCreate(ctx, store, store, key, approval); err != nil || ok {
		t.Fatalf("repeat create authorized: %v %v", ok, err)
	}
	var again api.AgentRun
	if err := store.Get(ctx, key, &again); err != nil {
		t.Fatal(err)
	}
	if !again.Status.CellnParent.AdmittedAt.Time.Equal(stamp.Time) {
		t.Fatal("repeat claim extended the original admission")
	}
}
