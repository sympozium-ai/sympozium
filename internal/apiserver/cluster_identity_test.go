package apiserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func clusterIdentityFor(t *testing.T, objects ...client.Object) ClusterIdentityResponse {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	srv.SetVersion("v1.2.3")
	res := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/cluster/identity", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status %d: %s", res.Code, res.Body.String())
	}
	var got ClusterIdentityResponse
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func kubeSystem(uid string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)}}
}

func identityNode(name, providerID string, roles ...string) *corev1.Node {
	labels := map[string]string{"kubernetes.io/hostname": name}
	for _, role := range roles {
		labels["node-role.kubernetes.io/"+role] = ""
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{ProviderID: providerID},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.33.1"}},
	}
}

func TestClusterIdentityReportsKubeadmNameNodesAndVersions(t *testing.T) {
	got := clusterIdentityFor(t,
		kubeSystem("0f3c9a52-aaaa-bbbb-cccc-1234567890ab"),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "kubeadm-config", Namespace: "kube-system"},
			Data:       map[string]string{"ClusterConfiguration": "apiVersion: kubeadm.k8s.io/v1beta4\nclusterName: homelab\nkind: ClusterConfiguration\n"},
		},
		identityNode("worker-1", ""),
		identityNode("cp-1", "", "control-plane"),
	)
	if got.ClusterID != "0f3c9a52-aaaa-bbbb-cccc-1234567890ab" || got.Name != "homelab" {
		t.Fatalf("identity: %+v", got)
	}
	if got.SympoziumVersion != "v1.2.3" || got.KubernetesVersion != "v1.33.1" {
		t.Fatalf("versions: %+v", got)
	}
	if got.NodeCount != 2 || len(got.Nodes) != 2 || got.Nodes[0].Name != "cp-1" || got.Nodes[1].Name != "worker-1" {
		t.Fatalf("nodes: %+v", got.Nodes)
	}
	if len(got.Nodes[0].Roles) != 1 || got.Nodes[0].Roles[0] != "control-plane" || len(got.Nodes[1].Roles) != 0 {
		t.Fatalf("roles: %+v", got.Nodes)
	}
}

func TestClusterIdentityWithOnlyTheNamespaceIsNotAnError(t *testing.T) {
	got := clusterIdentityFor(t, kubeSystem("uid-only"))
	if got.ClusterID != "uid-only" || got.Name != "" || got.KubernetesVersion != "" || got.NodeCount != 0 || got.Nodes == nil {
		t.Fatalf("identity: %+v", got)
	}
}

func TestClusterIdentityWithNothingReadableIsStillOK(t *testing.T) {
	got := clusterIdentityFor(t)
	if got.ClusterID != "" || got.SympoziumVersion != "v1.2.3" {
		t.Fatalf("identity: %+v", got)
	}
}

func TestClusterIdentityFallsBackToKindProviderID(t *testing.T) {
	got := clusterIdentityFor(t, kubeSystem("kind-uid"),
		identityNode("dev-control-plane", "kind://docker/dev/dev-control-plane", "control-plane"))
	if got.Name != "dev" {
		t.Fatalf("name: %q", got.Name)
	}
}

func TestClusterIdentityCapsTheNodeList(t *testing.T) {
	objects := []client.Object{kubeSystem("big")}
	for i := 0; i < clusterIdentityNodeCap+5; i++ {
		objects = append(objects, identityNode(fmt.Sprintf("node-%02d", i), ""))
	}
	got := clusterIdentityFor(t, objects...)
	if got.NodeCount != clusterIdentityNodeCap+5 || len(got.Nodes) != clusterIdentityNodeCap {
		t.Fatalf("count %d, listed %d", got.NodeCount, len(got.Nodes))
	}
}
