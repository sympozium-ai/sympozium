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
			if scenario == "allowed" && len(seen) != 4 {
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

func TestAuthorityReadinessUsesExplicitNamespaces(t *testing.T) {
	kube := fake.NewSimpleClientset()
	want := map[string]bool{
		"namespaces///tenant-a":                   true,
		"modelconnections/sympozium.ai/tenant-a/": true,
		"agents/sympozium.ai/tenant-a/":           true,
		"secrets//tenant-a/":                      true,
		"namespaces///tenant-b":                   true,
		"modelconnections/sympozium.ai/tenant-b/": true,
		"agents/sympozium.ai/tenant-b/":           true,
		"secrets//tenant-b/":                      true,
	}
	seen := map[string]bool{}
	kube.PrependReactor("create", "selfsubjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		a := review.Spec.ResourceAttributes
		key := a.Resource + "/" + a.Group + "/" + a.Namespace + "/" + a.Name
		if a.Verb != "get" || !want[key] {
			t.Fatalf("unexpected scoped authority query: %#v", a)
		}
		seen[key] = true
		return true, &authv1.SelfSubjectAccessReview{Status: authv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	if err := AuthorityReadinessForNamespaces(kube.AuthorizationV1().SelfSubjectAccessReviews(), []string{"tenant-a", "tenant-b"})(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(want) {
		t.Fatalf("saw %d checks, want %d", len(seen), len(want))
	}
	for _, invalid := range [][]string{{""}, {" tenant-a"}, {"tenant-a", "tenant-a"}} {
		if err := AuthorityReadinessForNamespaces(kube.AuthorizationV1().SelfSubjectAccessReviews(), invalid)(context.Background()); err == nil {
			t.Fatalf("accepted invalid namespaces %#v", invalid)
		}
	}
}
