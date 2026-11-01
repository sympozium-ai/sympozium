package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

func originKey(pinned string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "anthropic-key", Namespace: "default"}, Data: map[string][]byte{"ANTHROPIC_API_KEY": []byte("sk-ant-DUMMY")}}
	if pinned != "" {
		s.Annotations = map[string]string{modelkey.OriginAnnotation: pinned}
	}
	return s
}

// The reported repro, at the controller (which runs whether or not the
// webhook did): no pod holding the key is created for a run whose baseURL is
// not the key's origin, and the run reads as Refused with a typed code.
func TestJobRunsSendAKeyOnlyToItsOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, pinned, baseURL string
		env                   map[string]string
		wantRefused           bool
		wantPin               string
	}{
		{name: "attacker baseURL, unpinned key", baseURL: "http://listener.attacker.svc:443", wantRefused: true},
		{name: "attacker baseURL, pinned key", pinned: "https://api.anthropic.com", baseURL: "http://listener.attacker.svc:443", wantRefused: true, wantPin: "https://api.anthropic.com"},
		{name: "run cannot override an operator pin", pinned: "https://llm.internal:8443", baseURL: "", wantRefused: true, wantPin: "https://llm.internal:8443"},
		{name: "default endpoint works and pins", baseURL: "", wantPin: "https://api.anthropic.com"},
		{name: "operator-pinned custom endpoint works", pinned: "https://llm.internal:8443", baseURL: "https://llm.internal:8443/v1", wantPin: "https://llm.internal:8443"},
		{name: "spec.env cannot reroute the client", baseURL: "", env: map[string]string{"ANTHROPIC_BASE_URL": "http://listener.attacker.svc:443", "KEEP": "me"}, wantRefused: true, wantPin: "https://api.anthropic.com"},
		{name: "a run's own proxy is refused", baseURL: "", env: map[string]string{"HTTPS_PROXY": "http://listener.attacker.svc:443"}, wantRefused: true, wantPin: "https://api.anthropic.com"},
		{name: "ordinary env is kept", baseURL: "", env: map[string]string{"KEEP": "me"}, wantPin: "https://api.anthropic.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := parityAgent()
			agent.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{{Provider: "anthropic", Secret: "anthropic-key"}}
			run := parityRun()
			run.Spec.Model.Provider = "anthropic"
			run.Spec.Model.AuthSecretRef = "anthropic-key"
			run.Spec.Model.BaseURL = tc.baseURL
			run.Spec.Env = tc.env
			key := originKey(tc.pinned)
			r := newAgentRunTestReconciler(t, agent, key, run)
			if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
				t.Fatalf("reconcilePending: %v", err)
			}
			var job batchv1.Job
			jobErr := r.Get(context.Background(), client.ObjectKeyFromObject(run), &job)
			var stored sympoziumv1alpha1.AgentRun
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
				t.Fatal(err)
			}
			var secret corev1.Secret
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(key), &secret); err != nil {
				t.Fatal(err)
			}
			if got := secret.Annotations[modelkey.OriginAnnotation]; got != tc.wantPin {
				t.Fatalf("pin = %q, want %q", got, tc.wantPin)
			}
			if tc.wantRefused {
				if jobErr == nil {
					t.Fatal("a pod holding the key was created for a foreign origin")
				}
				if p := (jobBackend{}).Phase(&stored); p != ExecutionRefused {
					t.Fatalf("phase = %s, want Refused (status %q)", p, stored.Status.Error)
				}
				if res := (jobBackend{}).Result(&stored); res == nil || res.PublicReason != string(RefusalModelKeyOriginMismatch) {
					t.Fatalf("result = %+v, want %s", res, RefusalModelKeyOriginMismatch)
				}
				return
			}
			if jobErr != nil {
				t.Fatalf("Job not created: %v (status %q)", jobErr, stored.Status.Error)
			}
			for _, c := range job.Spec.Template.Spec.Containers {
				for _, e := range c.Env {
					if strings.Contains(e.Value, "attacker") {
						t.Fatalf("container %s env %s=%s reroutes the model client", c.Name, e.Name, e.Value)
					}
				}
			}
			if tc.env != nil {
				found := false
				for _, e := range job.Spec.Template.Spec.Containers[0].Env {
					found = found || (e.Name == "KEEP" && e.Value == "me")
				}
				if !found {
					t.Fatal("ordinary spec.env dropped")
				}
			}
		})
	}
}
