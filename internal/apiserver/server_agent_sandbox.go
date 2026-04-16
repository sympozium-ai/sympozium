package apiserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	// agentSandboxDefaultVersion is the default release version of agent-sandbox to install.
	agentSandboxDefaultVersion = "v0.3.10"

	// agentSandboxReleaseURL is the URL template for agent-sandbox release manifests.
	agentSandboxReleaseURL = "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/%s/%s"

	// agentSandboxGroup is the API group for Agent Sandbox CRDs.
	agentSandboxGroup = "agents.x-k8s.io"
)

// agentSandboxManifests lists the manifest files to fetch from the release.
var agentSandboxManifests = []string{"manifest.yaml", "extensions.yaml"}

// installAgentSandboxRequest is the optional request body for installing Agent Sandbox CRDs.
type installAgentSandboxRequest struct {
	Version string `json:"version,omitempty"`
}

// installAgentSandboxCRDs fetches the Agent Sandbox CRD manifests from the
// kubernetes-sigs/agent-sandbox GitHub release and applies them to the cluster.
func (s *Server) installAgentSandboxCRDs(w http.ResponseWriter, r *http.Request) {
	var req installAgentSandboxRequest
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	version := req.Version
	if version == "" {
		version = agentSandboxDefaultVersion
	}

	var allCRDs []apiextensionsv1.CustomResourceDefinition
	for _, manifest := range agentSandboxManifests {
		url := fmt.Sprintf(agentSandboxReleaseURL, version, manifest)
		crds, err := fetchCRDsFromURL(r.Context(), url)
		if err != nil {
			s.log.Error(err, "failed to fetch Agent Sandbox manifest", "url", url)
			http.Error(w, fmt.Sprintf("failed to fetch manifest %s: %v", manifest, err), http.StatusBadGateway)
			return
		}
		allCRDs = append(allCRDs, crds...)
	}

	if len(allCRDs) == 0 {
		http.Error(w, "no CRDs found in the Agent Sandbox release manifests", http.StatusBadGateway)
		return
	}

	var installed []string
	for i := range allCRDs {
		crd := &allCRDs[i]
		// Check if CRD already exists.
		existing := &apiextensionsv1.CustomResourceDefinition{}
		err := s.client.Get(r.Context(), types.NamespacedName{Name: crd.Name}, existing)
		if err == nil {
			// Already exists — update it.
			crd.ResourceVersion = existing.ResourceVersion
			if err := s.client.Update(r.Context(), crd); err != nil {
				s.log.Error(err, "failed to update Agent Sandbox CRD", "name", crd.Name)
				http.Error(w, fmt.Sprintf("failed to update CRD %s: %v", crd.Name, err), http.StatusInternalServerError)
				return
			}
		} else if k8serrors.IsNotFound(err) {
			if err := s.client.Create(r.Context(), crd); err != nil {
				s.log.Error(err, "failed to create Agent Sandbox CRD", "name", crd.Name)
				http.Error(w, fmt.Sprintf("failed to create CRD %s: %v", crd.Name, err), http.StatusInternalServerError)
				return
			}
		} else {
			s.log.Error(err, "failed to check Agent Sandbox CRD", "name", crd.Name)
			http.Error(w, fmt.Sprintf("failed to check CRD %s: %v", crd.Name, err), http.StatusInternalServerError)
			return
		}
		installed = append(installed, crd.Name)
	}

	s.log.Info("Installed Agent Sandbox CRDs", "version", version, "crds", installed)
	writeJSON(w, map[string]interface{}{
		"installed": installed,
		"version":   version,
	})
}

// uninstallAgentSandboxCRDs removes all Agent Sandbox CRDs (agents.x-k8s.io) from the cluster.
func (s *Server) uninstallAgentSandboxCRDs(w http.ResponseWriter, r *http.Request) {
	// List all CRDs and filter by group.
	var crdList apiextensionsv1.CustomResourceDefinitionList
	if err := s.client.List(r.Context(), &crdList); err != nil {
		s.log.Error(err, "failed to list CRDs")
		http.Error(w, "failed to list CRDs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var deleted []string
	for i := range crdList.Items {
		crd := &crdList.Items[i]
		if crd.Spec.Group == agentSandboxGroup {
			if err := s.client.Delete(r.Context(), crd); err != nil && !k8serrors.IsNotFound(err) {
				s.log.Error(err, "failed to delete Agent Sandbox CRD", "name", crd.Name)
				http.Error(w, fmt.Sprintf("failed to delete CRD %s: %v", crd.Name, err), http.StatusInternalServerError)
				return
			}
			deleted = append(deleted, crd.Name)
		}
	}

	s.log.Info("Uninstalled Agent Sandbox CRDs", "crds", deleted)
	writeJSON(w, map[string]interface{}{
		"deleted": deleted,
	})
}

// fetchCRDsFromURL downloads a YAML manifest from the given URL and extracts
// only CustomResourceDefinition documents.
func fetchCRDsFromURL(ctx context.Context, url string) ([]apiextensionsv1.CustomResourceDefinition, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}

	return parseCRDsFromYAML(body)
}

// parseCRDsFromYAML splits a multi-document YAML stream and extracts CRD documents.
func parseCRDsFromYAML(data []byte) ([]apiextensionsv1.CustomResourceDefinition, error) {
	var crds []apiextensionsv1.CustomResourceDefinition

	reader := yaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	for {
		doc, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading YAML document: %w", err)
		}

		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}

		// Quick check: is this a CRD document?
		if !isCRDDocument(doc) {
			continue
		}

		// Decode YAML to JSON, then unmarshal into CRD struct.
		jsonData, err := yaml.ToJSON(doc)
		if err != nil {
			return nil, fmt.Errorf("converting YAML to JSON: %w", err)
		}

		var crd apiextensionsv1.CustomResourceDefinition
		if err := json.Unmarshal(jsonData, &crd); err != nil {
			continue // skip documents that don't parse as CRDs
		}

		// Only include CRDs from the agent-sandbox group.
		if crd.Spec.Group == agentSandboxGroup {
			crds = append(crds, crd)
		}
	}

	return crds, nil
}

// isCRDDocument does a quick string check to see if a YAML document is likely a CRD.
func isCRDDocument(doc []byte) bool {
	s := string(doc)
	return strings.Contains(s, "kind: CustomResourceDefinition") &&
		strings.Contains(s, "apiextensions.k8s.io")
}
