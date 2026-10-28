package main

import (
	"context"
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
