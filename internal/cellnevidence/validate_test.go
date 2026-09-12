package cellnevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Manifest, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	raw := []byte("synthetic unit fixture, not installed evidence\n")
	if err := os.WriteFile(filepath.Join(dir, "case.log"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	h := sha256.Sum256(raw)
	zero := 0
	m := Manifest{APIVersion: Version, SympoziumSource: strings.Repeat("a", 40), CellnSource: strings.Repeat("b", 40), FixtureDigest: "sha256:" + strings.Repeat("c", 64), Images: map[string]string{"sympozium": "sha256:" + strings.Repeat("d", 64), "celln": "sha256:" + strings.Repeat("e", 64)}}
	for i := 1; i <= 12; i++ {
		m.Rows = append(m.Rows, Row{ID: fmt.Sprintf("A%02d", i), Test: "synthetic-test", Tier: "installed", Status: "pass", Expected: "expected observation", Actual: "reported observation", Command: "synthetic-command", ExitCode: &zero, Artifacts: []Artifact{{Path: "case.log", SHA256: "sha256:" + hex.EncodeToString(h[:])}}})
	}
	return m, root
}
func validate(t *testing.T, m Manifest, root *os.Root) (*Report, error) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return Validate(bytes.NewReader(raw), root)
}
func TestValidStructureNeverQualifiesRelease(t *testing.T) {
	m, root := fixture(t)
	report, err := validate(t, m, root)
	if err != nil || !report.StructurallyValid || !report.AllRowsClaimPass || !strings.HasPrefix(report.Qualification, "unverified:") {
		t.Fatalf("structure mistaken for qualification: %+v %v", report, err)
	}
	m.Rows[0].Status = "incomplete"
	m.Rows[0].Tier = "not-executed"
	m.Rows[0].Artifacts = nil
	m.Rows[0].ExitCode = nil
	report, err = validate(t, m, root)
	if err != nil || report.AllRowsClaimPass || len(report.Incomplete) != 1 {
		t.Fatalf("incomplete evidence hidden: %+v %v", report, err)
	}
}
func TestMalformedOrLowerTierEvidenceRefused(t *testing.T) {
	mutations := map[string]func(*Manifest){
		"missing-row":     func(m *Manifest) { m.Rows = m.Rows[:11] },
		"duplicate-row":   func(m *Manifest) { m.Rows[1].ID = m.Rows[0].ID },
		"mock-tier":       func(m *Manifest) { m.Rows[0].Tier = "mock" },
		"skip":            func(m *Manifest) { m.Rows[0].Status = "skip" },
		"no-command":      func(m *Manifest) { m.Rows[0].Command = "" },
		"no-exit":         func(m *Manifest) { m.Rows[0].ExitCode = nil },
		"nonzero":         func(m *Manifest) { n := 1; m.Rows[0].ExitCode = &n },
		"no-artifact":     func(m *Manifest) { m.Rows[0].Artifacts = nil },
		"digest-mismatch": func(m *Manifest) { m.Rows[0].Artifacts[0].SHA256 = "sha256:" + strings.Repeat("0", 64) },
		"traversal":       func(m *Manifest) { m.Rows[0].Artifacts[0].Path = "../case.log" },
		"missing-file":    func(m *Manifest) { m.Rows[0].Artifacts[0].Path = "absent" },
		"mutable-image":   func(m *Manifest) { m.Images["celln"] = "latest" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m, root := fixture(t)
			mutate(&m)
			if _, err := validate(t, m, root); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}
func TestArtifactSymlinkCannotEscapeEvidenceRoot(t *testing.T) {
	m, root := fixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink(outside, "escape"); err != nil {
		t.Fatal(err)
	}
	m.Rows[0].Artifacts[0].Path = "escape"
	if _, err := validate(t, m, root); err == nil {
		t.Fatal("outside evidence read")
	}
}
func TestDuplicateManifestKeysRejected(t *testing.T) {
	m, root := fixture(t)
	raw, _ := json.Marshal(m)
	raw = append([]byte(`{"apiVersion":"ignored",`), raw[1:]...)
	if _, err := Validate(bytes.NewReader(raw), root); err == nil {
		t.Fatal("duplicate keys accepted")
	}
}
