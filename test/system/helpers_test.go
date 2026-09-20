//go:build system

package system_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// httpDo sends an HTTP request through the API server mux and returns the response recorder.
func httpDo(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(raw)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// httpJSON sends an HTTP request and unmarshals the JSON response body.
func httpJSON[T any](t *testing.T, method, path string, body any) (T, int) {
	t.Helper()
	rec := httpDo(t, method, path, body)
	var result T
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("unmarshal response (status %d, body %s): %v", rec.Code, rec.Body.String(), err)
		}
	}
	return result, rec.Code
}

// createTestNamespace creates a unique namespace for the test and registers cleanup.
func createTestNamespace(t *testing.T) string {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "test-sys-",
		},
	}
	if err := k8sClient.Create(testCtx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(testCtx, ns)
	})
	return ns.Name
}

// pollUntil retries condition at interval until it returns true or timeout is reached.
func pollUntil(t *testing.T, timeout, interval time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pollUntil timed out after %s", timeout)
		}
		time.Sleep(interval)
	}
}

// waitForObject polls until the resource is readable through k8sClient and
// leaves it in obj. k8sClient is the manager's cached client, so an object
// written a moment ago — by the test, the API server or a controller — is
// visible only once its informer has delivered it. Use this, not assertExists,
// for any read that follows a write.
func waitForObject(t *testing.T, obj client.Object, ns, name string) {
	t.Helper()
	key := client.ObjectKey{Namespace: ns, Name: name}
	var lastErr error
	deadline := time.Now().Add(10 * time.Second)
	for {
		if lastErr = k8sClient.Get(testCtx, key, obj); lastErr == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %T %s/%s to exist: %v", obj, ns, name, lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitForGET polls an API GET until it returns 200. The API server reads
// through the same informer cache, so a handler that looks an object up
// (createRun's Agent, triggerStimulus's Ensemble) answers 404 for one that was
// created a moment ago. Wait for the GET before calling such a handler.
func waitForGET(t *testing.T, path string) {
	t.Helper()
	var last *httptest.ResponseRecorder
	deadline := time.Now().Add(10 * time.Second)
	for {
		if last = httpDo(t, http.MethodGet, path, nil); last.Code == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s never returned 200; last status = %d, body = %s", path, last.Code, last.Body.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// updateEnsemble applies mutate to the named Ensemble and writes it back,
// re-reading and retrying on a conflict. The read comes from the informer
// cache, which can trail the controller's own status writes, so a single
// get-modify-update can carry a stale resourceVersion.
func updateEnsemble(t *testing.T, ns, name string, mutate func(*sympoziumv1alpha1.Ensemble)) {
	t.Helper()
	backoff := wait.Backoff{Steps: 50, Duration: 200 * time.Millisecond, Factor: 1.0}
	err := retry.RetryOnConflict(backoff, func() error {
		var e sympoziumv1alpha1.Ensemble
		if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, &e); err != nil {
			return err
		}
		mutate(&e)
		return k8sClient.Update(testCtx, &e)
	})
	if err != nil {
		t.Fatalf("update ensemble %s/%s: %v", ns, name, err)
	}
}

// assertExists verifies the resource exists, with a single cached read. Only
// valid once the cache is already known to hold the object (for instance after
// a List through the same cache returned it); after a write use waitForObject.
func assertExists(t *testing.T, obj client.Object, ns, name string) {
	t.Helper()
	key := client.ObjectKey{Namespace: ns, Name: name}
	if err := k8sClient.Get(testCtx, key, obj); err != nil {
		t.Fatalf("expected %T %s/%s to exist: %v", obj, ns, name, err)
	}
}

// assertNotExists verifies the resource does not exist.
func assertNotExists(t *testing.T, obj client.Object, ns, name string) {
	t.Helper()
	key := client.ObjectKey{Namespace: ns, Name: name}
	err := k8sClient.Get(testCtx, key, obj)
	if err == nil {
		t.Fatalf("expected %T %s/%s to not exist, but it does", obj, ns, name)
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected error checking %T %s/%s: %v", obj, ns, name, err)
	}
}

// requireStatus asserts the HTTP status code and fails with body on mismatch.
func requireStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("HTTP status = %d, want %d; body = %s", rec.Code, want, rec.Body.String())
	}
}

// nsQuery returns the namespace query parameter string.
func nsQuery(ns string) string {
	return fmt.Sprintf("namespace=%s", ns)
}
