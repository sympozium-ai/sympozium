package webproxy

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestWebRequestHashUsesIdempotencyKey(t *testing.T) {
	first := webRequestHash("retry-safe", "agent-a", "mimo-v2.5-pro", "system one", "task one")
	second := webRequestHash(" retry-safe ", "agent-a", "mimo-v2-flash", "different", "different")
	if first != second {
		t.Fatalf("expected Idempotency-Key to dominate request fingerprint: %s != %s", first, second)
	}
	if len(first) != 16 {
		t.Fatalf("hash must be a compact label-safe value, got len=%d", len(first))
	}
}

func TestWebRequestHashFallsBackToPromptFingerprint(t *testing.T) {
	first := webRequestHash("", "agent-a", "mimo-v2.5-pro", "system", "task")
	second := webRequestHash("", "agent-a", "mimo-v2.5-pro", "system", "task")
	third := webRequestHash("", "agent-a", "mimo-v2.5-pro", "system", "different task")
	if first != second {
		t.Fatalf("same request should produce stable fingerprint: %s != %s", first, second)
	}
	if first == third {
		t.Fatalf("different request should produce different fingerprint: %s", first)
	}
}

func TestFindRecentWebRunReusesNewestMatchingRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	hash := "abcdef0123456789"
	older := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "older",
			Namespace:         "sympozium-system",
			CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute)),
			Labels: map[string]string{
				"sympozium.ai/instance":     "alfy",
				"sympozium.ai/source":       "web-proxy",
				"sympozium.ai/request-hash": hash,
			},
		},
	}
	newer := older.DeepCopy()
	newer.Name = "newer"
	newer.CreationTimestamp = metav1.NewTime(now.Add(-1 * time.Minute))
	stale := older.DeepCopy()
	stale.Name = "stale"
	stale.CreationTimestamp = metav1.NewTime(now.Add(-30 * time.Minute))
	otherHash := older.DeepCopy()
	otherHash.Name = "other"
	otherHash.Labels["sympozium.ai/request-hash"] = "different"

	proxy := &Proxy{
		k8s: fake.NewClientBuilder().WithScheme(scheme).WithObjects(older, newer, stale, otherHash).Build(),
		log: testr.New(t),
	}

	got, err := proxy.findRecentWebRun(context.Background(), "sympozium-system", "alfy", hash, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "newer" {
		t.Fatalf("expected newest matching recent run, got %#v", got)
	}
}

func TestFindRecentWebRunIgnoresExpiredRuns(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	hash := "abcdef0123456789"
	stale := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "stale",
			Namespace:         "sympozium-system",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-30 * time.Minute)),
			Labels: map[string]string{
				"sympozium.ai/instance":     "alfy",
				"sympozium.ai/source":       "web-proxy",
				"sympozium.ai/request-hash": hash,
			},
		},
	}
	proxy := &Proxy{k8s: fake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build(), log: testr.New(t)}
	got, err := proxy.findRecentWebRun(context.Background(), "sympozium-system", "alfy", hash, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no reusable run after ttl, got %s", got.Name)
	}
}
