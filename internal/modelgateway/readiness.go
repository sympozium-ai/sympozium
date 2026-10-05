package modelgateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authclient "k8s.io/client-go/kubernetes/typed/authorization/v1"
)

// AuthorityReadiness checks live API reachability and the dedicated process's
// actual cluster-wide get permissions. It neither lists nor reads Secret data.
// This deliberately reports the true credential-custody blast radius; passing
// this check is not a claim of namespace-isolated Kubernetes RBAC.
func AuthorityReadiness(reviews authclient.SelfSubjectAccessReviewInterface) func(context.Context) error {
	return AuthorityReadinessForNamespaces(reviews, nil)
}

// AuthorityReadinessForNamespaces optionally checks the exact namespace and
// namespaced authorities used by an isolated gateway. An empty namespace list
// preserves the historical cluster-wide readiness check. This is only an RBAC
// probe: it neither lists nor reads Namespace, ModelConnection, or Secret data.
func AuthorityReadinessForNamespaces(reviews authclient.SelfSubjectAccessReviewInterface, namespaces []string) func(context.Context) error {
	return func(ctx context.Context) error {
		if reviews == nil {
			return fail(ReasonUnavailable, 503, nil)
		}
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		type check struct{ group, resource, namespace, resourceName string }
		checks := []check{}
		if len(namespaces) == 0 {
			checks = []check{{resource: "namespaces"}, {group: "sympozium.ai", resource: "modelconnections"}, {group: "sympozium.ai", resource: "agents"}, {resource: "secrets"}}
		} else {
			seen := map[string]bool{}
			for _, namespace := range namespaces {
				if namespace == "" || namespace != strings.TrimSpace(namespace) || seen[namespace] {
					return fmt.Errorf("invalid readiness namespace configuration")
				}
				seen[namespace] = true
				checks = append(checks,
					check{resource: "namespaces", resourceName: namespace},
					check{group: "sympozium.ai", resource: "modelconnections", namespace: namespace},
					check{group: "sympozium.ai", resource: "agents", namespace: namespace},
					check{resource: "secrets", namespace: namespace},
				)
			}
		}
		for _, resource := range checks {
			result, err := reviews.Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{Group: resource.group, Resource: resource.resource, Namespace: resource.namespace, Name: resource.resourceName, Verb: "get"}}}, metav1.CreateOptions{})
			if err != nil || result == nil || !result.Status.Allowed || result.Status.Denied || result.Status.EvaluationError != "" {
				return fail(ReasonUnavailable, 503, nil)
			}
		}
		return nil
	}
}
