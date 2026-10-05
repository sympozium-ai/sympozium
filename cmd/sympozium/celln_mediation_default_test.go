package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
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
		{name: "no fleet, no mediation", in: mediationInputs{pinned: pinnedGateway}},
		{name: "default on with the release pin", in: mediationInputs{fleet: true, pinned: pinnedGateway}, auto: true, image: pinnedGateway, notice: "--no-celln-mediation"},
		{name: "opt-out", in: mediationInputs{fleet: true, optOut: true, pinned: pinnedGateway}, notice: "published to every fleet node"},
		{name: "opt-out of a mediated cluster says what it ends", in: mediationInputs{fleet: true, optOut: true, pinned: pinnedGateway, clusterMediated: true}, notice: "ScopedDispatchDisabled"},
		{name: "explicit false is an opt-out", in: mediationInputs{fleet: true, pinned: pinnedGateway, setValues: []string{"celln.mediation.enabled=false"}}, notice: "celln.mediation.enabled=false"},
		{name: "operator values", in: mediationInputs{fleet: true, pinned: pinnedGateway, setValues: []string{"celln.mediation.enabled=true", "celln.mediation.clusterId=c"}}, op: true, notice: "bootstraps nothing"},
		{name: "opt-out contradicts --set", in: mediationInputs{fleet: true, optOut: true, setValues: []string{"celln.mediation.enabled=true"}}, refused: "contradicts"},
		{name: "a source build falls back to off", in: mediationInputs{fleet: true}, notice: "--model-gateway-image"},
		{name: "a source build with the flag", in: mediationInputs{fleet: true, flagImage: flagGateway}, auto: true, image: flagGateway},
		{name: "a source build rerun keeps the deployed gateway", in: mediationInputs{fleet: true, deployedImage: deployedGateway, clusterMediated: true}, auto: true, image: deployedGateway},
		{name: "never off silently on a mediated cluster", in: mediationInputs{fleet: true, clusterMediated: true}, refused: "--no-celln-mediation to switch it off deliberately"},
		{name: "an unpinned flag is refused", in: mediationInputs{fleet: true, flagImage: "gw:latest"}, refused: "pinned by digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planMediation(tc.in)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("refusal = %v", err)
				}
				return
			}
			if err != nil || plan.auto != tc.auto || plan.operator != tc.op || plan.image != tc.image || !strings.Contains(plan.notice, tc.notice) {
				t.Fatalf("plan = %+v %v", plan, err)
			}
		})
	}
}

func TestMediationHostsFollowTheRelease(t *testing.T) {
	for _, tc := range []struct {
		name               string
		set                []string
		gateway, receivers []string
		namespace          string
	}{
		{name: "defaults", gateway: []string{"sympozium-model-gateway.sympozium-system.svc", "sympozium-model-gateway.sympozium-system.svc.cluster.local"}, receivers: []string{"celln-scoped-receiver.celln-system.svc", "celln-scoped-receiver.celln-system.svc.cluster.local"}, namespace: "sympozium-system"},
		{name: "full name override", set: []string{"fullnameOverride=sz"}, gateway: []string{"sz-model-gateway.sympozium-system.svc", "sz-model-gateway.sympozium-system.svc.cluster.local"}, namespace: "sympozium-system"},
		{name: "name override", set: []string{"nameOverride=agents", "namespace=platform"}, gateway: []string{"sympozium-agents-model-gateway.platform.svc", "sympozium-agents-model-gateway.platform.svc.cluster.local"}, namespace: "platform"},
		{name: "receiver URL", set: []string{"celln.mediation.receiver.url=https://receiver.example.com:9443"}, receivers: []string{"celln-scoped-receiver.celln-system.svc", "celln-scoped-receiver.celln-system.svc.cluster.local", "receiver.example.com"}, namespace: "sympozium-system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway, receiver, namespace, err := mediationHosts(tc.set)
			if err != nil || namespace != tc.namespace || (tc.gateway != nil && !slices.Equal(gateway, tc.gateway)) || (tc.receivers != nil && !slices.Equal(receiver, tc.receivers)) {
				t.Fatalf("got %v %v %q %v", gateway, receiver, namespace, err)
			}
		})
	}
	if _, _, _, err := mediationHosts([]string{"celln.mediation.receiver.url=http://plain"}); err == nil {
		t.Fatal("a plain-HTTP receiver URL was accepted")
	}
}

func TestRefreshModelGatewayImage(t *testing.T) {
	mediated := func(image string) map[string]interface{} {
		vals := map[string]interface{}{"celln": map[string]interface{}{"mediation": map[string]interface{}{"enabled": true}}}
		if image != "" {
			vals["modelGateway"] = map[string]interface{}{"image": image}
		}
		return vals
	}
	vals := mediated(deployedGateway)
	if notes := refreshModelGatewayImage(vals, pinnedGateway); len(notes) != 1 || nestedValue(vals, "modelGateway", "image") != pinnedGateway {
		t.Fatalf("the default gateway did not move to the pin: %v %v", notes, vals)
	}
	for name, tc := range map[string]struct {
		vals   map[string]interface{}
		pinned string
	}{
		"the operator's own repository": {mediated(flagGateway), pinnedGateway},
		"a build without a pin":         {mediated(deployedGateway), ""},
		"mediation off":                 {map[string]interface{}{"modelGateway": map[string]interface{}{"image": deployedGateway}}, pinnedGateway},
		"already the pin":               {mediated(pinnedGateway), pinnedGateway},
	} {
		before, _ := nestedString(tc.vals, "modelGateway", "image")
		if notes := refreshModelGatewayImage(tc.vals, tc.pinned); len(notes) != 0 {
			t.Fatalf("%s: %v", name, notes)
		}
		if after, _ := nestedString(tc.vals, "modelGateway", "image"); after != before {
			t.Fatalf("%s: image changed to %s", name, after)
		}
	}
}

func TestInstallRegistersMediationFlags(t *testing.T) {
	cmd := newInstallCmd()
	for _, flag := range []string{"no-celln-mediation", "model-gateway-image", "celln-starter-namespace"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Fatalf("install has no --%s", flag)
		}
	}
}

// The release pins the gateway digest it published into the CLI, under the
// variable this package reads, and the CLI is built only after it exists.
func TestReleasePinsTheModelGatewayDigest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs any `json:"needs"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	needs, _ := workflow.Jobs["release-cli"].Needs.([]any)
	if !slices.Contains(needs, any("release-model-gateway-digest")) || !slices.Contains(needs, any("release-starter-package")) {
		t.Fatalf("release-cli needs %v", workflow.Jobs["release-cli"].Needs)
	}
	if !strings.Contains(string(raw), "-X main.modelGatewayImage=${gateway}") {
		t.Fatal("the CLI build does not pin main.modelGatewayImage")
	}
}
