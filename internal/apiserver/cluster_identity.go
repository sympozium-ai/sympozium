package apiserver

import (
	"context"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

// clusterIdentityNodeCap bounds the node list in the identity response; the
// total is always reported in NodeCount.
const clusterIdentityNodeCap = 20

// ClusterIdentityNode is one node in the identity response.
type ClusterIdentityNode struct {
	Name           string   `json:"name"`
	Roles          []string `json:"roles"`
	KubeletVersion string   `json:"kubeletVersion,omitempty"`
}

// ClusterIdentityResponse is the response for GET /api/v1/cluster/identity.
// It answers "which cluster is this console talking to?". Every field is best
// effort: a missing permission or object yields an empty field, never an error.
type ClusterIdentityResponse struct {
	// ClusterID is the kube-system namespace UID.
	ClusterID string `json:"clusterID"`
	// Name is the kubeadm clusterName, else the Kind cluster name derived from
	// node providerIDs, else empty.
	Name              string                `json:"name"`
	KubernetesVersion string                `json:"kubernetesVersion"`
	Nodes             []ClusterIdentityNode `json:"nodes"`
	NodeCount         int                   `json:"nodeCount"`
	// SympoziumVersion is the API server's own build version.
	SympoziumVersion string `json:"sympoziumVersion"`
}

// SetVersion records the API server's build version for the identity endpoint.
func (s *Server) SetVersion(v string) {
	s.version = v
}

func (s *Server) getClusterIdentity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := ClusterIdentityResponse{
		Nodes:            []ClusterIdentityNode{},
		SympoziumVersion: s.version,
	}

	if id, err := cellninstall.ClusterIdentity(ctx, s.client); err == nil {
		resp.ClusterID = id
	}

	var nodes corev1.NodeList
	if err := s.client.List(ctx, &nodes); err != nil {
		nodes.Items = nil
	}
	sort.Slice(nodes.Items, func(i, j int) bool { return nodes.Items[i].Name < nodes.Items[j].Name })
	resp.NodeCount = len(nodes.Items)
	for i, node := range nodes.Items {
		if i >= clusterIdentityNodeCap {
			break
		}
		resp.Nodes = append(resp.Nodes, ClusterIdentityNode{
			Name:           node.Name,
			Roles:          nodeRoles(&node),
			KubeletVersion: node.Status.NodeInfo.KubeletVersion,
		})
	}

	resp.Name = s.kubeadmClusterName(ctx)
	if resp.Name == "" {
		resp.Name = kindClusterName(nodes.Items)
	}

	if s.kube != nil {
		if info, err := s.kube.Discovery().ServerVersion(); err == nil && info != nil {
			resp.KubernetesVersion = info.GitVersion
		}
	}
	if resp.KubernetesVersion == "" && len(nodes.Items) > 0 {
		resp.KubernetesVersion = nodes.Items[0].Status.NodeInfo.KubeletVersion
	}

	writeJSON(w, resp)
}

// kubeadmClusterName reads clusterName from kube-system/kubeadm-config. Kind
// clusters are kubeadm clusters whose clusterName is the Kind cluster name, so
// this also separates a Kind cluster from a host kubeadm one ("kubernetes").
// The typed clientset is preferred so a single read does not start a
// cluster-wide ConfigMap informer on the cached client.
func (s *Server) kubeadmClusterName(ctx context.Context) string {
	var data map[string]string
	if s.kube != nil {
		cm, err := s.kube.CoreV1().ConfigMaps("kube-system").Get(ctx, "kubeadm-config", metav1.GetOptions{})
		if err != nil {
			return ""
		}
		data = cm.Data
	} else {
		var cm corev1.ConfigMap
		if err := s.client.Get(ctx, types.NamespacedName{Namespace: "kube-system", Name: "kubeadm-config"}, &cm); err != nil {
			return ""
		}
		data = cm.Data
	}
	var cfg struct {
		ClusterName string `json:"clusterName"`
	}
	if err := yaml.Unmarshal([]byte(data["ClusterConfiguration"]), &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.ClusterName)
}

// kindClusterName derives the Kind cluster name from node providerIDs
// (kind://<provider>/<cluster>/<node>). Kind sets no cluster label on nodes;
// io.x-k8s.kind.cluster is a container label, invisible from the API.
func kindClusterName(nodes []corev1.Node) string {
	for _, node := range nodes {
		rest, ok := strings.CutPrefix(node.Spec.ProviderID, "kind://")
		if !ok {
			continue
		}
		if parts := strings.Split(rest, "/"); len(parts) == 3 && parts[1] != "" {
			return parts[1]
		}
	}
	return ""
}

func nodeRoles(node *corev1.Node) []string {
	roles := []string{}
	for label := range node.Labels {
		if role, ok := strings.CutPrefix(label, "node-role.kubernetes.io/"); ok && role != "" {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	return roles
}
