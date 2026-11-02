package cellnauthority

import (
	"context"
	"slices"
	"strings"
	"sync"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CatalogueReads memoises the resolver's two cluster-wide LISTs (cluster
// tools and execution policies) across the resolutions of one admission: the
// preparation's two loads and the revalidation's two loads before start.
//
// It never decides what is current. Every load still asks the API server,
// through the same uncached reader, for the identity of every object in the
// collection (a metadata-only LIST: name, UID and resourceVersion). The
// memoised full objects are reused only when that live identity set is exactly
// the one they were read with. resourceVersion changes on every write to an
// object, and a deleted-and-recreated object has a new UID, so an exact match
// means the API server holds byte-for-byte the objects that were read before;
// an object added to, changed in or removed from the collection forces a full
// LIST, exactly as without the memo. The resolver therefore sees what an
// uncached full LIST would have returned at the moment of the metadata LIST,
// and refusals are computed from that snapshot unchanged.
//
// The zero value is ready to use and safe for concurrent use. A nil
// *CatalogueReads disables memoisation.
type CatalogueReads struct {
	mu       sync.Mutex
	tools    catalogueMemo[api.ClusterCellnTool]
	policies catalogueMemo[api.CellnExecutionPolicy]
}

type objectIdentity struct {
	name, uid, resourceVersion string
}

type catalogueMemo[T any] struct {
	valid bool
	ids   []objectIdentity
	items []T
}

func identitiesOf(metas []metav1.ObjectMeta) []objectIdentity {
	ids := make([]objectIdentity, 0, len(metas))
	for _, meta := range metas {
		ids = append(ids, objectIdentity{name: meta.Name, uid: string(meta.UID), resourceVersion: meta.ResourceVersion})
	}
	slices.SortFunc(ids, func(a, b objectIdentity) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		if c := strings.Compare(a.uid, b.uid); c != 0 {
			return c
		}
		return strings.Compare(a.resourceVersion, b.resourceVersion)
	})
	return ids
}

// reusable reports whether every memoised identity is complete enough to pin
// an object's content. An object without a UID or resourceVersion is never
// reused.
func reusable(ids []objectIdentity) bool {
	for _, id := range ids {
		if id.uid == "" || id.resourceVersion == "" {
			return false
		}
	}
	return true
}

// liveIdentities lists only the metadata of a collection through the given
// uncached reader. ok is false when the metadata LIST failed; the caller then
// takes a full LIST, whose error carries today's refusal.
func liveIdentities(ctx context.Context, reader client.Reader, listKind string) ([]objectIdentity, bool) {
	var list metav1.PartialObjectMetadataList
	list.SetGroupVersionKind(api.GroupVersion.WithKind(listKind))
	if err := reader.List(ctx, &list); err != nil {
		return nil, false
	}
	metas := make([]metav1.ObjectMeta, 0, len(list.Items))
	for i := range list.Items {
		metas = append(metas, list.Items[i].ObjectMeta)
	}
	return identitiesOf(metas), true
}

func (c *CatalogueReads) memoisedTools() catalogueMemo[api.ClusterCellnTool] {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tools
}

func (c *CatalogueReads) memoisedPolicies() catalogueMemo[api.CellnExecutionPolicy] {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.policies
}

// listClusterTools returns every ClusterCellnTool, as a full uncached LIST
// would, reusing the memoised objects only when their live identities are
// unchanged.
func (c *CatalogueReads) listClusterTools(ctx context.Context, reader client.Reader) ([]api.ClusterCellnTool, error) {
	if c != nil {
		if memo := c.memoisedTools(); memo.valid {
			if live, ok := liveIdentities(ctx, reader, "ClusterCellnToolList"); ok && slices.Equal(live, memo.ids) {
				out := make([]api.ClusterCellnTool, len(memo.items))
				for i := range memo.items {
					memo.items[i].DeepCopyInto(&out[i])
				}
				return out, nil
			}
		}
	}
	var list api.ClusterCellnToolList
	if err := reader.List(ctx, &list); err != nil {
		return nil, err
	}
	if c != nil {
		metas := make([]metav1.ObjectMeta, 0, len(list.Items))
		items := make([]api.ClusterCellnTool, len(list.Items))
		for i := range list.Items {
			metas = append(metas, list.Items[i].ObjectMeta)
			list.Items[i].DeepCopyInto(&items[i])
		}
		ids := identitiesOf(metas)
		c.mu.Lock()
		c.tools = catalogueMemo[api.ClusterCellnTool]{valid: reusable(ids), ids: ids, items: items}
		c.mu.Unlock()
	}
	return list.Items, nil
}

// listExecutionPolicies is listClusterTools for CellnExecutionPolicy.
func (c *CatalogueReads) listExecutionPolicies(ctx context.Context, reader client.Reader) ([]api.CellnExecutionPolicy, error) {
	if c != nil {
		if memo := c.memoisedPolicies(); memo.valid {
			if live, ok := liveIdentities(ctx, reader, "CellnExecutionPolicyList"); ok && slices.Equal(live, memo.ids) {
				out := make([]api.CellnExecutionPolicy, len(memo.items))
				for i := range memo.items {
					memo.items[i].DeepCopyInto(&out[i])
				}
				return out, nil
			}
		}
	}
	var list api.CellnExecutionPolicyList
	if err := reader.List(ctx, &list); err != nil {
		return nil, err
	}
	if c != nil {
		metas := make([]metav1.ObjectMeta, 0, len(list.Items))
		items := make([]api.CellnExecutionPolicy, len(list.Items))
		for i := range list.Items {
			metas = append(metas, list.Items[i].ObjectMeta)
			list.Items[i].DeepCopyInto(&items[i])
		}
		ids := identitiesOf(metas)
		c.mu.Lock()
		c.policies = catalogueMemo[api.CellnExecutionPolicy]{valid: reusable(ids), ids: ids, items: items}
		c.mu.Unlock()
	}
	return list.Items, nil
}
