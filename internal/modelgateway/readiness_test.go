package modelgateway

import (
	"context"
	"errors"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestAuthorityReadinessFailsClosed(t *testing.T) {
	for _, scenario := range []string{"allowed", "denied", "outage", "evaluation-error"} {
		t.Run(scenario, func(t *testing.T) {
			kube := fake.NewSimpleClientset()
			seen := map[string]bool{}
			kube.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				review := action.(ktesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
				attr := review.Spec.ResourceAttributes
				if attr == nil || attr.Verb != "get" || attr.Namespace != "" {
					t.Fatal("unexpected authority query")
				}
				seen[attr.Resource] = true
				if scenario == "outage" {
					return true, nil, errors.New("unavailable")
				}
				status := authv1.SubjectAccessReviewStatus{Allowed: true}
				if scenario == "denied" && attr.Resource == "secrets" {
					status.Allowed = false
				}
				if scenario == "evaluation-error" {
					status.EvaluationError = "unknown"
				}
				return true, &authv1.SelfSubjectAccessReview{Status: status}, nil
			})
			err := AuthorityReadiness(kube.AuthorizationV1().SelfSubjectAccessReviews())(context.Background())
			if (err == nil) != (scenario == "allowed") {
				t.Fatalf("unexpected result %v", err)
			}
			if scenario == "allowed" && len(seen) != 3 {
				t.Fatal("missing authority checks")
			}
			for _, action := range kube.Actions() {
				if action.GetResource().Resource != "selfsubjectaccessreviews" {
					t.Fatal("readiness accessed data")
				}
			}
		})
	}
	if err := AuthorityReadiness(nil)(context.Background()); err == nil {
		t.Fatal("nil authority accepted")
	}
}
