package cellnparent

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ApprovalConfig is provisioned by the operator/issuer, never by an AgentRun.
// Per-run approval is the initial MLP bridge; automatic catalogue issuance is
// separate work. File removal/replacement revokes selection on the next load.
type ApprovalConfig struct {
	APIVersion string        `json:"apiVersion"`
	Approvals  []RunApproval `json:"approvals"`
}

type RunApproval struct {
	Namespace string                 `json:"namespace"`
	Name      string                 `json:"name"`
	Binding   api.CellnParentBinding `json:"binding"`
	TokenFile string                 `json:"tokenFile"`
	CAFile    string                 `json:"caFile,omitempty"`
}

func boundedFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("absolute operator file path required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("operator file unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("operator file unreadable or oversized")
	}
	return data, nil
}

// LoadApproval rereads trusted deployment configuration and validates exact run
// identity/intent. No network call, credential read, persistence or host issuance
// occurs here. Caller must close the returned client's transport.
func LoadApproval(path string, run *api.AgentRun) (api.CellnParentBinding, *Client, error) {
	return loadBinding(path, run, false)
}

// recovery is private to the joined-stop path. It permits no fresh admission:
// the complete operator binding must still equal the saved original binding.
func loadBinding(path string, run *api.AgentRun, recovery bool) (api.CellnParentBinding, *Client, error) {
	var zero api.CellnParentBinding
	// Optional operator-owned directory mode reads only this immutable UID's
	// record. It never scans or merges other runs' approvals, and existing
	// single-file configuration remains supported unchanged.
	directory := false
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		if !filepath.IsAbs(path) || run.UID == "" {
			return zero, nil, fmt.Errorf("absolute approval directory and run UID required")
		}
		directory = true
		path = filepath.Join(path, approvalFileName(string(run.UID)))
	}
	data, err := boundedFile(path, 1<<20)
	if err != nil {
		return zero, nil, err
	}
	var config ApprovalConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || config.APIVersion != "sympozium.ai/celln-parent-controller-v1" || len(config.Approvals) > 1024 {
		return zero, nil, fmt.Errorf("invalid parent operator configuration")
	}
	if directory && len(config.Approvals) != 1 {
		return zero, nil, fmt.Errorf("per-run approval file must contain exactly one binding")
	}
	seenRuns := map[string]bool{}
	seenParents := map[string]bool{}
	var selected *RunApproval
	for i := range config.Approvals {
		entry := &config.Approvals[i]
		// A UID and incarnation each have one owner/credential binding. Reject
		// duplicates globally rather than selecting the first matching entry.
		if entry.Namespace == "" || entry.Name == "" || entry.Binding.RunUID == "" || seenRuns[entry.Binding.RunUID] || seenParents[entry.Binding.Incarnation] || !hashPattern.MatchString(entry.Binding.Incarnation) || !hashPattern.MatchString(entry.Binding.LaunchProfile) || !filepath.IsAbs(entry.TokenFile) || (entry.CAFile != "" && !filepath.IsAbs(entry.CAFile)) {
			return zero, nil, fmt.Errorf("invalid or ambiguous parent operator binding")
		}
		seenRuns[entry.Binding.RunUID] = true
		seenParents[entry.Binding.Incarnation] = true
		if entry.Namespace == run.Namespace && entry.Name == run.Name && entry.Binding.RunUID == string(run.UID) {
			selected = entry
		}
	}
	if selected == nil {
		return zero, nil, fmt.Errorf("no operator approval for this parent run UID")
	}
	if recovery {
		if run.Status.CellnParent == nil || run.Status.CellnParent.Binding != selected.Binding {
			return zero, nil, fmt.Errorf("recovery must retain the original parent binding")
		}
	} else if err := ValidateAdmission(run, selected.Binding); err != nil {
		return zero, nil, err
	}
	var roots *x509.CertPool
	if selected.CAFile != "" {
		pem, err := boundedFile(selected.CAFile, 1<<20)
		if err != nil {
			return zero, nil, err
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return zero, nil, fmt.Errorf("invalid parent trust bundle")
		}
	}
	transport, err := New(Options{URL: selected.Binding.Target, TokenFile: selected.TokenFile, Roots: roots, AllowInsecure: os.Getenv("CELLN_ALLOW_INSECURE_HTTP") == "true"})
	if err != nil {
		return zero, nil, err
	}
	return selected.Binding, transport, nil
}
