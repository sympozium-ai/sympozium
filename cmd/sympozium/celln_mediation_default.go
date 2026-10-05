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
	fleet     bool
	optOut    bool
	flagImage string
	setValues []string
	pinned    string
	// deployedImage is the deployed release's modelGateway.image.
	deployedImage string
	// clusterMediated reports that the cluster's release has mediation on
	// (the chart's ConfigMap celln-system/celln-mediated-routes exists).
	clusterMediated bool
}

// mediationPlan is the decision for one install.
type mediationPlan struct {
	// auto: the installer derives the values, bootstraps the trust, and
	// gives the installer's key to a starter Agent instead of the fleet.
	auto bool
	// operator: --set celln.mediation.enabled=true; the operator's values
	// are used as given and nothing is derived or bootstrapped.
	operator bool
	image    string
	notice   string
}

func (p mediationPlan) enabled() bool { return p.auto || p.operator }

const mediationOffConsequence = "the installer's provider key is published to every fleet node and any Agent in an admitted namespace runs on it"

// planMediation decides whether this install mediates model access.
func planMediation(in mediationInputs) (mediationPlan, error) {
	if !in.fleet {
		return mediationPlan{}, nil
	}
	vals, err := buildHelmValues("", in.setValues)
	if err != nil {
		return mediationPlan{}, err
	}
	setEnabled, explicit := nestedValue(vals, "celln", "mediation", "enabled").(bool)
	setImage, _ := nestedString(vals, "modelGateway", "image")
	switchedOff := ""
	if in.clusterMediated {
		switchedOff = " This switches off the mediated access the cluster had: runs of Agents with their own key are held (ScopedDispatchDisabled)."
	}
	switch {
	case in.optOut && explicit && setEnabled:
		return mediationPlan{}, fmt.Errorf("--no-celln-mediation contradicts --set celln.mediation.enabled=true")
	case in.optOut:
		return mediationPlan{notice: "Mediated model access: off (--no-celln-mediation); " + mediationOffConsequence + "." + switchedOff}, nil
	case explicit && !setEnabled:
		return mediationPlan{notice: "Mediated model access: off (--set celln.mediation.enabled=false); " + mediationOffConsequence + "." + switchedOff}, nil
	case explicit:
		return mediationPlan{operator: true, notice: "Mediated model access: on with the operator's celln.mediation values (--set); the installer bootstraps nothing."}, nil
	}
	image, source, err := selectModelGatewayImage(in.flagImage, setImage, in.pinned, in.deployedImage)
	if err != nil {
		return mediationPlan{}, err
	}
	if image == "" {
		if in.clusterMediated {
			return mediationPlan{}, fmt.Errorf("this cluster runs mediated model access, but this build pins no model gateway image and the release names none: pass --model-gateway-image %s@sha256:<digest>, or --no-celln-mediation to switch it off deliberately", defaultModelGatewayRepo)
		}
		return mediationPlan{notice: fmt.Sprintf("Mediated model access: off, because this build (a source build) pins no model gateway image; pass --model-gateway-image %s@sha256:<digest> to turn it on. Until then %s.", defaultModelGatewayRepo, mediationOffConsequence)}, nil
	}
	return mediationPlan{auto: true, image: image, notice: fmt.Sprintf("Mediated model access: on (gateway %s from %s); provider keys stay with their Agents, Celln nodes hold none. Opt out with --no-celln-mediation.", image, source)}, nil
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
