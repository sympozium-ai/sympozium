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
		{"", "ghcr.io/sympozium-ai/celln", "v0.5.8", false},
		{"ghcr.io/sympozium-ai/celln:v0.5.8", "ghcr.io/sympozium-ai/celln", "v0.5.8", false},
		{"ghcr.io/sympozium-ai/celln@sha256:abcdef", "ghcr.io/sympozium-ai/celln", "sha256:abcdef", true},
		{"localhost:5000/celln", "localhost:5000/celln", "v0.5.8", false},
		{"localhost:5000/celln:v1", "localhost:5000/celln", "v1", false},
	}
	for _, c := range cases {
		repo, ref, digest := splitImageRef(c.in, "ghcr.io/sympozium-ai/celln", "v0.5.8")
		if repo != c.repo || ref != c.ref || digest != c.digest {
			t.Fatalf("splitImageRef(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, repo, ref, digest, c.repo, c.ref, c.digest)
		}
	}
}

func TestCellnInstallSetValues(t *testing.T) {
	vals, err := cellnInstallSetValues(
		context.Background(),
		"ghcr.io/sympozium-ai/celln:v0.5.8",
		"",
		[]string{"http://10.0.0.1:8787"},
		1,
		true,
	)
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
	for _, key := range []string{"enabled", "installer", "bootstrap", "dispatcher", "router", "tokenSecret", "capabilityTokenSecret"} {
		if _, ok := celln[key]; !ok {
			t.Fatalf("celln.%s missing from %#v", key, celln)
		}
	}
	bootstrap, _ := celln["bootstrap"].(map[string]interface{})
	if bootstrap["enabled"] != true {
		t.Fatalf("bootstrap.enabled = %v, want true", bootstrap["enabled"])
	}
	installer, _ := celln["installer"].(map[string]interface{})
	if installer["enabled"] != true {
		t.Fatalf("installer.enabled = %v, want true (hostInstaller=true)", installer["enabled"])
	}
	dispatcher, _ := celln["dispatcher"].(map[string]interface{})
	if dispatcher["enabled"] != false {
		t.Fatalf("dispatcher.enabled = %v, want false (hostInstaller=true replaces the in-cluster dispatcher)", dispatcher["enabled"])
	}
	router, _ := celln["router"].(map[string]interface{})
	if router["external"] != false {
		t.Fatalf("router.external = %v, want false", router["external"])
	}
	if router["image"].(map[string]interface{})["tag"] != "v0.5.8" {
		t.Fatalf("router.image.tag = %v, want v0.5.8", router["image"])
	}
	backends, ok := router["backends"].([]interface{})
	if !ok || len(backends) != 1 || backends[0] != "http://10.0.0.1:8787" {
		t.Fatalf("router.backends = %#v, want [http://10.0.0.1:8787]", router["backends"])
	}
}

func TestCellnInstallSetValuesDefaultBackends(t *testing.T) {
	vals, err := cellnInstallSetValues(
		context.Background(),
		"ghcr.io/sympozium-ai/celln:v0.5.8",
		"",
		nil,
		1,
		false,
	)
	if err != nil {
		t.Fatalf("cellnInstallSetValues: %v", err)
	}
	helmVals, err := buildHelmValues("", vals)
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}
	celln := helmVals["celln"].(map[string]interface{})
	router := celln["router"].(map[string]interface{})
	backends := router["backends"].([]interface{})
	if len(backends) != 1 || backends[0] != "http://celln-dispatcher.celln-system.svc.cluster.local:8787" {
		t.Fatalf("router.backends = %#v, want the in-cluster dispatcher Service", backends)
	}
	if _, has := celln["installer"]; has {
		installer := celln["installer"].(map[string]interface{})
		if installer["enabled"] == true {
			t.Fatalf("installer.enabled should be false when hostInstaller=false")
		}
	}
}

func TestCellnInstallSetValuesHostInstallerRequiresBackend(t *testing.T) {
	if _, err := cellnInstallSetValues(
		context.Background(),
		"ghcr.io/sympozium-ai/celln:v0.5.8",
		"",
		nil,
		1,
		true,
	); err == nil {
		t.Fatal("hostInstaller without --celln-backend must fail; the host dispatcher is loopback-only")
	}
}
