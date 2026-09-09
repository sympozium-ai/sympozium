package apiserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Captures the API-to-Kubernetes deletion contract; no host teardown implied.
type runDeleteCapture struct {
	client.Client
	uid             *types.UID
	calls           int
	name, namespace string
}

func (s *runDeleteCapture) Delete(_ context.Context, obj client.Object, options ...client.DeleteOption) error {
	s.calls++
	s.name, s.namespace = obj.GetName(), obj.GetNamespace()
	resolved := &client.DeleteOptions{}
	for _, option := range options {
		option.ApplyToDelete(resolved)
	}
	if resolved.Preconditions != nil {
		s.uid = resolved.Preconditions.UID
	}
	return nil
}

func TestRunDeletionPassesExactUIDPrecondition(t *testing.T) {
	for _, test := range []struct {
		query, uid string
		code       int
	}{
		{"?namespace=owned&uid=original-uid", "original-uid", http.StatusNoContent},
		{"", "", http.StatusNoContent},
		{"?uid=", "", http.StatusBadRequest},
		{"?uid=one&uid=two", "", http.StatusBadRequest},
	} {
		t.Run(test.query, func(t *testing.T) {
			store := &runDeleteCapture{}
			handler := NewServer(store, nil, nil, logr.Discard()).Handler(nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/v1/runs/parent"+test.query, nil))
			if response.Code != test.code {
				t.Fatalf("status %d", response.Code)
			}
			if test.code == http.StatusBadRequest {
				if store.calls != 0 {
					t.Fatal("invalid identity reached storage")
				}
				return
			}
			if store.calls != 1 || store.name != "parent" {
				t.Fatal("wrong deletion target")
			}
			if test.uid != "" {
				if store.uid == nil || string(*store.uid) != test.uid || store.namespace != "owned" {
					t.Fatal("identity precondition or namespace lost")
				}
			} else if store.uid != nil || store.namespace != "default" {
				t.Fatal("legacy deletion changed")
			}
		})
	}
}
