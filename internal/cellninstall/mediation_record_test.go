package cellninstall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"helm.sh/helm/v3/pkg/strvals"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func mediationRecord(content string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: fleetNamespace, Name: MediationRecordConfigMap}, Data: map[string]string{mediationRecordKey: content}}
}

func TestReadMediationRecord(t *testing.T) {
	anthropic := MediatedRoute{Provider: "anthropic", Protocol: "anthropic-messages", Models: []string{"claude-a"}, EndpointOrigins: []string{"https://api.anthropic.com"}}
	many := make([]MediatedRoute, 33)
	for i := range many {
		many[i] = anthropic
	}
	tooMany, _ := json.Marshal(MediationRecord{Routes: many})
	for _, tc := range []struct {
		name    string
		record  *corev1.ConfigMap
		want    MediationRecord
		refused string
	}{
		{name: "no record: mediation is disabled and nothing is declared"},
		{name: "enabled, nothing declared", record: mediationRecord(`{"mediateBackends":false,"routes":[]}`), want: MediationRecord{Enabled: true, Routes: []MediatedRoute{}}},
		{name: "empty record is enabled", record: mediationRecord(""), want: MediationRecord{Enabled: true}},
		{name: "declared", record: mediationRecord(`{"mediateBackends":true,"routes":[{"provider":"anthropic","protocol":"anthropic-messages","models":["claude-a"],"endpointOrigins":["https://api.anthropic.com"]}]}`), want: MediationRecord{Enabled: true, MediateBackends: true, Routes: []MediatedRoute{anthropic}}},
		{name: "unknown field", record: mediationRecord(`{"routes":[{"provider":"anthropic","protocol":"anthropic-messages","models":["claude-a"],"origins":["https://api.anthropic.com"]}]}`), refused: "unreadable"},
		{name: "plain HTTP origin", record: mediationRecord(`{"routes":[{"provider":"openai","protocol":"openai-chat","models":["gpt"],"endpointOrigins":["http://api.openai.com"]}]}`), refused: "plain HTTP"},
		{name: "wildcard model", record: mediationRecord(`{"routes":[{"provider":"openai","protocol":"openai-chat","models":["*"],"endpointOrigins":["https://api.openai.com"]}]}`), refused: "no wildcard"},
		{name: "more routes than a policy carries", record: mediationRecord(string(tooMany)), refused: "at most 32"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := extraStore(t)
			if tc.record != nil {
				store = extraStore(t, tc.record)
			}
			got, err := ReadMediationRecord(context.Background(), store)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("refusal = %v, want one mentioning %q", err, tc.refused)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("record = %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
}

// The installer hands the declaration to the chart as --set values; they must
// parse back into exactly the routes the operator declared.
func TestMediationValuesRoundTripThroughHelm(t *testing.T) {
	routes := []MediatedRoute{
		{Provider: "anthropic", Protocol: "anthropic-messages", Models: []string{"claude-sonnet-5", "claude.opus:5"}, EndpointOrigins: []string{"https://api.anthropic.com"}},
		{Provider: "open_ai", Protocol: "openai-chat", Models: []string{"org/gpt-5"}, EndpointOrigins: []string{"https://api.openai.com", "https://eu.api.openai.com"}},
	}
	values, err := MediationValues(true, routes)
	if err != nil {
		t.Fatal(err)
	}
	parsed := map[string]any{}
	for _, value := range values {
		if err := strvals.ParseInto(value, parsed); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	mediation := parsed["celln"].(map[string]any)["mediation"].(map[string]any)
	raw, _ := json.Marshal(mediation["routes"])
	var got []MediatedRoute
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, routes) || mediation["mediateBackends"] != true {
		t.Fatalf("values %v parse to %s (%v)", values, raw, err)
	}
	if _, enabled := mediation["enabled"]; enabled {
		t.Fatal("declaring routes must not switch mediation on")
	}
	if values, err := MediationValues(false, nil); err != nil || len(values) != 0 {
		t.Fatalf("nothing declared must set nothing: %v %v", values, err)
	}
	bad := []MediatedRoute{{Provider: "openai", Protocol: "openai-chat", Models: []string{"gpt"}, EndpointOrigins: []string{"https://api.openai.com:8443"}}}
	if _, err := MediationValues(false, bad); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("origin with a port accepted: %v", err)
	}
}

// publishConfiguration publishes a materialized configuration the way the
// fleet nodes do, with the configure DaemonSet's facts beside it.
func publishConfiguration(t *testing.T, dir, scope, packageHash, principal string) []*corev1.ConfigMap {
	t.Helper()
	data := map[string]string{}
	backends, err := ConfigurationBackends(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range backends {
		for _, file := range fleetConfigurationFiles {
			raw, err := os.ReadFile(filepath.Join(dir, backend, file))
			if err != nil {
				t.Fatal(err)
			}
			data[backend+"."+file] = string(raw)
		}
	}
	return []*corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Namespace: fleetNamespace, Name: FleetConfigurationConfigMap, Annotations: map[string]string{packageAnnotation: packageHash, FleetScopeAnnotation: scope}}, Data: data}}
}

func TestApplyMediationRecordExtendsAnInstalledScope(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	installed := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, installed); err != nil {
		t.Fatal(err)
	}
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: FleetConfigureDaemonSet, Namespace: fleetNamespace}}
	ds.Spec.Template.Spec.Containers = []corev1.Container{{Name: "configure", Env: []corev1.EnvVar{{Name: "FLEET_SCOPE", Value: "trial"}, {Name: "FLEET_PRINCIPAL", Value: principal}, {Name: "FLEET_PACKAGE_HASH", Value: packageHash}}}}
	if err := store.Create(ctx, ds); err != nil {
		t.Fatal(err)
	}
	for _, cm := range publishConfiguration(t, dir, "trial", packageHash, principal) {
		if err := store.Create(ctx, cm); err != nil {
			t.Fatal(err)
		}
	}
	secretRoutes := func() (out []api.CellnExecutionPolicyRoute) {
		t.Helper()
		_, policyName, _ := PlatformCatalogueNames("trial")
		var p api.CellnExecutionPolicy
		if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &p); err != nil {
			t.Fatal(err)
		}
		for _, r := range p.Spec.Routes {
			if r.Auth == "secret" {
				out = append(out, r)
			}
		}
		return out
	}

	// Mediation off: nothing to apply, and the policy stays as installed.
	if _, err := ApplyMediationRecord(ctx, store, t.TempDir(), "sympozium-system"); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("applied without a record: %v", err)
	}
	if got := secretRoutes(); len(got) != 0 {
		t.Fatalf("secret routes without a declaration: %+v", got)
	}

	// A record the chart would refuse changes nothing either.
	bad := mediationRecord(`{"routes":[{"provider":"openai","protocol":"openai-chat","models":["gpt"],"endpointOrigins":["http://api.openai.com"]}]}`)
	if err := store.Create(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyMediationRecord(ctx, store, t.TempDir(), "sympozium-system"); err == nil || !strings.Contains(err.Error(), "plain HTTP") {
		t.Fatalf("unusable record applied: %v", err)
	}
	if got := secretRoutes(); len(got) != 0 {
		t.Fatalf("a refused record wrote routes: %+v", got)
	}

	bad.Data[mediationRecordKey] = `{"mediateBackends":false,"routes":[{"provider":"openai","protocol":"openai-chat","models":["gpt-b","gpt-a"],"endpointOrigins":["https://api.openai.com"]}]}`
	if err := store.Update(ctx, bad); err != nil {
		t.Fatal(err)
	}
	record, err := ApplyMediationRecord(ctx, store, t.TempDir(), "sympozium-system")
	if err != nil || !record.Enabled || len(record.Routes) != 1 {
		t.Fatalf("apply: %+v %v", record, err)
	}
	want := []api.CellnExecutionPolicyRoute{{Provider: "openai", Protocol: "openai-chat", Models: []string{"gpt-a", "gpt-b"}, EndpointOrigins: []string{"https://api.openai.com"}, Auth: "secret"}}
	if got := secretRoutes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("secret routes = %+v", got)
	}

	// Every later caller that did not install the fleet (the API server adding
	// a backend) carries the same declaration, in the installer's namespace
	// and namespace-selection mode.
	facts, err := ReadFleetFacts(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	options, err := PlatformOptionsFromCluster(ctx, store, facts, dir, filepath.Join(t.TempDir(), "again"), "sympozium-system")
	if err != nil {
		t.Fatal(err)
	}
	if options.Namespace != "tenant-a" || options.Authorise != "all" || options.MediateBackends || !reflect.DeepEqual(options.MediatedRoutes, record.Routes) || options.ClusterID != "cluster-uid" {
		t.Fatalf("options from the cluster: %+v", options)
	}
	if err := InstallPlatform(ctx, store, options); err != nil {
		t.Fatal(err)
	}
	if got := secretRoutes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a later caller changed the secret routes: %+v", got)
	}

	// mediateBackends in the record offers the HTTPS backends as well.
	bad.Data[mediationRecordKey] = `{"mediateBackends":true,"routes":[]}`
	if err := store.Update(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyMediationRecord(ctx, store, t.TempDir(), "sympozium-system"); err != nil {
		t.Fatal(err)
	}
	if got := secretRoutes(); len(got) != 3 || got[0].Provider != "openai" {
		t.Fatalf("withdrawing a declared route must remove nothing, and backends must be added: %+v", got)
	}
}
