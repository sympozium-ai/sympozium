package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// `sympozium install --celln-fleet` on a build that pins a starter package
// needs no package flags: the package identity comes from the build and every
// node the probe finds KVM on joins by itself. Nothing about models is asked:
// the fleet calls no provider, and each Agent brings its own connection and
// key. A bare `sympozium install` deploys no Celln (#693).

const defaultFleetScope = "starter"

func defaultFleetOutputDir(scope string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("--celln-fleet-output-dir is required when the home directory is unknown: %w", err)
	}
	return filepath.Join(home, ".sympozium", "celln-fleet", scope), nil
}

// applyStarterDefaults fills the package identity, scope and output directory
// from this build's pinned starter package when the flags omit them.
func (f *cellnFleetFlags) applyStarterDefaults() error {
	if f.options.PackageImage == "" && f.options.PackageHash == "" && f.options.Publisher == "" {
		d := defaultCellnStarter()
		if !d.complete() {
			return fmt.Errorf("--celln-fleet needs --celln-fleet-package-image, --celln-fleet-package-hash and --celln-fleet-publisher: this build carries no default starter package")
		}
		f.options.PackageImage, f.options.PackageHash, f.options.Publisher = d.Image, d.PackageHash, d.Publisher
		fmt.Printf("  Using the default Celln starter package (Celln %s) at %s\n", d.CellnVersion, d.Image)
	}
	if f.options.Scope == "" {
		f.options.Scope = defaultFleetScope
	}
	if f.outputDir == "" {
		dir, err := defaultFleetOutputDir(f.options.Scope)
		if err != nil {
			return err
		}
		f.outputDir = dir
	}
	return nil
}

// cellnNotDeployedSummary is printed after an install that did not deploy
// Celln: the Job backend is the default and Celln is an option (#693).
const cellnNotDeployedSummary = "  Celln: not deployed. Agents run on the Kubernetes Job backend. To add the hardware-isolated Celln fleet on KVM nodes later, rerun with --celln-fleet."

// keepDeployedFleet decides whether a bare `sympozium install` runs the fleet.
// Celln is opt-in, so only a cluster whose release already runs the fleet
// keeps it; a fresh install never deploys it. It reports what it chose.
func (f *cellnFleetFlags) keepDeployedFleet(deployed bool) bool {
	if !deployed {
		return false
	}
	f.kept = true
	fmt.Println("  Celln fleet: already deployed on this cluster; keeping it (Celln is otherwise opt-in with --celln-fleet).")
	return true
}

// deployedFleetEnabled reports whether the deployed release runs the Celln
// fleet. Only a missing release (helmReleaseInfo returns nil, nil for
// release-not-found) counts as not deployed. Any other read error aborts:
// guessing "not deployed" on a transient Helm or RBAC error would render
// celln.enabled=false and tear a live fleet down.
func deployedFleetEnabled() (bool, error) {
	return deployedFleetEnabledFrom(helmReleaseInfo)
}

func deployedFleetEnabledFrom(read func() (*releaseInfo, error)) (bool, error) {
	rel, err := read()
	if err != nil {
		return false, fmt.Errorf("reading the deployed Sympozium release to check for a Celln fleet: %w; refusing to continue, because a plain install on a cluster that runs the fleet would remove it. Fix access to the release (or pass --celln-fleet to keep a fleet, --no-celln to remove it) and rerun", err)
	}
	if rel == nil {
		return false, nil
	}
	// Both switches must be on (the chart defaults both to false). A release
	// disabled with --reuse-values --set celln.enabled=false still carries
	// celln.fleet.enabled=true; counting that as deployed would bring the
	// privileged DaemonSets back on the next plain install.
	celln, _ := nestedValue(rel.Config, "celln", "enabled").(bool)
	fleet, _ := nestedValue(rel.Config, "celln", "fleet", "enabled").(bool)
	return celln && fleet, nil
}
