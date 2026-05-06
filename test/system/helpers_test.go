package system_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// assertExists verifies the resource exists in the cluster.
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
