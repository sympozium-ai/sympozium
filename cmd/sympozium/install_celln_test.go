package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSplitImageRef(t *testing.T) {
	cases := []struct {
		in     string
		repo   string
		ref    string
		digest bool
	}{
		{"", "ghcr.io/sympozium-ai/celln", "v0.5.35", false},
		{"ghcr.io/sympozium-ai/celln:v0.5.35", "ghcr.io/sympozium-ai/celln", "v0.5.35", false},
		{"ghcr.io/sympozium-ai/celln@sha256:abcdef", "ghcr.io/sympozium-ai/celln", "sha256:abcdef", true},
		{"localhost:5000/celln", "localhost:5000/celln", "v0.5.35", false},
		{"localhost:5000/celln:v1", "localhost:5000/celln", "v1", false},
	}
	for _, c := range cases {
		repo, ref, digest := splitImageRef(c.in, "ghcr.io/sympozium-ai/celln", "v0.5.35")
		if repo != c.repo || ref != c.ref || digest != c.digest {
			t.Fatalf("splitImageRef(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, repo, ref, digest, c.repo, c.ref, c.digest)
		}
	}
}

func TestCellnInstallSetValues(t *testing.T) {
	vals, err := cellnInstallSetValues(context.Background(), "ghcr.io/sympozium-ai/celln:v0.5.35", "", 1)
	if err != nil {
		t.Fatalf("cellnInstallSetValues: %v", err)
	}
	// The generated values must be parseable by the same strvals path Helm uses.
	helmVals, err := buildHelmValues("", vals)
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}
	celln, ok := helmVals["celln"].(map[string]interface{})
	if !ok {
		t.Fatalf("celln values missing: %#v", helmVals["celln"])
	}
	for _, key := range []string{"enabled", "bootstrap", "router", "tokenSecret", "capabilityTokenSecret", "image"} {
		if _, ok := celln[key]; !ok {
			t.Fatalf("celln.%s missing from %#v", key, celln)
		}
	}
	// The single in-cluster dispatcher and the host installer are gone: the
	// fleet (celln.fleet) is the only execution plane behind the router.
	for _, gone := range []string{"installer", "dispatcher"} {
		if _, has := celln[gone]; has {
			t.Fatalf("celln.%s is set: %#v", gone, celln[gone])
		}
	}
	router, _ := celln["router"].(map[string]interface{})
	if router["external"] != false || router["image"].(map[string]interface{})["tag"] != "v0.5.35" {
		t.Fatalf("router values: %#v", router)
	}
	if _, has := router["backends"]; has {
		t.Fatalf("router.backends is set; the router discovers fleet nodes by DNS: %#v", router["backends"])
	}
}

func TestBareInstallKeepsOnlyADeployedFleet(t *testing.T) {
	var fresh cellnFleetFlags
	if fresh.keepDeployedFleet(false) || fresh.kept {
		t.Fatal("a fresh bare install must not deploy the Celln fleet (#693)")
	}
	var existing cellnFleetFlags
	if !existing.keepDeployedFleet(true) || !existing.kept {
		t.Fatal("a rerun on a cluster that runs the fleet must keep it")
	}
}

func TestDeployedFleetEnabledOnlyTreatsNotFoundAsAbsent(t *testing.T) {
	if got, err := deployedFleetEnabledFrom(func() (*releaseInfo, error) { return nil, nil }); err != nil || got {
		t.Fatalf("no release: got %v, %v; want false, nil", got, err)
	}
	fleet := &releaseInfo{Config: map[string]interface{}{"celln": map[string]interface{}{"fleet": map[string]interface{}{"enabled": true}}}}
	if got, err := deployedFleetEnabledFrom(func() (*releaseInfo, error) { return fleet, nil }); err != nil || !got {
		t.Fatalf("fleet release: got %v, %v; want true, nil", got, err)
	}
	// A transient Helm or RBAC read error must abort, never read as "no
	// fleet": that would render celln.enabled=false and remove a live fleet.
	readErr := errors.New("secrets is forbidden")
	got, err := deployedFleetEnabledFrom(func() (*releaseInfo, error) { return nil, readErr })
	if err == nil || !errors.Is(err, readErr) || got {
		t.Fatalf("read error: got %v, %v; want an error wrapping the read error", got, err)
	}
}

func TestDoctorPlainInstallPreviewsNoCelln(t *testing.T) {
	set, from, err := doctorDefaultValues(defaultFleetScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 1 || set[0] != "celln.enabled=false" || !strings.Contains(from, "--celln-fleet") {
		t.Fatalf("doctor preview of a plain install = %v (%q); want Celln off", set, from)
	}
}
