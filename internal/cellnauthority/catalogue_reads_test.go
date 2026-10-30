package cellnauthority

import (
	"context"
	"fmt"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// listCountingReader counts the resolver's LISTs by what they return: full
// objects or metadata only.
type listCountingReader struct {
	client.Reader
	full     map[string]int
	metadata map[string]int
}

func newListCountingReader(inner client.Reader) *listCountingReader {
	return &listCountingReader{Reader: inner, full: map[string]int{}, metadata: map[string]int{}}
}

func (r *listCountingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	switch l := list.(type) {
	case *metav1.PartialObjectMetadataList:
		r.metadata[l.GetObjectKind().GroupVersionKind().Kind]++
	default:
		r.full[fmt.Sprintf("%T", list)]++
	}
	return r.Reader.List(ctx, list, opts...)
}

func (r *listCountingReader) reset() {
	r.full, r.metadata = map[string]int{}, map[string]int{}
}

const (
	fullToolList   = "*v1alpha1.ClusterCellnToolList"
	fullPolicyList = "*v1alpha1.CellnExecutionPolicyList"
)

// One admission resolves four times over the catalogue: the preparation's two
// loads and the revalidation's two. With the memo, the full catalogue crosses
// the wire once; every later load only lists identities.
func TestCatalogueReadsRevalidationDoesNotRelistUnchangedCatalogue(t *testing.T) {
	f := newPlatformFixture(t, "tenant", true)
	counter := newListCountingReader(f.client)
	resolver := PlatformResolver{Reader: counter, Reads: &CatalogueReads{}}

	frozen, err := resolver.Resolve(t.Context(), f.runKey, platformRequest(f))
	if err != nil {
		t.Fatal(err)
	}
	if counter.full[fullToolList] != 1 || counter.full[fullPolicyList] != 1 {
		t.Fatalf("preparation full LISTs = %v, want one per catalogue", counter.full)
	}
	if counter.metadata["ClusterCellnToolList"] != 1 || counter.metadata["CellnExecutionPolicyList"] != 1 {
		t.Fatalf("preparation stability check metadata LISTs = %v, want one per catalogue", counter.metadata)
	}

	counter.reset()
	if err := resolver.Revalidate(t.Context(), f.runKey, *frozen); err != nil {
		t.Fatalf("unchanged authority refused at revalidation: %v", err)
	}
	if len(counter.full) != 0 {
		t.Fatalf("revalidation re-listed full catalogue objects: %v", counter.full)
	}
	if counter.metadata["ClusterCellnToolList"] != 2 || counter.metadata["CellnExecutionPolicyList"] != 2 {
		t.Fatalf("revalidation metadata LISTs = %v, want two per catalogue (both loads still ask the API server)", counter.metadata)
	}

	// Without a memo the resolver keeps today's behaviour: a full LIST of each
	// catalogue on every load.
	counter.reset()
	plain := PlatformResolver{Reader: counter}
	if err := plain.Revalidate(t.Context(), f.runKey, *frozen); err != nil {
		t.Fatal(err)
	}
	if counter.full[fullToolList] != 2 || counter.full[fullPolicyList] != 2 || len(counter.metadata) != 0 {
		t.Fatalf("memo-free revalidation LISTs full=%v metadata=%v", counter.full, counter.metadata)
	}
}

// A change between preparation and revalidation is refused with the same
// AUTH_* code with or without the memo, and a changed catalogue is re-read in
// full rather than served from the memo.
func TestCatalogueReadsRevalidationStillRefusesChangesWithTodaysCodes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		catalogue string // the catalogue the change touches, if any
		change    func(context.Context, *testing.T, platformFixture)
	}{
		{name: "policy-spec", catalogue: fullPolicyList, change: func(ctx context.Context, t *testing.T, f platformFixture) {
			var policy api.CellnExecutionPolicy
			mustDo(t, f.client.Get(ctx, types.NamespacedName{Name: "a-policy"}, &policy))
			policy.Spec.Ceilings.MaxOutputTokens--
			mustDo(t, f.client.Update(ctx, &policy))
		}},
		{name: "policy-metadata-only", catalogue: fullPolicyList, change: func(ctx context.Context, t *testing.T, f platformFixture) {
			var policy api.CellnExecutionPolicy
			mustDo(t, f.client.Get(ctx, types.NamespacedName{Name: "a-policy"}, &policy))
			if policy.Annotations == nil {
				policy.Annotations = map[string]string{}
			}
			policy.Annotations["touched"] = "yes"
			mustDo(t, f.client.Update(ctx, &policy))
		}},
		{name: "policy-added", catalogue: fullPolicyList, change: func(ctx context.Context, t *testing.T, f platformFixture) {
			var policy api.CellnExecutionPolicy
			mustDo(t, f.client.Get(ctx, types.NamespacedName{Name: "a-policy"}, &policy))
			added := &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "m-policy", UID: "m-policy-uid", Generation: 1}, Spec: *policy.Spec.DeepCopy()}
			added.Spec.Ceilings.MaxTurns = 2
			mustDo(t, f.client.Create(ctx, added))
		}},
		{name: "policy-removed", catalogue: fullPolicyList, change: func(ctx context.Context, t *testing.T, f platformFixture) {
			mustDo(t, f.client.Delete(ctx, &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "a-policy"}}))
		}},
		{name: "policy-recreated-same-spec", catalogue: fullPolicyList, change: func(ctx context.Context, t *testing.T, f platformFixture) {
			var policy api.CellnExecutionPolicy
			mustDo(t, f.client.Get(ctx, types.NamespacedName{Name: "a-policy"}, &policy))
			mustDo(t, f.client.Delete(ctx, &policy))
			recreated := &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "a-policy", UID: "a-policy-uid-2", Generation: 1}, Spec: *policy.Spec.DeepCopy()}
			mustDo(t, f.client.Create(ctx, recreated))
		}},
		{name: "tool-recreated-same-spec", catalogue: fullToolList, change: func(ctx context.Context, t *testing.T, f platformFixture) {
			var tool api.ClusterCellnTool
			mustDo(t, f.client.Get(ctx, types.NamespacedName{Name: "workspace-write-v1"}, &tool))
			mustDo(t, f.client.Delete(ctx, &tool))
			recreated := &api.ClusterCellnTool{ObjectMeta: metav1.ObjectMeta{Name: tool.Name, UID: "tool-uid-2", Generation: 1}, Spec: *tool.Spec.DeepCopy()}
			mustDo(t, f.client.Create(ctx, recreated))
		}},
		{name: "tool-revision", catalogue: fullToolList, change: func(ctx context.Context, t *testing.T, f platformFixture) {
			var tool api.ClusterCellnTool
			mustDo(t, f.client.Get(ctx, types.NamespacedName{Name: "workspace-write-v1"}, &tool))
			tool.Spec.Revision = "v2"
			mustDo(t, f.client.Update(ctx, &tool))
		}},
		{name: "tool-deleted", catalogue: fullToolList, change: func(ctx context.Context, t *testing.T, f platformFixture) {
			mustDo(t, f.client.Delete(ctx, &api.ClusterCellnTool{ObjectMeta: metav1.ObjectMeta{Name: "workspace-write-v1"}}))
		}},
		{name: "namespace-withdrawn", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			var ns corev1.Namespace
			mustDo(t, f.client.Get(ctx, types.NamespacedName{Name: "tenant"}, &ns))
			ns.Labels = map[string]string{}
			mustDo(t, f.client.Update(ctx, &ns))
		}},
		{name: "connection-endpoint", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			var connection api.ModelConnection
			mustDo(t, f.client.Get(ctx, types.NamespacedName{Namespace: "tenant", Name: "model"}, &connection))
			connection.Spec.Endpoint = "https://other.example/v1/chat/completions"
			mustDo(t, f.client.Update(ctx, &connection))
		}},
		{name: "run-spec", change: func(ctx context.Context, t *testing.T, f platformFixture) {
			var run api.AgentRun
			mustDo(t, f.client.Get(ctx, f.runKey, &run))
			run.Spec.SystemPrompt = "changed"
			mustDo(t, f.client.Update(ctx, &run))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Today's outcome: no memo, the same change.
			baseline := newPlatformFixture(t, "tenant", true)
			baselineFrozen, err := baseline.resolver.Resolve(t.Context(), baseline.runKey, platformRequest(baseline))
			if err != nil {
				t.Fatal(err)
			}
			tc.change(t.Context(), t, baseline)
			baselineErr := baseline.resolver.Revalidate(t.Context(), baseline.runKey, *baselineFrozen)

			// The same admission with the memo shared from preparation.
			f := newPlatformFixture(t, "tenant", true)
			counter := newListCountingReader(f.client)
			resolver := PlatformResolver{Reader: counter, Reads: &CatalogueReads{}}
			frozen, err := resolver.Resolve(t.Context(), f.runKey, platformRequest(f))
			if err != nil {
				t.Fatal(err)
			}
			tc.change(t.Context(), t, f)
			counter.reset()
			memoErr := resolver.Revalidate(t.Context(), f.runKey, *frozen)

			if (baselineErr == nil) != (memoErr == nil) || PlatformReason(baselineErr) != PlatformReason(memoErr) {
				t.Fatalf("memo changed the revalidation outcome: without memo %q (%v), with memo %q (%v)", PlatformReason(baselineErr), baselineErr, PlatformReason(memoErr), memoErr)
			}
			if tc.name == "policy-metadata-only" {
				// Metadata-only writes do not change authority today either.
				if memoErr != nil {
					t.Fatalf("metadata-only policy write refused: %v", memoErr)
				}
			} else if PlatformReason(memoErr) == "" {
				t.Fatalf("change was not refused with an AUTH_* code: %v", memoErr)
			}
			if tc.catalogue != "" && counter.full[tc.catalogue] == 0 {
				t.Fatalf("changed catalogue %s was served from the memo instead of a full LIST: full=%v metadata=%v", tc.catalogue, counter.full, counter.metadata)
			}
			t.Logf("reason %q", PlatformReason(memoErr))
		})
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
