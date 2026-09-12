// Package cellnevidence validates the shape and artifact integrity of release
// evidence. It cannot attest that a reported test ran or that hardware isolated it.
package cellnevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

const Version = "sympozium.ai/celln-tenancy-evidence-v1"

var sha = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Row struct {
	ID        string     `json:"id"`
	Test      string     `json:"test"`
	Tier      string     `json:"tier"`
	Status    string     `json:"status"`
	Expected  string     `json:"expected"`
	Actual    string     `json:"actual"`
	Command   string     `json:"command"`
	ExitCode  *int       `json:"exitCode"`
	Artifacts []Artifact `json:"artifacts"`
}
type Manifest struct {
	APIVersion      string `json:"apiVersion"`
	SympoziumSource string `json:"sympoziumSource"`
	CellnSource     string `json:"cellnSource"`
	FixtureDigest   string `json:"fixtureDigest"`
	// Required for any claimed installed pass; values are content digests, not tags.
	Images map[string]string `json:"images"`
	Rows   []Row             `json:"rows"`
}
type Report struct {
	StructurallyValid bool     `json:"structurallyValid"`
	AllRowsClaimPass  bool     `json:"allRowsClaimPass"`
	Qualification     string   `json:"qualification"`
	Incomplete        []string `json:"incomplete"`
}

// Validate reads at most 1 MiB of manifest and 4 MiB per evidence reference.
// root is the evidence directory, not the filesystem root. os.Root prevents
// symlink/path traversal outside that directory. No artifact contents are returned.
func Validate(input io.Reader, root *os.Root) (*Report, error) {
	if root == nil {
		return nil, fmt.Errorf("evidence directory required")
	}
	raw, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, fmt.Errorf("manifest unreadable or oversized")
	}
	var m Manifest
	// Shared strict decoding rejects duplicate/unknown keys and trailing data.
	if err := cap.StrictDecode(raw, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest fields")
	}
	if m.APIVersion != Version || !sha.MatchString(m.SympoziumSource) || !sha.MatchString(m.CellnSource) || !digest.MatchString(m.FixtureDigest) {
		return nil, fmt.Errorf("version and exact source/fixture pins required")
	}
	if len(m.Rows) != 12 {
		return nil, fmt.Errorf("exactly A01-A12 are required")
	}
	report := &Report{StructurallyValid: true, AllRowsClaimPass: true, Qualification: "unverified: artifact integrity is not installed execution proof", Incomplete: []string{}}
	seen := map[string]bool{}
	for _, row := range m.Rows {
		validID := false
		for i := 1; i <= 12; i++ {
			if row.ID == fmt.Sprintf("A%02d", i) {
				validID = true
			}
		}
		if !validID || seen[row.ID] {
			return nil, fmt.Errorf("unknown or duplicate acceptance row")
		}
		seen[row.ID] = true
		if row.Test == "" || row.Expected == "" || row.Actual == "" || len(row.Artifacts) > 16 {
			return nil, fmt.Errorf("%s: bounded observations and test required", row.ID)
		}
		if row.Status != "pass" && row.Status != "fail" && row.Status != "incomplete" {
			return nil, fmt.Errorf("%s: invalid status", row.ID)
		}
		if row.Tier != "installed" && row.Tier != "not-executed" {
			return nil, fmt.Errorf("%s: component/mock evidence cannot be installed acceptance", row.ID)
		}
		if row.Status == "pass" {
			if row.Tier != "installed" || row.Command == "" || row.ExitCode == nil || *row.ExitCode != 0 || len(row.Artifacts) == 0 {
				return nil, fmt.Errorf("%s: pass requires installed command, exit zero and artifacts", row.ID)
			}
			for _, component := range []string{"sympozium", "celln"} {
				if !digest.MatchString(m.Images[component]) {
					return nil, fmt.Errorf("installed image digests required")
				}
			}
		} else {
			report.AllRowsClaimPass = false
			report.Incomplete = append(report.Incomplete, row.ID)
		}
		for _, artifact := range row.Artifacts {
			if err := verifyArtifact(root, artifact); err != nil {
				return nil, fmt.Errorf("%s: invalid or mismatched evidence artifact", row.ID)
			}
		}
	}
	return report, nil
}

func verifyArtifact(root *os.Root, a Artifact) error {
	if !filepath.IsLocal(a.Path) || !digest.MatchString(a.SHA256) {
		return fmt.Errorf("invalid reference")
	}
	file, err := root.OpenFile(a.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return fmt.Errorf("bounded regular artifact required")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, (4<<20)+1))
	if err != nil || n > 4<<20 {
		return fmt.Errorf("artifact unreadable")
	}
	if "sha256:"+hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("digest mismatch")
	}
	return nil
}
