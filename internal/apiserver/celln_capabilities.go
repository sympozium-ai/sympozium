package apiserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const cellnCapabilityVersion = "celln.dev/capabilities-v1alpha1"

// Capability state codes distinguish installation, transport, reachability and
// readiness. Transport/config failure is never reported as "not installed".
const (
	capabilityStateDisabled          = "disabled"
	capabilityStateNotInstalled      = "not_installed"
	capabilityStateUnreachable       = "unreachable"
	capabilityStateTransportInvalid  = "transport_invalid"
	capabilityStateCredentialInvalid = "credential_invalid"
	capabilityStateIncompatible      = "incompatible"
	capabilityStateNoCapacity        = "no_capacity"
	capabilityStateReady             = "ready"
	capabilityStateUnknown           = "unknown"
	capabilityStateNotApproved       = "not_approved"
)

// Discovery credentials have no execution authority. Never fall back to the
// controller token or public health/TCP if authenticated discovery fails.
func cellnCapabilityStatus() CapabilityStatus {
	oneShot := cellnOneShotCapabilityStatus()
	enduring := cellnEnduringCapabilityStatus()
	status := CapabilityStatus{
		Available: oneShot.Available || enduring.Available,
		Reason:    combineCellnReasons(oneShot, enduring),
		State:     preferCellnState(oneShot, enduring),
		OneShot:   &oneShot,
		Enduring:  &enduring,
	}
	return status
}

func cellnOneShotCapabilityStatus() CapabilityStatus {
	refuse := func(state, reason string) CapabilityStatus {
		return CapabilityStatus{Available: false, State: state, Reason: reason}
	}
	if os.Getenv("CELLN_ENABLED") != "true" {
		return refuse(capabilityStateDisabled, "Celln one-shot router is disabled (CELLN_ENABLED is not true)")
	}
	origin := os.Getenv("CELLN_ROUTER_URL")
	if origin == "" {
		origin = defaultCellnRouterURL
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return refuse(capabilityStateTransportInvalid, "Celln one-shot capability URL is invalid")
	}
	if u.Scheme == "http" && os.Getenv("CELLN_ALLOW_INSECURE_HTTP") != "true" {
		return refuse(capabilityStateTransportInvalid, "Celln requires HTTPS or explicit plaintext acknowledgement (set CELLN_ALLOW_INSECURE_HTTP=true only for operator-approved plaintext lab endpoints)")
	}
	tokenFile := os.Getenv("CELLN_CAPABILITY_TOKEN_FILE")
	if tokenFile == "" {
		return refuse(capabilityStateCredentialInvalid, "Celln read-only credential unavailable")
	}
	file, err := os.Open(tokenFile)
	if err != nil {
		if os.IsNotExist(err) {
			return refuse(capabilityStateCredentialInvalid, "Celln read-only credential unavailable")
		}
		return refuse(capabilityStateCredentialInvalid, "Celln read-only credential unavailable")
	}
	bytes, err := io.ReadAll(io.LimitReader(file, 4097))
	file.Close()
	token := strings.TrimSpace(string(bytes))
	if err != nil || len(bytes) > 4096 || len(token) < 24 {
		return refuse(capabilityStateCredentialInvalid, "Celln read-only credential invalid")
	}
	for _, b := range []byte(token) {
		if b < 33 || b > 126 {
			return refuse(capabilityStateCredentialInvalid, "Celln read-only credential invalid")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/capabilities"
	u.RawPath = ""
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return refuse(capabilityStateTransportInvalid, "Celln capability URL is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return refuse(capabilityStateUnreachable, "Celln authenticated capability probe failed (router unreachable or TLS/configuration error)")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return refuse(capabilityStateNotInstalled, "Celln one-shot capabilities endpoint was not found on the configured router")
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return refuse(capabilityStateNotApproved, "Celln authenticated capability probe refused (credential not approved for discovery)")
	}
	if response.StatusCode != http.StatusOK {
		return refuse(capabilityStateUnreachable, "Celln authenticated capability probe refused or unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil || len(body) > 2*1024*1024 {
		return refuse(capabilityStateIncompatible, "Celln capability response invalid or oversized")
	}
	var report cellnRouterCapabilities
	if json.Unmarshal(body, &report) != nil || report.APIVersion != cellnCapabilityVersion || !report.PreflightOnly || report.ArtifactReadiness != "not_checked" || len(report.Nodes) > 32 {
		return refuse(capabilityStateIncompatible, "Celln capability contract is incompatible")
	}
	eligible := 0
	for _, node := range report.Nodes {
		if !node.PreflightEligible {
			continue
		}
		if node.Report == nil || !node.Report.eligible() {
			return refuse(capabilityStateIncompatible, "Celln capability eligibility is inconsistent")
		}
		eligible++
	}
	if eligible != report.EligibleNodes {
		return refuse(capabilityStateIncompatible, "Celln capability eligibility is inconsistent")
	}
	if eligible == 0 {
		return refuse(capabilityStateNoCapacity, "Celln has no authenticated compatible node with available KVM capacity")
	}
	return CapabilityStatus{
		Available: true,
		State:     capabilityStateReady,
		Reason:    "One-shot node preflight eligible only; selected Harness, approved tools, model grants and warm artifacts still require validation",
	}
}

// Native enduring readiness is owned by the parent controller path and is not
// inferred from the one-shot router probe. The API reports configuration signals
// available to this process without claiming host admission.
func cellnEnduringCapabilityStatus() CapabilityStatus {
	if os.Getenv("CELLN_ENABLED") != "true" {
		return CapabilityStatus{
			Available: false,
			State:     capabilityStateDisabled,
			Reason:    "Celln is disabled; native enduring is not advertised",
		}
	}
	// Parent controller config is mounted on the parent-only controller, not
	// necessarily on this API process. Do not claim absence from a missing local file.
	return CapabilityStatus{
		Available: false,
		State:     capabilityStateUnknown,
		Reason:    "Native enduring readiness is distinct from one-shot router preflight; parent controller registration and launch approval are checked at run admission",
	}
}

func combineCellnReasons(oneShot, enduring CapabilityStatus) string {
	parts := make([]string, 0, 2)
	if oneShot.Reason != "" {
		parts = append(parts, "one-shot: "+oneShot.Reason)
	}
	if enduring.Reason != "" {
		parts = append(parts, "enduring: "+enduring.Reason)
	}
	return strings.Join(parts, " · ")
}

func preferCellnState(oneShot, enduring CapabilityStatus) string {
	if oneShot.Available {
		return oneShot.State
	}
	if enduring.Available {
		return enduring.State
	}
	// Prefer precise transport/install states over a generic unavailable label.
	for _, state := range []string{
		capabilityStateTransportInvalid,
		capabilityStateNotInstalled,
		capabilityStateUnreachable,
		capabilityStateCredentialInvalid,
		capabilityStateNotApproved,
		capabilityStateIncompatible,
		capabilityStateNoCapacity,
		capabilityStateDisabled,
		capabilityStateUnknown,
	} {
		if oneShot.State == state {
			return state
		}
	}
	if enduring.State != "" {
		return enduring.State
	}
	return capabilityStateUnknown
}

type cellnRouterCapabilities struct {
	APIVersion        string `json:"apiVersion"`
	PreflightOnly     bool   `json:"preflightOnly"`
	ArtifactReadiness string `json:"artifactReadiness"`
	EligibleNodes     int    `json:"eligibleNodes"`
	Nodes             []struct {
		PreflightEligible bool                         `json:"preflightEligible"`
		Report            *cellnDispatcherCapabilities `json:"report"`
	} `json:"nodes"`
}

type cellnDispatcherCapabilities struct {
	APIVersion        string   `json:"apiVersion"`
	PreflightOnly     bool     `json:"preflightOnly"`
	ArtifactReadiness string   `json:"artifactReadiness"`
	RequestVersions   []string `json:"requestVersions"`
	Node              struct {
		KVM    bool    `json:"kvm"`
		CPU    bool    `json:"cpu_virtualization"`
		Kernel bool    `json:"guest_kernel"`
		Motes  bool    `json:"mote_store"`
		Tools  bool    `json:"tool_store"`
		Live   *uint32 `json:"live_cells"`
		Max    *uint32 `json:"max_cells"`
		Memory *uint64 `json:"memory_bytes"`
	} `json:"node"`
}

func (r *cellnDispatcherCapabilities) eligible() bool {
	supported := false
	for _, version := range r.RequestVersions {
		supported = supported || version == "celln.dev/v1alpha1"
	}
	n := r.Node
	return r.APIVersion == cellnCapabilityVersion && r.PreflightOnly && r.ArtifactReadiness == "not_checked" && supported && n.KVM && n.CPU && n.Kernel && n.Motes && n.Tools && n.Live != nil && n.Max != nil && n.Memory != nil && *n.Live < *n.Max && *n.Memory > 0
}
