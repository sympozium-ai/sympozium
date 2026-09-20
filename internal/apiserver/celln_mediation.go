package apiserver

import (
	"net/http"
	"reflect"
	"slices"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

// Which providers an Agent in a namespace may bring its own key for. Read
// only, and nothing here is a credential: provider names, model names and
// public origins, exactly as the operator declared them. The routes offered
// are the ones the resolver will match (the namespace's live policies), not
// the declaration, so a client never offers a route a run would be refused.

// CellnMediatedRoute is one auth "secret" or "none" route. A ModelConnection matches it
// only with exactly this provider and protocol, one of these models and an
// endpoint on one of these origins.
type CellnMediatedRoute struct {
	Provider        string   `json:"provider"`
	Protocol        string   `json:"protocol"`
	Models          []string `json:"models"`
	EndpointOrigins []string `json:"endpointOrigins"`
	// Policy is the execution policy carrying the route; empty on a pending one.
	Policy string `json:"policy,omitempty"`
	// SecretKey is the fixed key the provider Secret must hold, empty for auth none.
	SecretKey     string `json:"secretKey"`
	Auth          string `json:"auth,omitempty"`
	AllowInsecure bool   `json:"allowInsecure,omitempty"`
}

func mediatedAPIRoute(route api.CellnExecutionPolicyRoute, policy string) CellnMediatedRoute {
	out := CellnMediatedRoute{Provider: route.Provider, Protocol: route.Protocol, Models: route.Models, EndpointOrigins: route.EndpointOrigins, Policy: policy, AllowInsecure: route.AllowInsecure}
	if route.Auth == "none" {
		out.Auth = "none"
	} else {
		out.SecretKey = connectionSecretKey(route.Protocol)
	}
	return out
}

// CellnMediation is the answer of GET /api/v1/celln-platform/mediation.
type CellnMediation struct {
	// Enabled reports that the release runs with celln.mediation.enabled: the
	// chart then renders the operator's declaration into celln-system.
	Enabled bool `json:"enabled"`
	// MediateBackends reports that the operator also offers every HTTPS fleet
	// backend's route to an Agent's own key.
	MediateBackends bool `json:"mediateBackends"`
	// Routes are the mediated routes the namespace's policies carry now.
	Routes []CellnMediatedRoute `json:"routes"`
	// Pending are routes the operator declared that no policy in the cluster
	// carries yet; `sympozium celln-mediation apply-routes` (or the
	// next install or added backend) publishes them.
	Pending []CellnMediatedRoute `json:"pending"`
}

func (s *Server) getCellnMediation(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	record, err := cellninstall.ReadMediationRecord(r.Context(), s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	authorised, err := cellnplatform.AuthorisedProfiles(r.Context(), s.client, ns)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := CellnMediation{Enabled: record.Enabled, MediateBackends: record.MediateBackends, Routes: []CellnMediatedRoute{}, Pending: []CellnMediatedRoute{}}
	seen := map[string]bool{}
	for _, a := range authorised {
		if seen[a.Policy.Name] {
			continue
		}
		seen[a.Policy.Name] = true
		for _, route := range a.Policy.Spec.Routes {
			if route.Auth == "secret" || route.Auth == "none" {
				out.Routes = append(out.Routes, mediatedAPIRoute(route, a.Policy.Name))
			}
		}
	}
	var policies api.CellnExecutionPolicyList
	if len(record.Routes) != 0 {
		if err := s.client.List(r.Context(), &policies); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	for _, declared := range record.Routes {
		route, err := declared.PolicyRoute()
		if err != nil {
			continue // ReadMediationRecord validated it
		}
		if !slices.ContainsFunc(policies.Items, func(policy api.CellnExecutionPolicy) bool {
			return slices.ContainsFunc(policy.Spec.Routes, func(have api.CellnExecutionPolicyRoute) bool { return reflect.DeepEqual(have, route) })
		}) {
			out.Pending = append(out.Pending, mediatedAPIRoute(route, ""))
		}
	}
	writeJSON(w, out)
}
