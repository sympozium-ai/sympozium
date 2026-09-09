package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// The administrator credential bootstraps the isolated fixture only. The actual
// issuer gets a short-lived, namespace-scoped token, never the bootstrap identity.
// This deliberately does not pretend to implement production token renewal.
func liveIssuerKubeconfig(t *testing.T, ctx context.Context, dir, evidence, bootstrap, namespace string) string {
	t.Helper()
	if !strings.HasPrefix(namespace, "celln-catalogue-proof-") {
		t.Fatal("issuer identity requires an isolated proof namespace")
	}
	source, err := clientcmd.LoadFromFile(bootstrap)
	must(t, err)
	if source.CurrentContext != "kind-celln-deployed" {
		t.Fatal("issuer bootstrap requires isolated kind-celln-deployed")
	}
	must(t, clientcmdapi.FlattenConfig(source))
	adminConfig, err := clientcmd.NewDefaultClientConfig(*source, &clientcmd.ConfigOverrides{}).ClientConfig()
	must(t, err)
	admin, err := kubernetes.NewForConfig(adminConfig)
	must(t, err)
	const name = "catalogue-issuer"
	meta := metav1.ObjectMeta{Name: name, Namespace: namespace}
	no := false
	_, err = admin.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{ObjectMeta: meta, AutomountServiceAccountToken: &no}, metav1.CreateOptions{})
	must(t, err)
	_, err = admin.RbacV1().Roles(namespace).Create(ctx, &rbacv1.Role{ObjectMeta: meta, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{"sympozium.ai"}, Resources: []string{"agents"}, ResourceNames: []string{"agent"}, Verbs: []string{"get"}},
		{APIGroups: []string{"sympozium.ai"}, Resources: []string{"agentruntimes"}, ResourceNames: []string{"runtime"}, Verbs: []string{"get"}},
		{APIGroups: []string{"sympozium.ai"}, Resources: []string{"agentruns", "cellntools"}, Verbs: []string{"get"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{"operator", "runtime", "agent", "model"}, Verbs: []string{"get"}},
	}}, metav1.CreateOptions{})
	must(t, err)
	_, err = admin.RbacV1().RoleBindings(namespace).Create(ctx, &rbacv1.RoleBinding{ObjectMeta: meta,
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: namespace}},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
	}, metav1.CreateOptions{})
	must(t, err)
	seconds := int64(600)
	if os.Getenv("CELLN_LIVE_INTERACTIVE") == "1" {
		// A bounded development session, not a production renewal mechanism.
		seconds = 9 * 60 * 60
	}
	issued, err := admin.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, name, &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &seconds}}, metav1.CreateOptions{})
	must(t, err)
	if issued.Status.Token == "" {
		t.Fatal("empty issuer Kubernetes token")
	}
	cluster := source.Clusters[source.Contexts[source.CurrentContext].Cluster]
	if cluster == nil || cluster.InsecureSkipTLSVerify || len(cluster.CertificateAuthorityData) == 0 {
		t.Fatal("issuer requires verified Kubernetes TLS")
	}
	// Construct from an allowlist; do not retain client certificates, exec plugins,
	// impersonation, auth providers, token files or other bootstrap credentials.
	restricted := clientcmdapi.Config{
		Clusters:  map[string]*clientcmdapi.Cluster{"proof": {Server: cluster.Server, TLSServerName: cluster.TLSServerName, CertificateAuthorityData: cluster.CertificateAuthorityData}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"issuer": {Token: issued.Status.Token}},
		Contexts:  map[string]*clientcmdapi.Context{"issuer": {Cluster: "proof", AuthInfo: "issuer", Namespace: namespace}}, CurrentContext: "issuer",
	}
	raw, err := clientcmd.Write(restricted)
	must(t, err)
	path := filepath.Join(dir, "issuer-kubeconfig")
	must(t, os.WriteFile(path, raw, 0600))
	rest, err := clientcmd.NewDefaultClientConfig(restricted, &clientcmd.ConfigOverrides{}).ClientConfig()
	must(t, err)
	reader, err := kubernetes.NewForConfig(rest)
	must(t, err)
	for _, name := range []string{"operator", "runtime", "agent", "model"} {
		_, err := reader.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
		must(t, err)
	}
	refused := func(label string, err error) {
		t.Helper()
		if !apierrors.IsForbidden(err) {
			t.Fatalf("issuer %s: expected Forbidden, got %v", label, err)
		}
	}
	_, err = reader.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	refused("secret listing", err)
	_, err = reader.CoreV1().Secrets(namespace).Get(ctx, "issuer-probe", metav1.GetOptions{})
	refused("secret reading", err)
	_, err = reader.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	refused("approval listing", err)
	_, err = reader.CoreV1().ConfigMaps(namespace).Get(ctx, "unbound", metav1.GetOptions{})
	refused("unbound approval", err)
	_, err = reader.CoreV1().ConfigMaps("default").Get(ctx, "operator", metav1.GetOptions{})
	refused("foreign namespace approval", err)
	_, err = reader.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "issuer-probe"}}, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	refused("approval creation", err)
	_, err = reader.CoreV1().ConfigMaps(namespace).Patch(ctx, "operator", types.MergePatchType, []byte(`{"data":{"issuer-probe":"forbidden"}}`), metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}})
	refused("bound approval modification", err)
	_, err = reader.RbacV1().ClusterRoles().List(ctx, metav1.ListOptions{})
	refused("cluster RBAC listing", err)
	writeJSON(t, filepath.Join(evidence, "issuer-kubernetes-identity.json"), map[string]any{
		"serviceAccount": namespace + "/" + name, "credential": "short-lived TokenRequest; no bootstrap authentication copied",
		"approvalGets": 4, "actualForbiddenResponses": 8, "tokenExpiresAt": issued.Status.ExpirationTimestamp,
		"scope": "isolated namespace; GET-only catalogue and four named approvals; production renewal not qualified",
	})
	return path
}
