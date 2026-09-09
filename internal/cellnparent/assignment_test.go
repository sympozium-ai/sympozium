package cellnparent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestParentAssignmentConcurrentOneWinnerAndStableRecovery(t *testing.T) {
	dir := t.TempDir()
	base := RunApproval{Namespace: "tenant", Name: "parent", TokenFile: "/operator/token", Binding: api.CellnParentBinding{RunUID: "one", Incarnation: "blake3:" + strings.Repeat("a", 64)}}
	other := base
	other.Binding.RunUID = "two"
	start := make(chan struct{})
	var wg sync.WaitGroup
	errors := make([]error, 2)
	for i, entry := range []RunApproval{base, other} {
		wg.Add(1)
		go func(i int, entry RunApproval) {
			defer wg.Done()
			<-start
			errors[i] = claimParentAssignment(dir, entry, "registration")
		}(i, entry)
	}
	close(start)
	wg.Wait()
	if (errors[0] == nil) == (errors[1] == nil) {
		t.Fatalf("wanted exactly one assignment: %v", errors)
	}
	winner := base
	if errors[1] == nil {
		winner = other
	}
	for i := 0; i < 3; i++ {
		if err := claimParentAssignment(dir, winner, "registration"); err != nil {
			t.Fatal(err)
		}
	}
	if err := claimParentAssignment(dir, winner, "changed-registration"); err == nil {
		t.Fatal("registration replacement reused a claim")
	}
	rotated := winner
	rotated.TokenFile = "/operator/another-token"
	if err := claimParentAssignment(dir, rotated, "registration"); err == nil {
		t.Fatal("credential retargeting reused claim")
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("temporary assignment files leaked: %d", len(files))
	}
	info, err := files[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("assignment permissions: %v", info.Mode())
	}
}

func TestParentAssignmentRefusesCorruptionAndMissingJournal(t *testing.T) {
	dir := t.TempDir()
	entry := RunApproval{Binding: api.CellnParentBinding{RunUID: "run", Incarnation: "blake3:" + strings.Repeat("b", 64)}}
	path := filepath.Join(dir, entry.Binding.Incarnation[7:]+".json")
	if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := claimParentAssignment(dir, entry, "registration"); err == nil {
		t.Fatal("corrupt claim overwritten")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "partial" {
		t.Fatal("existing evidence changed")
	}
	if err := claimParentAssignment(filepath.Join(dir, "absent"), entry, "registration"); err == nil {
		t.Fatal("missing journal silently created")
	}
	if err := claimParentAssignment("relative", entry, "registration"); err == nil {
		t.Fatal("relative journal accepted")
	}
}
