package webproxy

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// A gated run that retries never publishes under the name the request holds.
// If the proxy only matched that name, the HTTP call would block until its
// deadline while the answer sat on the successor's event.
func TestAnswersRun_FollowsTheRetryChain(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	attempt := func(name, retryOf string) *sympoziumv1alpha1.AgentRun {
		run := &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "sympozium-system"},
		}
		if retryOf != "" {
			run.Status.RetryOf = retryOf
		}
		return run
	}
	first := attempt("alfy-web-abc", "")
	second := attempt("alfy-web-abc-retry-2", "alfy-web-abc")
	// The lineage label carries the chain when the status write was lost.
	third := attempt("alfy-web-abc-retry-3", "")
	third.Labels = map[string]string{"sympozium.ai/retry-of": "alfy-web-abc-retry-2"}
	stranger := attempt("someone-else-retry-2", "someone-else")

	proxy := &Proxy{
		k8s: fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(first, second, third, stranger).Build(),
		log: testr.New(t),
	}

	tests := []struct {
		name      string
		eventRun  string
		wantMatch bool
	}{
		{"the run itself", "alfy-web-abc", true},
		{"its successor", "alfy-web-abc-retry-2", true},
		{"two attempts on, through the lineage label", "alfy-web-abc-retry-3", true},
		{"another request's chain", "someone-else-retry-2", false},
		{"an unrelated run", "nightly-report-7", false},
		{"no run at all", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &eventbus.Event{Metadata: map[string]string{"agentRunID": tt.eventRun}}
			if got := proxy.answersRun(context.Background(), first, event); got != tt.wantMatch {
				t.Errorf("answersRun(%q) = %v, want %v", tt.eventRun, got, tt.wantMatch)
			}
		})
	}
}
