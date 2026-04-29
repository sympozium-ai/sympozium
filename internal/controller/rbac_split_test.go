package controller

import (
	"os"
	"strings"
	"testing"
)

// TestRBACSplit_ControllerAndApiserverHaveSeparateRoles validates Fix 4:
// the sympozium-manager ClusterRole should only bind to the controller SA,
// and a separate sympozium-apiserver ClusterRole should exist for the apiserver.
func TestRBACSplit_ControllerAndApiserverHaveSeparateRoles(t *testing.T) {
	rbacPath := "../../charts/sympozium/templates/rbac.yaml"
	data, err := os.ReadFile(rbacPath)
	if err != nil {
		t.Fatalf("read rbac.yaml: %v", err)
	}
	content := string(data)

	// There should be a separate apiserver ClusterRole
	if !strings.Contains(content, "-apiserver") {
		t.Error("rbac.yaml should contain a separate apiserver ClusterRole")
	}

	// The manager ClusterRoleBinding should NOT bind to sympozium-apiserver
	// Split on "---" to find the manager binding
	docs := strings.Split(content, "---")
	for _, doc := range docs {
		if strings.Contains(doc, "ClusterRoleBinding") &&
			strings.Contains(doc, "-manager") &&
			!strings.Contains(doc, "-apiserver") {
			// This is the manager binding — verify it only has controller SA
			if strings.Contains(doc, "name: sympozium-apiserver") {
				t.Error("manager ClusterRoleBinding should NOT bind to sympozium-apiserver ServiceAccount")
			}
		}
	}

	// The apiserver ClusterRoleBinding should bind to sympozium-apiserver
	foundApiserverBinding := false
	for _, doc := range docs {
		if strings.Contains(doc, "ClusterRoleBinding") && strings.Contains(doc, "-apiserver") {
			foundApiserverBinding = true
			if !strings.Contains(doc, "name: sympozium-apiserver") {
				t.Error("apiserver ClusterRoleBinding should bind to sympozium-apiserver ServiceAccount")
			}
		}
	}
	if !foundApiserverBinding {
		t.Error("should have a separate apiserver ClusterRoleBinding")
	}
}

// TestRBACSplit_ApiserverHasNodeAccess validates that the apiserver ClusterRole
// includes read access to nodes, which is required for the topology view,
// provider discovery, and cluster status endpoints.
func TestRBACSplit_ApiserverHasNodeAccess(t *testing.T) {
	rbacPath := "../../charts/sympozium/templates/rbac.yaml"
	data, err := os.ReadFile(rbacPath)
	if err != nil {
		t.Fatalf("read rbac.yaml: %v", err)
	}
	content := string(data)

	docs := strings.Split(content, "---")
	for _, doc := range docs {
		if strings.Contains(doc, "kind: ClusterRole") &&
			strings.Contains(doc, "-apiserver") &&
			!strings.Contains(doc, "ClusterRoleBinding") {
			if !strings.Contains(doc, `"nodes"`) {
				t.Error("apiserver ClusterRole must include 'nodes' resource for topology/cluster-status endpoints")
			}
		}
	}
}

// TestRBACSplit_ApiserverLacksRBACDelegation validates that the apiserver
// ClusterRole cannot create/modify RBAC rules (no privilege escalation path).
func TestRBACSplit_ApiserverLacksRBACDelegation(t *testing.T) {
	rbacPath := "../../charts/sympozium/templates/rbac.yaml"
	data, err := os.ReadFile(rbacPath)
	if err != nil {
		t.Fatalf("read rbac.yaml: %v", err)
	}
	content := string(data)

	// Split by --- to find apiserver ClusterRole (not binding)
	docs := strings.Split(content, "---")
	for _, doc := range docs {
		if strings.Contains(doc, "kind: ClusterRole") &&
			strings.Contains(doc, "-apiserver") &&
			!strings.Contains(doc, "ClusterRoleBinding") {
			// The apiserver ClusterRole should NOT have RBAC delegation
			if strings.Contains(doc, `"clusterroles"`) {
				t.Error("apiserver ClusterRole should NOT have access to clusterroles")
			}
			if strings.Contains(doc, `"clusterrolebindings"`) {
				t.Error("apiserver ClusterRole should NOT have access to clusterrolebindings")
			}
			if strings.Contains(doc, `"roles"`) {
				t.Error("apiserver ClusterRole should NOT have access to roles")
			}
			if strings.Contains(doc, `"rolebindings"`) {
				t.Error("apiserver ClusterRole should NOT have access to rolebindings")
			}
			// Should NOT have pods/exec
			if strings.Contains(doc, "pods/exec") {
				t.Error("apiserver ClusterRole should NOT have pods/exec access")
			}
		}
	}
}
