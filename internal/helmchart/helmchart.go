// Package helmchart provides helpers for loading the embedded Sympozium Helm chart.
package helmchart

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"

	"github.com/sympozium-ai/sympozium/charts"
	"github.com/sympozium-ai/sympozium/config/ergoz"
)

// Load returns the embedded Sympozium Helm chart, ready for use with the
// Helm SDK action package.
func Load() (*chart.Chart, error) {
	files, err := collectFiles("sympozium")
	if err != nil {
		return nil, fmt.Errorf("reading embedded chart: %w", err)
	}
	ch, err := loader.LoadFiles(files)
	if err != nil {
		return nil, fmt.Errorf("loading helm chart: %w", err)
	}
	return ch, nil
}

// AppVersion returns the embedded chart's appVersion: the most recent release
// a binary built from source knows about.
func AppVersion() (string, error) {
	ch, err := Load()
	if err != nil {
		return "", err
	}
	if ch.Metadata == nil || ch.Metadata.AppVersion == "" {
		return "", fmt.Errorf("embedded chart carries no appVersion")
	}
	return ch.Metadata.AppVersion, nil
}

// collectFiles walks the embedded filesystem and returns chart files relative
// to the chart root.
func collectFiles(root string) ([]*loader.BufferedFile, error) {
	var files []*loader.BufferedFile
	err := fs.WalkDir(charts.Sympozium, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(charts.Sympozium, path)
		if err != nil {
			return err
		}
		// Strip the root prefix so paths are relative to the chart directory
		// (e.g. "sympozium/Chart.yaml" → "Chart.yaml").
		rel := path[len(root)+1:]
		files = append(files, &loader.BufferedFile{Name: rel, Data: data})
		return nil
	})
	return files, err
}

// LoadErgoz returns the vendored ergoz chart after checking it is exactly
// the release config/ergoz/release.json pins.
func LoadErgoz() (*chart.Chart, ergoz.Release, error) {
	pin, err := ergoz.Pinned()
	if err != nil {
		return nil, ergoz.Release{}, fmt.Errorf("reading the ergoz pin: %w", err)
	}
	data, err := fs.ReadFile(charts.Ergoz, "ergoz/"+pin.Chart)
	if err != nil {
		return nil, pin, fmt.Errorf("vendored ergoz chart %s missing: %w", pin.Chart, err)
	}
	if sum := fmt.Sprintf("%x", sha256.Sum256(data)); sum != pin.ChartSHA256 {
		return nil, pin, fmt.Errorf("vendored ergoz chart %s does not match its pin", pin.Chart)
	}
	ch, err := loader.LoadArchive(bytes.NewReader(data))
	if err != nil {
		return nil, pin, fmt.Errorf("loading ergoz chart: %w", err)
	}
	return ch, pin, nil
}
