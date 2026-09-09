package controller_test

import (
	"bytes"
	"context"
	"io"
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"os"
	"path/filepath"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"testing"
)

// Real, expiring ServiceAccount authentication; no admin impersonation.
func liveParentControllerConfig(t *testing.T, ctx context.Context, store client.Client, admin *rest.Config, namespace string) *rest.Config {
	t.Helper()
	if os.Getenv("CELLN_INTEROP_SCOPED_CONTROLLER") != "true" {
		return admin
	}
	path := os.Getenv("CELLN_INTEROP_PARENT_RBAC")
	if !filepath.IsAbs(path) {
		t.Fatal("explicit absolute parent RBAC manifest required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for {
		object := &unstructured.Unstructured{}
		if err := decoder.Decode(object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		object.SetNamespace(namespace)
		if object.GetKind() == "RoleBinding" {
			if err := unstructured.SetNestedSlice(object.Object, []any{map[string]any{"kind": "ServiceAccount", "name": "celln-parent-controller", "namespace": namespace}}, "subjects"); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	adminClient, err := kubernetes.NewForConfig(admin)
	if err != nil {
		t.Fatal(err)
	}
	seconds := int64(600)
	token, err := adminClient.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, "celln-parent-controller", &authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{ExpirationSeconds: &seconds}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately omit admin certificates, exec auth and credential files.
	config := &rest.Config{Host: admin.Host, TLSClientConfig: rest.TLSClientConfig{CAFile: admin.CAFile, CAData: admin.CAData, ServerName: admin.ServerName}, BearerToken: token.Status.Token}
	scoped, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		group, resource, verb, name, ns string
		allowed                         bool
	}{
		{"sympozium.ai", "agentruns", "patch", "", namespace, true},
		{"sympozium.ai", "agentrunturns", "watch", "", namespace, true},
		{"sympozium.ai", "cellntools", "get", "", namespace, true},
		{"", "configmaps", "get", "grant-operator", namespace, true},
		{"", "configmaps", "get", "unrelated", namespace, false},
		{"", "configmaps", "update", "grant-operator", namespace, false},
		{"sympozium.ai", "cellntools", "update", "", namespace, false},
		{"", "secrets", "get", "", namespace, false},
		{"", "pods", "create", "", namespace, false},
		{"sympozium.ai", "agentruns", "get", "", "default", false},
	} {
		answer, err := scoped.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authzv1.ResourceAttributes{Namespace: check.ns, Group: check.group, Resource: check.resource, Verb: check.verb, Name: check.name}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if answer.Status.Allowed != check.allowed {
			t.Fatalf("scoped permission %s %s/%s in %s: got %v", check.verb, check.resource, check.name, check.ns, answer.Status.Allowed)
		}
	}
	t.Log("scoped ServiceAccount authenticated; grant writes, secrets, pod creation and other-namespace reads denied")
	return config
}
