package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
)

func newTestServer(t *testing.T, objs ...client.Object) (*Server, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	return srv, cl
}

func TestPatchPersonaPackRejectsMissingSecret(t *testing.T) {
	pack := &sympoziumv1alpha1.PersonaPack{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-team", Namespace: "default"},
		Spec: sympoziumv1alpha1.PersonaPackSpec{
			Personas: []sympoziumv1alpha1.PersonaSpec{{Name: "sre"}},
		},
	}
	srv, cl := newTestServer(t, pack)

	body := `{"enabled":true,"provider":"openai","secretName":"platform-team-credentials","model":"gpt-4o"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/personapacks/platform-team?namespace=default", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `secret "platform-team-credentials" not found`) {
		t.Fatalf("expected missing secret error, got: %s", rec.Body.String())
	}

	var got sympoziumv1alpha1.PersonaPack
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "platform-team", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get persona pack: %v", err)
	}
	if len(got.Spec.AuthRefs) != 0 {
		t.Fatalf("expected authRefs to remain empty, got %#v", got.Spec.AuthRefs)
	}
}

func TestPatchPersonaPackAutoCreatesProviderSecretWithNewName(t *testing.T) {
	pack := &sympoziumv1alpha1.PersonaPack{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-team", Namespace: "default"},
		Spec: sympoziumv1alpha1.PersonaPackSpec{
			Personas: []sympoziumv1alpha1.PersonaSpec{{Name: "sre"}},
		},
	}
	srv, cl := newTestServer(t, pack)

	payload := map[string]any{
		"enabled":  true,
		"provider": "openai",
		"apiKey":   "sk-test",
		"model":    "gpt-4o",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/personapacks/platform-team?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got sympoziumv1alpha1.PersonaPack
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "platform-team", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get persona pack: %v", err)
	}
	if len(got.Spec.AuthRefs) != 1 {
		t.Fatalf("expected 1 authRef, got %#v", got.Spec.AuthRefs)
	}
	if got.Spec.AuthRefs[0].Secret != "platform-team-openai-key" {
		t.Fatalf("expected secret name platform-team-openai-key, got %q", got.Spec.AuthRefs[0].Secret)
	}

	var secret corev1.Secret
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "platform-team-openai-key", Namespace: "default"}, &secret); err != nil {
		t.Fatalf("get provider secret: %v", err)
	}
	key := string(secret.Data["OPENAI_API_KEY"])
	if key == "" {
		key = secret.StringData["OPENAI_API_KEY"]
	}
	if key != "sk-test" {
		t.Fatalf("expected OPENAI_API_KEY to be set")
	}
}
