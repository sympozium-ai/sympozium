package modelgateway

import (
	"context"
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
	return func(ctx context.Context) error {
		if reviews == nil {
			return fail(ReasonUnavailable, 503, nil)
		}
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		for _, resource := range []struct{ group, name string }{{"", "namespaces"}, {"sympozium.ai", "modelconnections"}, {"", "secrets"}} {
			result, err := reviews.Create(ctx, &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{Group: resource.group, Resource: resource.name, Verb: "get"}}}, metav1.CreateOptions{})
			if err != nil || result == nil || !result.Status.Allowed || result.Status.Denied || result.Status.EvaluationError != "" {
				return fail(ReasonUnavailable, 503, nil)
			}
		}
		return nil
	}
}
