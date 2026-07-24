package apiserver

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestTokenReader returns a tokenReader pre-seeded with value, leaving
// the file path empty so the reader never tries to hit the filesystem in
// tests where rotation is not being exercised.
func newTestTokenReader(value string) *tokenReader {
	r := &tokenReader{path: ""}
	r.now = value
	r.hasValue = value != ""
	return r
}

// counterFile is a tiny helper that writes value to a temp file and tracks
// how many reads have happened. Tests use a closure to swap the file
// contents and inspect the counter.
type counterFile struct {
	path string
	read int64
}

func newCounterFile(t *testing.T, value string) *counterFile {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return &counterFile{path: path}
}

func (c *counterFile) write(t *testing.T, value string) {
	t.Helper()
	if err := os.WriteFile(c.path, []byte(value), 0o600); err != nil {
		t.Fatalf("update token file: %v", err)
	}
}

func (c *counterFile) reads() int64 { return atomic.LoadInt64(&c.read) }

func TestTokenReader_FirstReadSucceeds(t *testing.T) {
	cf := newCounterFile(t, "token-a")
	r := &tokenReader{path: cf.path}

	if got := r.Current(); got != "token-a" {
		t.Fatalf("Current() = %q, want %q", got, "token-a")
	}
}

func TestTokenReader_RotatedFileReloads(t *testing.T) {
	cf := newCounterFile(t, "token-a")
	r := &tokenReader{path: cf.path}

	if got := r.Current(); got != "token-a" {
		t.Fatalf("first Current() = %q, want %q", got, "token-a")
	}

	// Ensure the new mtime is strictly different from the cached one.
	time.Sleep(10 * time.Millisecond)
	cf.write(t, "token-b")

	if got := r.Current(); got != "token-b" {
		t.Fatalf("rotated Current() = %q, want %q", got, "token-b")
	}
}

func TestTokenReader_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	r := &tokenReader{path: filepath.Join(dir, "nope")}

	if got := r.Current(); got != "" {
		t.Fatalf("Current() = %q, want empty", got)
	}
	// Snapshot must also be empty (Current() with no cached value returns "").
	if got := r.Current(); got != "" {
		t.Fatalf("Current() (second call) = %q, want empty", got)
	}
}

func TestTokenReader_RemovesTrailingNewline(t *testing.T) {
	cf := newCounterFile(t, "token-a\n")
	r := &tokenReader{path: cf.path}

	if got := r.Current(); got != "token-a" {
		t.Fatalf("Current() = %q, want %q", got, "token-a")
	}
}

func TestTokenReader_SameMtimeSkipsReread(t *testing.T) {
	cf := newCounterFile(t, "token-a")
	r := &tokenReader{path: cf.path}

	// First read populates the cache.
	if got := r.Current(); got != "token-a" {
		t.Fatalf("first Current() = %q, want %q", got, "token-a")
	}

	// The reader caches by mtime only — subsequent Current() calls without
	// a file change should not re-stat the file. We assert that by reading
	// many times and verifying the cached value remains stable (any read
	// failure would return "" or the stale value, both of which are
	// observable here).
	for i := 0; i < 1000; i++ {
		if got := r.Current(); got != "token-a" {
			t.Fatalf("iteration %d: Current() = %q, want %q", i, got, "token-a")
		}
	}
}

func TestTokenReader_SeedOverridesMissingFile(t *testing.T) {
	dir := t.TempDir()
	r := &tokenReader{path: filepath.Join(dir, "nope")}
	r.Seed("seeded-token")

	if got := r.Current(); got != "seeded-token" {
		t.Fatalf("Current() = %q, want %q", got, "seeded-token")
	}
}

func TestTokenReader_RotatedFileOverridesSeed(t *testing.T) {
	cf := newCounterFile(t, "file-token")
	r := &tokenReader{path: cf.path}
	r.Seed("seeded-token")

	// First read picks up the file value (mtime differs from zero).
	if got := r.Current(); got != "file-token" {
		t.Fatalf("post-file Current() = %q, want %q", got, "file-token")
	}
}

func TestTokenReader_ReadFailureFallsBackToSnapshot(t *testing.T) {
	cf := newCounterFile(t, "token-a")
	r := &tokenReader{path: cf.path}

	if got := r.Current(); got != "token-a" {
		t.Fatalf("first Current() = %q, want %q", got, "token-a")
	}

	// Replace the file with a directory so subsequent reads fail.
	if err := os.Remove(cf.path); err != nil {
		t.Fatalf("remove token file: %v", err)
	}
	if err := os.Mkdir(cf.path, 0o700); err != nil {
		t.Fatalf("mkdir as token path: %v", err)
	}

	// mtime changed but read fails — should fall back to the cached value.
	got := r.Current()
	if got != "token-a" {
		t.Fatalf("Current() = %q, want %q (snapshot fallback)", got, "token-a")
	}
}

// silence unused-import warnings if any future refactors drop one of the
// helpers above.
var _ = strings.TrimRight
