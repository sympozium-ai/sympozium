// celln-tenancy-evidence checks evidence structure/integrity, never release readiness.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/sympozium-ai/sympozium/internal/cellnevidence"
	"os"
	"path/filepath"
	"syscall"
)

func main() {
	path := flag.String("manifest", "", "evidence manifest; artifacts resolve within its directory")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "explicit manifest required; this is not a release runner")
		os.Exit(2)
	}
	if err := run(*path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(path string) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("evidence directory unavailable")
	}
	defer root.Close()
	input, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("manifest unavailable")
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("regular manifest file required")
	}
	report, err := cellnevidence.Validate(input, root)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
