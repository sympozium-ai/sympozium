package main

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

// Mediated model access is the default for a fleet install: the model
// gateway holds each Agent's own provider key and Celln nodes never hold
// one. The installer bootstraps the trust itself and derives every
// celln.mediation value on each run, so a rerun never switches it off.
// --no-celln-mediation opts out; values given with --set
// celln.mediation.enabled=true keep the operator-managed path, in which the
// installer derives and bootstraps nothing.

// modelGatewayImage is the model gateway image this release pins, set by the
// release workflow with -ldflags "-X main.modelGatewayImage=repo@sha256:…"
// from the digest it published. A source build carries none.
var modelGatewayImage string

const defaultModelGatewayRepo = "ghcr.io/sympozium-ai/sympozium/model-gateway"

var gatewayImagePattern = regexp.MustCompile(`^[^@\s,=]+@sha256:[a-f0-9]{64}$`)

// pinnedModelGatewayImage is the build's pin, or "" when it carries none (or
// a malformed one, which is never used).
func pinnedModelGatewayImage() string {
	if gatewayImagePattern.MatchString(modelGatewayImage) {
		return modelGatewayImage
	}
	return ""
}

// selectModelGatewayImage picks the gateway image: --model-gateway-image,
// then --set modelGateway.image, then this build's pin, then the deployed
// release's (so a rerun from a build without a pin keeps the gateway it
// runs). Explicit inputs must be digest-pinned; the chart refuses tags.
func selectModelGatewayImage(flag, set, pinned, deployed string) (image, source string, err error) {
	for _, candidate := range []struct{ image, source string }{
		{flag, "--model-gateway-image"},
		{set, "--set modelGateway.image"},
		{pinned, "this release's pin"},
		{deployed, "the deployed release"},
	} {
		if candidate.image == "" {
			continue
		}
		if !gatewayImagePattern.MatchString(candidate.image) {
			return "", "", fmt.Errorf("%s %q must be pinned by digest (repository@sha256:<64 hex>)", candidate.source, candidate.image)
		}
		return candidate.image, candidate.source, nil
	}
	return "", "", nil
}

// mediationInputs is everything the mediation decision reads.
type mediationInputs struct {
	flagImage string
	setValues []string
	pinned    string
	// deployedImage is the deployed release's modelGateway.image.
	deployedImage string
	// clusterMediated reports that the cluster's release already runs the
	// gateway (the chart's record in celln-system exists).
	clusterMediated bool
}

// mediationPlan is the decision for one install.
type mediationPlan struct {
	// auto: the installer derives the values and bootstraps the trust.
	auto bool
	// operator: --set celln.mediation.enabled=true; the operator's values
	// are used as given and nothing is derived or bootstrapped, except the
	// gateway image, which the install still pins when they name none.
	operator bool
	image    string
	notice   string
}

// planMediation decides how this install wires the model gateway. The fleet
// serves model requests only through the gateway, so there is no "off": a
// build that pins no gateway image needs --model-gateway-image.
func planMediation(in mediationInputs) (mediationPlan, error) {
	vals, err := buildHelmValues("", in.setValues)
	if err != nil {
		return mediationPlan{}, err
	}
	setEnabled, explicit := nestedValue(vals, "celln", "mediation", "enabled").(bool)
	setImage, _ := nestedString(vals, "modelGateway", "image")
	switch {
	case explicit && !setEnabled:
		return mediationPlan{}, fmt.Errorf("--set celln.mediation.enabled=false: the Celln fleet serves model requests only through the model gateway, so it cannot be installed without one")
	}
	image, source, err := selectModelGatewayImage(in.flagImage, setImage, in.pinned, in.deployedImage)
	if err != nil {
		return mediationPlan{}, err
	}
	if image == "" {
		return mediationPlan{}, fmt.Errorf("this build (a source build) pins no model gateway image and the release names none: pass --model-gateway-image %s@sha256:<digest>; the fleet serves model requests only through the gateway", defaultModelGatewayRepo)
	}
	if explicit {
		// The operator's values wire the trust; the gateway image is still
		// this release's unless they name one. An install from a release
		// never leaves the chart without a digest-pinned image.
		return mediationPlan{operator: true, image: image, notice: fmt.Sprintf("Model gateway: wired with the operator's celln.mediation values (--set), image %s (from %s); the installer bootstraps nothing.", image, source)}, nil
	}
	return mediationPlan{auto: true, image: image, notice: fmt.Sprintf("Model gateway: %s (from %s); every Agent's own key is added there and never reaches a node.", image, source)}, nil
}

// chartFullname is the chart's sympozium.fullname for the CLI's release.
func chartFullname(vals map[string]interface{}) string {
	trim := func(s string) string {
		if len(s) > 63 {
			s = s[:63]
		}
		return strings.TrimSuffix(s, "-")
	}
	if override, _ := vals["fullnameOverride"].(string); override != "" {
		return trim(override)
	}
	name := "sympozium"
	if override, _ := vals["nameOverride"].(string); override != "" {
		name = override
	}
	if strings.Contains(helmReleaseName, name) {
		return trim(helmReleaseName)
	}
	return trim(helmReleaseName + "-" + name)
}

// mediationHosts are the names the gateway and receiver certificates must
// answer for in this release: the gateway Service of the release's actual
// full name and namespace, the celln-scoped-receiver Service (which fronts
// the router), and the host of celln.mediation.receiver.url when set.
func mediationHosts(setValues []string) (gateway, receiver []string, systemNamespace string, err error) {
	vals, err := buildHelmValues("", setValues)
	if err != nil {
		return nil, nil, "", err
	}
	systemNamespace = helmNamespace
	if ns, _ := vals["namespace"].(string); ns != "" {
		systemNamespace = ns
	}
	gateway, receiver = cellninstall.DefaultMediationHosts(chartFullname(vals), systemNamespace)
	if raw, _ := nestedString(vals, "celln", "mediation", "receiver", "url"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return nil, nil, "", fmt.Errorf("celln.mediation.receiver.url must be an HTTPS origin, got %q", raw)
		}
		if !slices.Contains(receiver, u.Hostname()) {
			receiver = append(receiver, u.Hostname())
		}
	}
	return gateway, receiver, systemNamespace, nil
}

// deployedModelGatewayImage is the deployed release's modelGateway.image, or
// "" when there is none (or it cannot be read).
func deployedModelGatewayImage() string {
	rel, err := helmReleaseInfo()
	if err != nil || rel == nil {
		return ""
	}
	image, _ := nestedString(rel.Config, "modelGateway", "image")
	return image
}
