package system_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestModelCreateViaAPI(t *testing.T) {
	ns := createTestNamespace(t)
	name := "sys-model-create"

	body := map[string]any{
		"name":        name,
		"namespace":   ns,
		"serverType":  "llama-cpp",
		"url":         "https://example.com/model.gguf",
		"storageSize": "1Gi",
		"memory":      "2Gi",
		"cpu":         "1",
	}

	rec := httpDo(t, http.MethodPost, "/api/v1/models", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create model status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Wait for Model CR to appear in the informer cache.
	var model sympoziumv1alpha1.Model
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		return k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, &model) == nil
	})

	if model.Spec.Source.URL != "https://example.com/model.gguf" {
		t.Errorf("source.url = %q", model.Spec.Source.URL)
	}

	t.Cleanup(func() {
		_ = k8sClient.Delete(testCtx, &model)
	})
}

func TestModelPhaseTransitions(t *testing.T) {
	ns := createTestNamespace(t)
	name := "sys-model-phase"

	body := map[string]any{
		"name":        name,
		"namespace":   ns,
		"serverType":  "llama-cpp",
		"url":         "https://example.com/model.gguf",
		"storageSize": "1Gi",
		"memory":      "2Gi",
		"cpu":         "1",
	}

	rec := httpDo(t, http.MethodPost, "/api/v1/models", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create model status = %d, body = %s", rec.Code, rec.Body.String())
	}

	t.Cleanup(func() {
		var m sympoziumv1alpha1.Model
		_ = k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, &m)
		_ = k8sClient.Delete(testCtx, &m)
	})

	// Wait for controller to advance the phase past empty.
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var model sympoziumv1alpha1.Model
		if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, &model); err != nil {
			return false
		}
		return model.Status.Phase != ""
	})

	// Verify phase is one of the expected early phases.
	var model sympoziumv1alpha1.Model
	if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, &model); err != nil {
		t.Fatalf("get model: %v", err)
	}
	validPhases := map[sympoziumv1alpha1.ModelPhase]bool{
		sympoziumv1alpha1.ModelPhasePending:     true,
		sympoziumv1alpha1.ModelPhaseDownloading: true,
		sympoziumv1alpha1.ModelPhaseLoading:     true,
	}
	if !validPhases[model.Status.Phase] {
		t.Errorf("phase = %q, want one of Pending/Downloading/Loading", model.Status.Phase)
	}

	// Wait for PVC to be created (name is "model-<name>").
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var pvc corev1.PersistentVolumeClaim
		return k8sClient.Get(testCtx, client.ObjectKey{
			Namespace: ns,
			Name:      fmt.Sprintf("model-%s", name),
		}, &pvc) == nil
	})
}

func TestModelDeleteCleansUp(t *testing.T) {
	ns := createTestNamespace(t)
	name := "sys-model-del"

	body := map[string]any{
		"name":        name,
		"namespace":   ns,
		"serverType":  "llama-cpp",
		"url":         "https://example.com/model.gguf",
		"storageSize": "1Gi",
		"memory":      "2Gi",
		"cpu":         "1",
	}

	rec := httpDo(t, http.MethodPost, "/api/v1/models", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create model status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Wait for controller to process the model (phase non-empty).
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var model sympoziumv1alpha1.Model
		if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, &model); err != nil {
			return false
		}
		return model.Status.Phase != ""
	})

	// Delete via API.
	delPath := fmt.Sprintf("/api/v1/models/%s?namespace=%s", name, ns)
	rec = httpDo(t, http.MethodDelete, delPath, nil)
	if rec.Code != 200 && rec.Code != 204 {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Verify Model CR is gone.
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		var m sympoziumv1alpha1.Model
		err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, &m)
		return err != nil
	})
}
