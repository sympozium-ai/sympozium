package main

import (
	"testing"

	"helm.sh/helm/v3/pkg/release"
)

func revisions(statuses ...release.Status) []*release.Release {
	var out []*release.Release
	for i, s := range statuses {
		out = append(out, &release.Release{Version: i + 1, Info: &release.Info{Status: s}})
	}
	return out
}

// A release that was ever deployed is upgraded, even after a failed attempt:
// uninstalling it would delete every object the chart owns.
func TestReleasePlanNeverUninstallsAOnceDeployedRelease(t *testing.T) {
	for name, tc := range map[string]struct {
		history []*release.Release
		want    releaseAction
		fails   bool
	}{
		"no release":                    {nil, planInstall, false},
		"deployed":                      {revisions(release.StatusSuperseded, release.StatusDeployed), planUpgrade, false},
		"failed after a deployed one":   {revisions(release.StatusSuperseded, release.StatusDeployed, release.StatusFailed), planUpgrade, false},
		"two failures after a deployed": {revisions(release.StatusDeployed, release.StatusFailed, release.StatusFailed), planUpgrade, false},
		"never deployed":                {revisions(release.StatusFailed), planReinstall, false},
		"uninstalled with history":      {revisions(release.StatusUninstalled), planReinstall, false},
		"operation in progress":         {revisions(release.StatusDeployed, release.StatusPendingUpgrade), 0, true},
	} {
		got, err := releasePlan("sympozium", "sympozium-system", tc.history)
		if (err != nil) != tc.fails || (!tc.fails && got != tc.want) {
			t.Errorf("%s: plan %v err %v, want %v (error %v)", name, got, err, tc.want, tc.fails)
		}
	}
}
