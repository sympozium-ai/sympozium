package main

import (
	"context"
	"testing"

	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func reviewNamespace(name string, tenant bool) *core.Namespace {
	labels := map[string]string{"sympozium.ai/celln-review": "495"}
	if tenant {
		labels["sympozium.ai/celln-review-tenant"] = "enabled"
	}
	return &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Status: core.NamespaceStatus{Phase: core.NamespaceActive}}
}

func TestNamespacePreflightIsReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		alter   func([]*core.Namespace) []*core.Namespace
		wantErr bool
	}{
		{"valid", func(ns []*core.Namespace) []*core.Namespace { return ns }, false},
		{"missing-second-tenant", func(ns []*core.Namespace) []*core.Namespace { return append(ns[:1], ns[2:]...) }, true},
		{"foreign-system", func(ns []*core.Namespace) []*core.Namespace {
			ns[2].Labels["sympozium.ai/celln-review"] = "other"
			return ns
		}, true},
		{"system-is-tenant", func(ns []*core.Namespace) []*core.Namespace {
			ns[2].Labels["sympozium.ai/celln-review-tenant"] = "enabled"
			return ns
		}, true},
		{"disabled-tenant", func(ns []*core.Namespace) []*core.Namespace {
			delete(ns[1].Labels, "sympozium.ai/celln-review-tenant")
			return ns
		}, true},
		{"terminating-tenant", func(ns []*core.Namespace) []*core.Namespace {
			now := metav1.Now()
			ns[1].DeletionTimestamp = &now
			return ns
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns := tc.alter([]*core.Namespace{reviewNamespace("celln-review-a-495", true), reviewNamespace("celln-review-b-495", true), reviewNamespace("celln-review-system-495", false)})
			objects := make([]runtime.Object, 0, len(ns))
			for _, n := range ns {
				objects = append(objects, n)
			}
			kube := fake.NewClientset(objects...)
			h := &harness{admin: kube, review: "495"}
			err := h.preflightNamespaces(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v, wantErr=%t", err, tc.wantErr)
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() != "get" || action.GetResource().Resource != "namespaces" {
					t.Fatalf("preflight mutated cluster: %#v", action)
				}
			}
			if tc.wantErr {
				kube.ClearActions()
				if err := h.run(context.Background()); err == nil {
					t.Fatal("run accepted invalid namespaces")
				}
				for _, action := range kube.Actions() {
					if action.GetVerb() != "get" {
						t.Fatalf("run wrote before validating all namespaces: %#v", action)
					}
				}
			}
		})
	}
}

func TestTenantConfigDoesNotInheritAdministratorCredentials(t *testing.T) {
	admin := &rest.Config{Host: "https://cluster.invalid", Username: "admin", Password: "password", BearerToken: "admin-token", BearerTokenFile: "/admin-token", Impersonate: rest.ImpersonationConfig{UserName: "admin"}, TLSClientConfig: rest.TLSClientConfig{CAData: []byte("ca"), CertData: []byte("certificate"), KeyData: []byte("private-key"), Insecure: true}}
	got := tenantConfig(admin, "tenant-token")
	if got.Host != admin.Host || got.BearerToken != "tenant-token" || string(got.CAData) != "ca" {
		t.Fatal("tenant endpoint/trust/token missing")
	}
	if got.Username != "" || got.Password != "" || got.BearerTokenFile != "" || got.Impersonate.UserName != "" || got.CertData != nil || got.KeyData != nil || got.Insecure || got.ExecProvider != nil || got.AuthProvider != nil {
		t.Fatal("administrator authentication inherited")
	}
	got.CAData[0] = 'X'
	if string(admin.CAData) != "ca" {
		t.Fatal("CA data aliases administrator config")
	}
}
