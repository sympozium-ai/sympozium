package main

import (
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"strings"
	"testing"
)

func TestControllerNamespaceCacheIsExplicitAndDefaultCompatible(t *testing.T) {
	defaults, err := controllerCacheOptions("")
	if err != nil || defaults.DefaultNamespaces != nil || defaults.ByObject != nil {
		t.Fatal("default cluster-wide cache changed")
	}
	options, err := controllerCacheOptions("celln-catalogue-proof-test")
	if err != nil || len(options.DefaultNamespaces) != 1 {
		t.Fatal("one namespace was not selected")
	}
	if _, ok := options.DefaultNamespaces["celln-catalogue-proof-test"]; !ok {
		t.Fatal("wrong namespace selected")
	}
	if _, ok := options.DefaultNamespaces[""]; ok {
		t.Fatal("all-namespace fallback added")
	}
	for _, invalid := range []string{"*", "a,b", " a", "a ", " ", "A", "a.b", "a/b", strings.Repeat("a", 64)} {
		if _, err := controllerCacheOptions(invalid); err == nil {
			t.Fatalf("invalid namespace accepted: %q", invalid)
		}
	}
}

func TestControllerNamespaceExclusionPreservesOtherWorkloads(t *testing.T) {
	options, err := controllerCacheOptions("", "celln-agents,other-owner")
	if err != nil || options.DefaultFieldSelector != nil || len(options.DefaultNamespaces) != 1 {
		t.Fatalf("invalid exclusion options: %v", err)
	}
	selector := options.DefaultNamespaces[cache.AllNamespaces].FieldSelector
	for _, name := range []string{"default", "sympozium-system", "existing-e2e", "celln-agents", "other-owner"} {
		want := name != "celln-agents" && name != "other-owner"
		if got := selector.Matches(fields.Set{"metadata.namespace": name}); got != want {
			t.Fatalf("namespace %s: included=%v, want %v", name, got, want)
		}
	}
	for _, invalid := range []string{"a,", ",a", "a,a", "a, b", "*", "A", "a/b"} {
		if _, err := controllerCacheOptions("", invalid); err == nil {
			t.Fatalf("invalid exclusions accepted: %q", invalid)
		}
	}
	if _, err := controllerCacheOptions("included", "excluded"); err == nil {
		t.Fatal("ambiguous include/exclude mode accepted")
	}
}
