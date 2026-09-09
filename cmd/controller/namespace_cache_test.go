package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Exercise the real informer and multi-namespace cache, not a fake client:
// controller-runtime v0.20.0 accepts the configuration but fails scoped Lists.
func TestExcludingCacheListsIncludedNamespacesAndClusterObjects(t *testing.T) {
	// This bounded HTTP fixture implements ordinary list/watch, not the
	// optional streaming initial-list protocol of newer API servers.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("watch") == "true" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		switch r.URL.Path {
		case "/api/v1/pods":
			selector, err := fields.ParseSelector(r.URL.Query().Get("fieldSelector"))
			if err != nil {
				t.Error(err)
				http.Error(w, "invalid selector", http.StatusBadRequest)
				return
			}
			list := corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, ListMeta: metav1.ListMeta{ResourceVersion: "1"}}
			for _, ns := range []string{"default", "sympozium-system", "celln-agents"} {
				if selector.Matches(fields.Set{"metadata.namespace": ns}) {
					list.Items = append(list.Items, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: ns}})
				}
			}
			_ = json.NewEncoder(w).Encode(list)
		case "/api/v1/nodes":
			if r.URL.Query().Get("fieldSelector") != "" {
				t.Error("namespace selector leaked to a cluster-scoped resource")
				http.Error(w, "namespace selector on nodes", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, ListMeta: metav1.ListMeta{ResourceVersion: "1"}, Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "framework"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Pod"), meta.RESTScopeNamespace)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("PodList"), meta.RESTScopeNamespace)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Node"), meta.RESTScopeRoot)
	mapper.Add(corev1.SchemeGroupVersion.WithKind("NodeList"), meta.RESTScopeRoot)
	options, err := controllerCacheOptions("", "celln-agents")
	if err != nil {
		t.Fatal(err)
	}
	options.Scheme, options.Mapper = scheme, mapper
	c, err := cache.New(&rest.Config{Host: server.URL}, options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if !c.WaitForCacheSync(ctx) {
		t.Fatal("cache did not start")
	}
	for _, ns := range []string{"default", "sympozium-system", "celln-agents", ""} {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.InNamespace(ns)); err != nil {
			t.Fatalf("List namespace %q: %v", ns, err)
		}
		want := 1
		if ns == "celln-agents" {
			want = 0
		}
		if ns == "" {
			want = 2
		}
		if len(pods.Items) != want {
			t.Fatalf("namespace %q: got %d pods, want %d", ns, len(pods.Items), want)
		}
		for _, pod := range pods.Items {
			if pod.Namespace == "celln-agents" || (ns != "" && pod.Namespace != ns) {
				t.Fatalf("namespace isolation violated: %+v", pod.ObjectMeta)
			}
		}
	}
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes); err != nil || len(nodes.Items) != 1 {
		t.Fatalf("cluster-scoped List: nodes=%v, err=%v", nodes.Items, err)
	}
}
