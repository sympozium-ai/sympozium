package main

import (
	"strings"
	"testing"
)

var (
	pinnedGateway   = defaultModelGatewayRepo + "@sha256:" + strings.Repeat("a", 64)
	flagGateway     = "registry.example.com/gw@sha256:" + strings.Repeat("b", 64)
	deployedGateway = defaultModelGatewayRepo + "@sha256:" + strings.Repeat("c", 64)
)

func TestSelectModelGatewayImage(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		flag, set, pinned, deployed string
		want, source, refused       string
	}{
		{name: "a source build with nothing", want: ""},
		{name: "the release pin", pinned: pinnedGateway, deployed: deployedGateway, want: pinnedGateway, source: "this release's pin"},
		{name: "the flag wins", flag: flagGateway, pinned: pinnedGateway, want: flagGateway, source: "--model-gateway-image"},
		{name: "--set before the pin", set: flagGateway, pinned: pinnedGateway, want: flagGateway, source: "--set modelGateway.image"},
		{name: "a rerun without a pin keeps the deployed gateway", deployed: deployedGateway, want: deployedGateway, source: "the deployed release"},
		{name: "a tag is refused", flag: defaultModelGatewayRepo + ":v1", refused: "pinned by digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image, source, err := selectModelGatewayImage(tc.flag, tc.set, tc.pinned, tc.deployed)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("refusal = %v", err)
				}
				return
			}
			if err != nil || image != tc.want || source != tc.source {
				t.Fatalf("got %q from %q (%v)", image, source, err)
			}
		})
	}
}

func TestPinnedModelGatewayImage(t *testing.T) {
	defer func(saved string) { modelGatewayImage = saved }(modelGatewayImage)
	for pin, want := range map[string]string{"": "", pinnedGateway: pinnedGateway, defaultModelGatewayRepo + ":v1": ""} {
		modelGatewayImage = pin
		if got := pinnedModelGatewayImage(); got != want {
			t.Fatalf("pin %q: got %q", pin, got)
		}
	}
}

// Mediation is on by default for a fleet whenever a gateway image is known,
// off only by choice or for a source build, and a rerun never turns it off
// silently.
func TestPlanMediation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      mediationInputs
		auto    bool
		op      bool
		image   string
		notice  string
		refused string
	}{
		{name: "the release pin", in: mediationInputs{pinned: pinnedGateway}, auto: true, image: pinnedGateway, notice: "never reaches a node"},
		{name: "explicit false is refused: the fleet has no other model path", in: mediationInputs{pinned: pinnedGateway, setValues: []string{"celln.mediation.enabled=false"}}, refused: "cannot be installed without one"},
		{name: "operator values keep the release's gateway pin", in: mediationInputs{pinned: pinnedGateway, setValues: []string{"celln.mediation.enabled=true", "celln.mediation.clusterId=c"}}, op: true, image: pinnedGateway, notice: "bootstraps nothing"},
		{name: "operator values with their own gateway image", in: mediationInputs{pinned: pinnedGateway, setValues: []string{"celln.mediation.enabled=true", "modelGateway.image=" + flagGateway}}, op: true, image: flagGateway, notice: "--set modelGateway.image"},
		{name: "operator values on a source build need an image too", in: mediationInputs{setValues: []string{"celln.mediation.enabled=true"}}, refused: "--model-gateway-image"},
		{name: "a source build needs the flag", in: mediationInputs{}, refused: "--model-gateway-image"},
		{name: "a source build with the flag", in: mediationInputs{flagImage: flagGateway}, auto: true, image: flagGateway},
		{name: "a source build rerun keeps the deployed gateway", in: mediationInputs{deployedImage: deployedGateway, clusterMediated: true}, auto: true, image: deployedGateway},
		{name: "an unpinned flag image is refused", in: mediationInputs{flagImage: "ghcr.io/sympozium-ai/sympozium/model-gateway:latest"}, refused: "pinned by digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planMediation(tc.in)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("refusal = %v, want one mentioning %q", err, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.auto != tc.auto || plan.operator != tc.op || plan.image != tc.image || !strings.Contains(plan.notice, tc.notice) {
				t.Fatalf("plan = %+v", plan)
			}
		})
	}
}
