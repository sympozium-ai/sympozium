package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodeCanRunCellsNeedsTheDeviceAndAKernel(t *testing.T) {
	boot := t.TempDir()
	if nodeCanRunCells("/dev/null", filepath.Join(boot, "vmlinuz*")) {
		t.Fatal("no kernel: must not label")
	}
	if err := os.WriteFile(filepath.Join(boot, "vmlinuz-6.1"), []byte("k"), 0600); err != nil {
		t.Fatal(err)
	}
	if !nodeCanRunCells("/dev/null", filepath.Join(boot, "vmlinuz*")) {
		t.Fatal("a character device and a kernel: must label")
	}
	if nodeCanRunCells(filepath.Join(boot, "vmlinuz-6.1"), filepath.Join(boot, "vmlinuz*")) {
		t.Fatal("a regular file is not a KVM device")
	}
	if nodeCanRunCells(filepath.Join(boot, "missing"), filepath.Join(boot, "vmlinuz*")) {
		t.Fatal("no device: must not label")
	}
}
