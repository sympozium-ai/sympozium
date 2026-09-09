package cellnparent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOperatorApprovalSelectionRotationAndRunIsolation(t *testing.T) {
	run, binding := admissionFixture(t)
	root := t.TempDir()
	path := filepath.Join(root, "approvals.json")
	config := ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: filepath.Join(root, "not-yet-mounted-token")}}}
	write := func() {
		t.Helper()
		data, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	saved, transport, err := LoadApproval(path, run)
	if err != nil {
		t.Fatal(err)
	}
	transport.Close()
	if saved != binding {
		t.Fatal("binding changed")
	}
	// No model key or controller bearer is needed merely to load approval.
	replacement := run.DeepCopy()
	replacement.UID = "replacement"
	if _, _, err := LoadApproval(path, replacement); err == nil {
		t.Fatal("run name reuse inherited approval")
	}
	config.Approvals = nil
	write()
	if _, _, err := LoadApproval(path, run); err == nil {
		t.Fatal("removed approval remained cached")
	}
}

func TestOperatorApprovalRejectsAmbiguityMalformedAndOversizedFiles(t *testing.T) {
	run, binding := admissionFixture(t)
	path := filepath.Join(t.TempDir(), "approvals.json")
	entry := RunApproval{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: "/operator/parent-token"}
	duplicate, _ := json.Marshal(ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: []RunApproval{entry, entry}})
	for _, raw := range [][]byte{duplicate, []byte(`{"apiVersion":"sympozium.ai/celln-parent-controller-v1","approvals":[],"token":"tenant-supplied"}`), make([]byte, (1<<20)+1)} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadApproval(path, run); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	if _, _, err := LoadApproval("relative.json", run); err == nil {
		t.Fatal("relative config accepted")
	}
}

func TestApprovalDirectoryIsolatesRunUIDAndRefusesAmbiguousRecords(t *testing.T) {
	run, binding := admissionFixture(t)
	dir := t.TempDir()
	entry := RunApproval{Namespace: run.Namespace, Name: run.Name, Binding: binding, TokenFile: "/operator/token"}
	write := func(entries []RunApproval) {
		t.Helper()
		raw, err := json.Marshal(ApprovalConfig{APIVersion: "sympozium.ai/celln-parent-controller-v1", Approvals: entries})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, approvalFileName(string(run.UID))), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write([]RunApproval{entry})
	loaded, transport, err := LoadApproval(dir, run)
	if err != nil {
		t.Fatal(err)
	}
	transport.Close()
	if loaded != binding {
		t.Fatal("directory changed binding")
	}
	replacement := run.DeepCopy()
	replacement.UID = "replacement"
	if _, _, err := LoadApproval(dir, replacement); err == nil {
		t.Fatal("name reuse inherited approval")
	}
	write([]RunApproval{entry, entry})
	if _, _, err := LoadApproval(dir, run); err == nil {
		t.Fatal("ambiguous per-run file accepted")
	}
	write([]RunApproval{entry})
	if err := os.Remove(filepath.Join(dir, approvalFileName(string(run.UID)))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadApproval(dir, run); err == nil {
		t.Fatal("removed approval remained readable")
	}
}
