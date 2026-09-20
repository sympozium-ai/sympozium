package cellninstall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The operator's mediation intent, recorded once in the cluster. The chart
// renders it from celln.mediation.mediateBackends and celln.mediation.routes
// whenever celln.mediation.enabled is set, beside the fleet's other facts in
// celln-system. Every caller of InstallPlatform (the installer, the API
// server completing an added backend, `celln-mediation apply-routes`) reads
// it from there, so they cannot disagree about which providers an Agent may
// bring its own key for. It holds provider names, model names and public
// origins only, never a credential, and no tenant can write to celln-system.

const (
	// MediationRecordConfigMap is the record; its presence means mediation
	// is enabled on this cluster's release.
	MediationRecordConfigMap = "celln-mediated-routes"
	mediationRecordKey       = "mediation.json"
	// maxPolicyRoutes is the CRD's bound on one policy's routes.
	maxPolicyRoutes = 32
)

// MediationRecord is the operator's declaration as the cluster holds it.
type MediationRecord struct {
	// Enabled reports that the record exists, which the chart renders only
	// with celln.mediation.enabled.
	Enabled bool `json:"-"`
	// MediateBackends and Routes are PlatformOptions' fields of those names.
	MediateBackends bool            `json:"mediateBackends"`
	Routes          []MediatedRoute `json:"routes"`
}

// ReadMediationRecord reads the record. A cluster without one has mediation
// disabled and declares nothing. Every route is validated exactly as
// InstallPlatform validates it, so an unusable record is reported where it is
// read rather than half-applied.
func ReadMediationRecord(ctx context.Context, store client.Reader) (MediationRecord, error) {
	var cm corev1.ConfigMap
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: MediationRecordConfigMap}, &cm); apierrors.IsNotFound(err) {
		return MediationRecord{}, nil
	} else if err != nil {
		return MediationRecord{}, err
	}
	record := MediationRecord{Enabled: true}
	if raw := cm.Data[mediationRecordKey]; raw != "" {
		decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return MediationRecord{}, fmt.Errorf("ConfigMap %s/%s carries an unreadable %s: %w", fleetNamespace, MediationRecordConfigMap, mediationRecordKey, err)
		}
	}
	if err := ValidateMediatedRoutes(record.Routes); err != nil {
		return MediationRecord{}, fmt.Errorf("ConfigMap %s/%s: %w", fleetNamespace, MediationRecordConfigMap, err)
	}
	return record, nil
}

// ValidateMediatedRoutes checks every declared route with
// MediatedRoute.PolicyRoute and the policy's bound on routes.
func ValidateMediatedRoutes(routes []MediatedRoute) error {
	if len(routes) > maxPolicyRoutes {
		return fmt.Errorf("a scope's policy carries at most %d routes; %d mediated routes were declared", maxPolicyRoutes, len(routes))
	}
	for _, route := range routes {
		if _, err := route.PolicyRoute(); err != nil {
			return err
		}
	}
	return nil
}

// Apply sets the platform installation's mediation options from the record.
func (r MediationRecord) Apply(o *PlatformOptions) {
	o.MediateBackends = r.MediateBackends
	o.MediatedRoutes = append([]MediatedRoute(nil), r.Routes...)
}

// MediationValues are the chart values that make the chart render the record
// for this declaration; nothing when the operator declared nothing, so an
// install without the flags sets no mediation value at all.
func MediationValues(mediateBackends bool, routes []MediatedRoute) ([]string, error) {
	if err := ValidateMediatedRoutes(routes); err != nil {
		return nil, err
	}
	var values []string
	if mediateBackends {
		values = append(values, "celln.mediation.mediateBackends=true")
	}
	for i, route := range routes {
		prefix := fmt.Sprintf("celln.mediation.routes[%d].", i)
		values = append(values, prefix+"provider="+strvalsEscape(route.Provider), prefix+"protocol="+route.Protocol)
		if route.Auth != "" {
			values = append(values, prefix+"auth="+route.Auth)
		}
		if route.AllowInsecure {
			values = append(values, prefix+"allowInsecure=true")
		}
		for j, model := range route.Models {
			values = append(values, fmt.Sprintf("%smodels[%d]=%s", prefix, j, strvalsEscape(model)))
		}
		for j, origin := range route.EndpointOrigins {
			values = append(values, fmt.Sprintf("%sendpointOrigins[%d]=%s", prefix, j, strvalsEscape(origin)))
		}
	}
	return values, nil
}

// PlatformOptionsFromCluster are the options a caller that did not install
// the fleet passes to InstallPlatform: the scope's identities from the fleet
// facts, the namespace selection read back from the scope's policy, the
// namespace the installer wired, and the operator's recorded mediation intent.
func PlatformOptionsFromCluster(ctx context.Context, store client.Client, facts FleetFacts, configurationDir, outputDir, controllerNamespace string) (PlatformOptions, error) {
	clusterID, err := ClusterIdentity(ctx, store)
	if err != nil {
		return PlatformOptions{}, err
	}
	_, policyName, _ := PlatformCatalogueNames(facts.Scope)
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		return PlatformOptions{}, err
	}
	namespace, err := InstallNamespaceFor(ctx, store, facts.Scope)
	if err != nil {
		return PlatformOptions{}, err
	}
	if namespace == "" {
		namespace = "default"
	}
	record, err := ReadMediationRecord(ctx, store)
	if err != nil {
		return PlatformOptions{}, err
	}
	options := PlatformOptions{Namespace: namespace, ConfigurationDir: configurationDir, OutputDir: outputDir, Scope: facts.Scope, ClusterID: clusterID, PackageHash: facts.PackageHash, Principal: facts.Principal, ControllerNamespace: controllerNamespace, Authorise: AuthoriseModeOf(&policy)}
	record.Apply(&options)
	return options, nil
}

// ApplyMediationRecord brings an installed scope's policy up to the recorded
// declaration without reinstalling the fleet: it materializes the
// configuration the nodes published under workDir and runs the same
// idempotent InstallPlatform the installer runs, which only ever appends
// routes. It is how an operator who enabled mediation with Helm alone gets
// the declared routes into the policy.
func ApplyMediationRecord(ctx context.Context, store client.Client, workDir, controllerNamespace string) (MediationRecord, error) {
	if !filepath.IsAbs(workDir) || filepath.Clean(workDir) != workDir {
		return MediationRecord{}, fmt.Errorf("clean absolute working directory required")
	}
	facts, err := ReadFleetFacts(ctx, store)
	if err != nil {
		return MediationRecord{}, err
	}
	record, err := ReadMediationRecord(ctx, store)
	if err != nil {
		return MediationRecord{}, err
	}
	if !record.Enabled {
		return MediationRecord{}, fmt.Errorf("mediation is not enabled on this cluster (no ConfigMap %s/%s); enable celln.mediation in the release first", fleetNamespace, MediationRecordConfigMap)
	}
	configuration := filepath.Join(workDir, "configuration")
	if ok, err := ReadFleetConfigurationFor(ctx, store, configuration, facts.PackageHash, nil); err != nil {
		return MediationRecord{}, err
	} else if !ok {
		return MediationRecord{}, fmt.Errorf("the fleet has not published the configuration of package %s yet", facts.PackageHash)
	}
	options, err := PlatformOptionsFromCluster(ctx, store, facts, configuration, filepath.Join(workDir, "installation"), controllerNamespace)
	if err != nil {
		return MediationRecord{}, err
	}
	return record, InstallPlatform(ctx, store, options)
}
