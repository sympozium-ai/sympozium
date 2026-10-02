package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReport(t *testing.T) {
	dir := t.TempDir()
	if err := writeReport(dir, report{Epoch: "synthetic-unit-test"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "report.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if value["installedAcceptance"] != false {
		t.Fatal("partial runner must not claim installed acceptance")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("report permissions: %v", info.Mode())
	}
	if err := writeReport(filepath.Join(dir, "missing"), report{}); err == nil {
		t.Fatal("missing directory write silently succeeded")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeReport(dir, report{}); err == nil {
		t.Fatal("unwritable report target silently succeeded")
	}
}
