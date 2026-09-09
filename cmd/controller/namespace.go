package main

import (
	"fmt"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"strings"
)

// Cache selection is deliberately separate from authority: the uncached reader
// still resolves independently configured approval sources, and direct clients
// remain subject to their service account's Kubernetes RBAC.
func controllerCacheOptions(namespace string, exclusions ...string) (cache.Options, error) {
	if len(exclusions) > 0 && exclusions[0] != "" {
		if namespace != "" || len(exclusions) != 1 {
			return cache.Options{}, fmt.Errorf("choose either one watch namespace or excluded namespaces")
		}
		seen := map[string]bool{}
		var selectors []fields.Selector
		for _, excluded := range strings.Split(exclusions[0], ",") {
			if len(validation.IsDNS1123Label(excluded)) != 0 || seen[excluded] {
				return cache.Options{}, fmt.Errorf("excluded namespaces must be distinct valid namespace names")
			}
			seen[excluded] = true
			selectors = append(selectors, fields.OneTermNotEqualSelector("metadata.namespace", excluded))
		}
		// Namespace-local config is important: a global DefaultFieldSelector
		// would also filter cluster-scoped resources, which have no namespace.
		return cache.Options{DefaultNamespaces: map[string]cache.Config{
			cache.AllNamespaces: {FieldSelector: fields.AndSelectors(selectors...)},
		}}, nil
	}
	if namespace == "" {
		return cache.Options{}, nil
	}
	if errors := validation.IsDNS1123Label(namespace); len(errors) != 0 {
		return cache.Options{}, fmt.Errorf("watch namespace must be a single valid namespace name")
	}
	return cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}}, nil
}
