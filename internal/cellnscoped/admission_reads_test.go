package cellnscoped

import (
	"fmt"
	"testing"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAdmissionReadsAreSharedWithinOneAdmissionAndBounded(t *testing.T) {
	d := &Dispatcher{}
	now := time.Unix(1790000000, 0)
	key := admissionKey("run-uid", "")
	first := d.admissionReads(key, now)
	if first == nil || d.admissionReads(key, now.Add(time.Second)) != first {
		t.Fatal("preparation and revalidation of one admission must share one memo")
	}
	if d.admissionReads(admissionKey("run-uid", "turn-1"), now) == first {
		t.Fatal("a continuation turn is its own admission")
	}
	if d.admissionReads(key, now.Add(admissionReadTTL)) == first {
		t.Fatal("an expired memo was reused")
	}
	if d.admissionReads(admissionKey("", ""), now) != nil {
		t.Fatal("a run without an identity must not be memoised")
	}

	full := &Dispatcher{}
	for i := range admissionReadLimit {
		if full.admissionReads(admissionKey(fmt.Sprintf("run-%d", i), ""), now) == nil {
			t.Fatalf("entry %d refused below the limit", i)
		}
	}
	if full.admissionReads(admissionKey("overflow", ""), now) != nil {
		t.Fatal("the memo table grew past its limit")
	}
	if full.admissionReads(admissionKey("overflow", ""), now.Add(admissionReadTTL)) == nil {
		t.Fatal("expired entries were not pruned to make room")
	}
}

// Revalidation uses, then drops, the memo the preparation of the same run
// created: the keys derived on both sides agree.
func TestRevalidateAuthorityConsumesThePreparationMemo(t *testing.T) {
	d := &Dispatcher{Store: cellnauthority.PreparedStore{Reader: fake.NewClientBuilder().Build()}}
	now := time.Now()
	d.admissionReads(admissionKey("run-uid", ""), now)
	prepared := &cellnauthority.StoredPreparation{}
	prepared.Operation.Resolution.Decision.Run = cellnauthority.RunBinding{Namespace: "tenant", Name: "run", UID: "run-uid"}
	if err := d.RevalidateAuthority(t.Context(), prepared); err == nil {
		t.Fatal("revalidation against an empty API unexpectedly passed")
	}
	if _, ok := d.reads[admissionKey("run-uid", "")]; ok {
		t.Fatal("revalidation did not consume the preparation's memo")
	}

	d.admissionReads(admissionKey("run-uid", "turn-1"), now)
	turn := &cellnauthority.StoredPreparation{}
	turn.Operation.Resolution.Decision.Run = cellnauthority.RunBinding{Namespace: "tenant", Name: "run", UID: "run-uid"}
	turn.Operation.ResolveRequest.TurnKey = &types.NamespacedName{Namespace: "tenant", Name: "turn-1"}
	_ = d.RevalidateAuthority(t.Context(), turn)
	if len(d.reads) != 0 {
		t.Fatalf("turn revalidation left memos behind: %v", d.reads)
	}
}
