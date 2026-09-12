package apiserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CellnPreviewBinding struct {
	Agent          types.NamespacedName `json:"agent"`
	OperatorSource types.NamespacedName `json:"operatorSource"`
	RuntimeSource  types.NamespacedName `json:"runtimeSource"`
	AgentSource    types.NamespacedName `json:"agentSource"`
}

// ConfigureCellnPreview must run before serving. Configuration is operator-owned
// and contains only grant locations, never issuer/router credentials. Reader
// must be uncached. No source locations may be supplied by an HTTP caller.
func (s *Server) ConfigureCellnPreview(reader client.Reader, bindings []CellnPreviewBinding) error {
	if reader == nil || len(bindings) == 0 || len(bindings) > 1024 {
		return fmt.Errorf("reader and bounded preview bindings required")
	}
	configured := map[types.NamespacedName]cellnauthority.Loader{}
	for _, b := range bindings {
		if b.Agent.Namespace == "" || b.Agent.Name == "" {
			return fmt.Errorf("namespaced Agent required")
		}
		if _, exists := configured[b.Agent]; exists {
			return fmt.Errorf("duplicate Agent binding")
		}
		seen := map[types.NamespacedName]bool{}
		for _, ref := range []types.NamespacedName{b.OperatorSource, b.RuntimeSource, b.AgentSource} {
			if ref.Namespace == "" || ref.Name == "" || seen[ref] {
				return fmt.Errorf("three distinct configured sources required")
			}
			seen[ref] = true
		}
		configured[b.Agent] = cellnauthority.Loader{Reader: reader, OperatorSource: b.OperatorSource, RuntimeSource: b.RuntimeSource, AgentSource: b.AgentSource}
	}
	s.cellnPreview = configured
	return nil
}

func (s *Server) LoadCellnPreview(path string, reader client.Reader) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute preview configuration path required")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("preview configuration unreadable or oversized")
	}
	var config struct {
		APIVersion string                `json:"apiVersion"`
		Bindings   []CellnPreviewBinding `json:"bindings"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&config) != nil || d.Decode(new(any)) != io.EOF || config.APIVersion != "sympozium.ai/celln-permission-preview-v1" {
		return fmt.Errorf("invalid preview configuration")
	}
	return s.ConfigureCellnPreview(reader, config.Bindings)
}

func (s *Server) previewCellnSelection(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	var req struct {
		AgentRef  string                      `json:"agentRef"`
		Selection api.CellnCatalogueSelection `json:"cellnSelection"`
		Lifecycle string                      `json:"executionLifecycle,omitempty"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || req.AgentRef == "" || (req.Lifecycle != "" && req.Lifecycle != "one-shot" && req.Lifecycle != "enduring") {
		http.Error(w, "bounded explicit catalogue selection required", http.StatusBadRequest)
		return
	}
	if len(req.Selection.ClusterToolRefs) != 0 {
		http.Error(w, "AUTH_PROTOCOL_UNSUPPORTED: shared catalogue preview is not available on the legacy endpoint; no execution authorized", http.StatusUnprocessableEntity)
		return
	}
	l, ok := s.cellnPreview[types.NamespacedName{Namespace: ns, Name: req.AgentRef}]
	if !ok {
		http.Error(w, "Permission preview is not configured for this Agent; readiness is not established", http.StatusServiceUnavailable)
		return
	}
	preview, err := l.PreviewLifecycle(r.Context(), types.NamespacedName{Namespace: ns, Name: req.AgentRef}, req.Selection, req.Lifecycle)
	if err != nil {
		// Do not expose privileged ConfigMap names/contents or other bindings.
		http.Error(w, "Selection permissions could not be resolved from current approvals; no execution authorized", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, preview)
}
