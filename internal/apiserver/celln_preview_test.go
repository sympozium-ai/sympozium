package apiserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPreviewEndpointRefusesUnconfiguredOrCallerAuthority(t *testing.T) {
	s := NewServer(nil, nil, nil, logr.Discard())
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"agentRef":"agent","cellnSelection":{"toolRefs":[]}}`, 503},
		{`{"agentRef":"agent","cellnSelection":{"toolRefs":[]},"executionLifecycle":"enduring"}`, 503},
		{`{"agentRef":"agent","cellnSelection":{"toolRefs":[]},"executionLifecycle":"unknown"}`, 400},
		{`{"agentRef":"agent","operatorSource":{"name":"mine"},"cellnSelection":{"toolRefs":[]}}`, 400},
		{`{"agentRef":"agent","cellnSelection":{"toolRefs":[]}} {}`, 400},
		{strings.Repeat("x", 8193), 400},
	} {
		r := httptest.NewRecorder()
		s.Handler(nil).ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/api/v1/celln-selection/preview", strings.NewReader(tc.body)))
		if r.Code != tc.code {
			t.Fatalf("status %d want %d", r.Code, tc.code)
		}
	}
}

func TestPreviewConfigurationStrictAndBounded(t *testing.T) {
	s := NewServer(nil, nil, nil, logr.Discard())
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()
	valid := `{"apiVersion":"sympozium.ai/celln-permission-preview-v1","bindings":[{"agent":{"namespace":"tenant","name":"agent"},"operatorSource":{"namespace":"authority","name":"operator"},"runtimeSource":{"namespace":"authority","name":"runtime"},"agentSource":{"namespace":"authority","name":"agent"}}]}`
	for _, tc := range []struct {
		raw  string
		good bool
	}{
		{valid, true}, {valid + ` {}`, false}, {strings.Replace(valid, `"bindings"`, `"unknown"`, 1), false},
		{strings.Replace(valid, `"name":"runtime"`, `"name":"operator"`, 1), false},
		{`{"apiVersion":"sympozium.ai/celln-permission-preview-v1","bindings":[]}`, false},
		{strings.Repeat("x", (1<<20)+1), false},
	} {
		path := filepath.Join(t.TempDir(), "preview.json")
		if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.LoadCellnPreview(path, c); (err == nil) != tc.good {
			t.Fatalf("configuration accepted=%t want %t: %v", err == nil, tc.good, err)
		}
	}
	if err := s.LoadCellnPreview("relative.json", c); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := s.ConfigureCellnPreview(nil, nil); err == nil {
		t.Fatal("missing reader accepted")
	}
}
