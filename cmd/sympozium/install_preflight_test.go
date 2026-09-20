package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/helmchart"
)

func preflightClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme, sympoziumv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

// defaultInstallValues are the values of a plain `sympozium install`.
func defaultInstallValues(t *testing.T) map[string]interface{} {
	t.Helper()
	set, err := cellnInstallSetValues(context.Background(), "", "", nil, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	vals, err := buildHelmValues("", set)
	if err != nil {
		t.Fatal(err)
	}
	return vals
}

func owned() (map[string]string, map[string]string) {
	return map[string]string{helmManagedByLabel: "Helm"}, map[string]string{helmReleaseNameAnno: helmReleaseName, helmReleaseNamespaceAnn: helmNamespace}
}

// leftovers is the used machine of the epic: a namespace, a NetworkPolicy, a
// Secret and two workloads of an older install that no release owns.
func leftovers() []client.Object {
	labels, annotations := owned()
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "celln-system"}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-router-ingress", Namespace: "celln-system"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "celln-dispatcher-backend", Namespace: "celln-system"}, Data: map[string][]byte{"token": []byte("kept")}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "celln-router", Namespace: "celln-system"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "celln-dispatcher", Namespace: "celln-system"}},
		// Properly owned: never a collision.
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "celln-router", Namespace: "celln-system", Labels: labels, Annotations: annotations}},
		// Claimed by another release: reported, never adopted automatically.
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "celln-dispatcher", Namespace: "celln-system", Labels: labels,
			Annotations: map[string]string{helmReleaseNameAnno: "other", helmReleaseNamespaceAnn: "elsewhere"}}},
		// Not something the chart creates.
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "celln-system"}},
	}
}

func TestOwnershipCollisionsFindsEveryLeftoverInOnePass(t *testing.T) {
	ch, err := helmchart.Load()
	if err != nil {
		t.Fatal(err)
	}
	objects, err := renderReleaseObjects(ch, defaultInstallValues(t))
	if err != nil {
		t.Fatal(err)
	}
	collisions, err := ownershipCollisions(context.Background(), preflightClient(t, leftovers()...), objects)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]collision{}
	for _, c := range collisions {
		got[c.Object] = c
	}
	for _, tc := range []struct {
		object    string
		adoptable bool
		remedy    string
	}{
		{"Namespace celln-system", true, "kubectl label namespace celln-system app.kubernetes.io/managed-by=Helm --overwrite && kubectl annotate namespace celln-system meta.helm.sh/release-name=sympozium meta.helm.sh/release-namespace=sympozium-system --overwrite"},
		{"NetworkPolicy celln-system/celln-router-ingress", true, "kubectl label -n celln-system networkpolicy.networking.k8s.io celln-router-ingress app.kubernetes.io/managed-by=Helm"},
		{"Secret celln-system/celln-dispatcher-backend", true, "kubectl label -n celln-system secret celln-dispatcher-backend"},
		{"Deployment celln-system/celln-router", false, "kubectl delete -n celln-system deployment.apps celln-router"},
		{"Deployment celln-system/celln-dispatcher", false, "kubectl delete -n celln-system deployment.apps celln-dispatcher"},
		{"Service celln-system/celln-dispatcher", false, "kubectl label -n celln-system service celln-dispatcher"},
	} {
		t.Run(tc.object, func(t *testing.T) {
			c, ok := got[tc.object]
			if !ok {
				t.Fatalf("collision not reported; got %v", collisions)
			}
			if c.Adoptable != tc.adoptable || !strings.HasPrefix(c.Remedy, tc.remedy) || c.Problem == "" {
				t.Fatalf("got adoptable=%v remedy=%q problem=%q", c.Adoptable, c.Remedy, c.Problem)
			}
		})
	}
	if len(collisions) != 6 {
		t.Fatalf("want exactly the six leftovers, got %d: %v", len(collisions), collisions)
	}
}

func TestInstallPreflightStopsWithTheCompleteList(t *testing.T) {
	ch, err := helmchart.Load()
	if err != nil {
		t.Fatal(err)
	}
	c := preflightClient(t, leftovers()...)
	var out bytes.Buffer
	err = installPreflight(context.Background(), c, ch, defaultInstallValues(t), false, &out)
	if err == nil {
		t.Fatal("an install over unowned leftovers must stop before Helm runs")
	}
	for _, want := range []string{
		"6 object(s)", "nothing was changed", "Namespace celln-system", "NetworkPolicy celln-system/celln-router-ingress",
		"kubectl delete -n celln-system deployment.apps celln-router", "kubectl delete -n celln-system deployment.apps celln-dispatcher",
		"another Helm release claims it", "sympozium install --adopt-existing", "sympozium doctor",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal lacks %q:\n%v", want, err)
		}
	}
	var ns corev1.Namespace
	if err := c.Get(context.Background(), types.NamespacedName{Name: "celln-system"}, &ns); err != nil || len(ns.Labels) != 0 {
		t.Fatalf("the default must not touch the cluster: %v %v", err, ns.Labels)
	}
}

func TestInstallPreflightAdoptsOnlyWhatIsSafe(t *testing.T) {
	ch, err := helmchart.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := preflightClient(t, leftovers()...)
	var out bytes.Buffer
	err = installPreflight(ctx, c, ch, defaultInstallValues(t), true, &out)
	if err == nil {
		t.Fatal("workloads are never adopted, so the install must still stop")
	}
	for _, want := range []string{"3 object(s)", "deployment.apps celln-router", "deployment.apps celln-dispatcher", "Service celln-system/celln-dispatcher"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "Namespace celln-system") {
		t.Fatalf("an adopted object must leave the list:\n%v", err)
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: "celln-system"}, &ns); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: "celln-dispatcher-backend"}, &secret); err != nil {
		t.Fatal(err)
	}
	for _, meta := range []metav1.ObjectMeta{ns.ObjectMeta, secret.ObjectMeta} {
		if meta.Labels[helmManagedByLabel] != "Helm" || meta.Annotations[helmReleaseNameAnno] != helmReleaseName || meta.Annotations[helmReleaseNamespaceAnn] != helmNamespace {
			t.Fatalf("%s not adopted: %v %v", meta.Name, meta.Labels, meta.Annotations)
		}
	}
	if string(secret.Data["token"]) != "kept" {
		t.Fatal("adoption must keep a Secret's content")
	}
	var deployment appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: "celln-router"}, &deployment); err != nil || len(deployment.Labels) != 0 {
		t.Fatalf("a Deployment must never be adopted: %v %v", err, deployment.Labels)
	}
	var other corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: "celln-dispatcher"}, &other); err != nil || other.Annotations[helmReleaseNameAnno] != "other" {
		t.Fatalf("another release's object must be left alone: %v %v", err, other.Annotations)
	}
	if !strings.Contains(out.String(), "Adopted Namespace celln-system") {
		t.Fatalf("adoption is not reported: %s", out.String())
	}

	// Once the workloads are gone and the foreign Service handed over, a rerun passes.
	for _, obj := range []client.Object{&deployment, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "celln-dispatcher", Namespace: "celln-system"}}, &other} {
		if err := c.Delete(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	if err := installPreflight(ctx, c, ch, defaultInstallValues(t), true, &out); err != nil {
		t.Fatalf("clean cluster must pass: %v", err)
	}
}

func TestInstallPreflightPassesOnAnEmptyCluster(t *testing.T) {
	ch, err := helmchart.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := installPreflight(context.Background(), preflightClient(t), ch, defaultInstallValues(t), false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

// terminatingCRD is agentruns.sympozium.ai as the API server shows it while
// custom resources still exist.
func terminatingCRD(since time.Time) *unstructured.Unstructured {
	crd := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]interface{}{"name": "agentruns.sympozium.ai"},
		"spec": map[string]interface{}{
			"group": "sympozium.ai", "scope": "Namespaced",
			"names":    map[string]interface{}{"kind": "AgentRun", "plural": "agentruns"},
			"versions": []interface{}{map[string]interface{}{"name": "v1alpha1", "served": true, "storage": true}},
		},
	}}
	deleted := metav1.NewTime(since)
	crd.SetDeletionTimestamp(&deleted)
	crd.SetFinalizers([]string{"customresourcecleanup.apiextensions.k8s.io"})
	return crd
}

func TestTerminatingBlockers(t *testing.T) {
	since := time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)
	deleted := metav1.NewTime(since)
	held := func(name string) *sympoziumv1alpha1.AgentRun {
		return &sympoziumv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Finalizers: []string{"sympozium.ai/agentrun-finalizer"}}}
	}
	ch, err := helmchart.Load()
	if err != nil {
		t.Fatal(err)
	}
	crds, err := chartCRDs(ch)
	if err != nil || len(crds) == 0 {
		t.Fatalf("chart CRDs: %v %d", err, len(crds))
	}
	for _, tc := range []struct {
		name        string
		objects     []client.Object
		wantObject  string
		wantHolders []string
		wantReason  string
	}{
		{name: "nothing terminating", objects: []client.Object{held("free")}},
		{name: "CRD held by finalizers", objects: []client.Object{terminatingCRD(since), held("run-a"), held("run-b"),
			&sympoziumv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "no-finalizer", Namespace: "default"}}},
			wantObject: "CustomResourceDefinition agentruns.sympozium.ai", wantHolders: []string{"AgentRun default/run-a", "AgentRun default/run-b"}},
		{name: "CRD with no holder", objects: []client.Object{terminatingCRD(since)}, wantObject: "CustomResourceDefinition agentruns.sympozium.ai"},
		{name: "namespace", objects: []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "celln-system", DeletionTimestamp: &deleted, Finalizers: []string{"kubernetes"}},
				Status: corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating, Conditions: []corev1.NamespaceCondition{
					{Type: corev1.NamespaceContentRemaining, Status: corev1.ConditionTrue, Message: "Some resources are remaining: agentruns.sympozium.ai has 1 resource instances"},
					{Type: corev1.NamespaceDeletionDiscoveryFailure, Status: corev1.ConditionFalse, Message: "All resources successfully discovered"}}}},
			&sympoziumv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-c", Namespace: "celln-system", Finalizers: []string{"sympozium.ai/agentrun-finalizer"}}},
			held("elsewhere")},
			wantObject: "Namespace celln-system", wantHolders: []string{"AgentRun celln-system/run-c"}, wantReason: "agentruns.sympozium.ai has 1 resource instances"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stuck, err := terminatingBlockers(context.Background(), preflightClient(t, tc.objects...), crds, []string{"celln-system", helmNamespace})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantObject == "" {
				if len(stuck) != 0 {
					t.Fatalf("unexpected blockers: %+v", stuck)
				}
				return
			}
			if len(stuck) != 1 || stuck[0].Object != tc.wantObject || stuck[0].Since != "2026-09-15T08:00:00Z" {
				t.Fatalf("got %+v", stuck)
			}
			var holders []string
			for _, h := range stuck[0].Holders {
				holders = append(holders, h.Object)
				ns, name, _ := strings.Cut(strings.TrimPrefix(h.Object, "AgentRun "), "/")
				want := "kubectl patch -n " + ns + " agentruns.sympozium.ai " + name + ` --type=merge -p '{"metadata":{"finalizers":null}}'`
				if h.Remedy != want || h.Finalizers[0] != "sympozium.ai/agentrun-finalizer" {
					t.Fatalf("holder %+v, want remedy %q", h, want)
				}
			}
			if strings.Join(holders, ",") != strings.Join(tc.wantHolders, ",") {
				t.Fatalf("holders %v, want %v", holders, tc.wantHolders)
			}
			if tc.wantReason != "" && (len(stuck[0].Reasons) != 1 || !strings.Contains(stuck[0].Reasons[0], tc.wantReason)) {
				t.Fatalf("reasons %v", stuck[0].Reasons)
			}
		})
	}
}

func TestInstallPreflightStopsOnATerminatingCRD(t *testing.T) {
	ch, err := helmchart.Load()
	if err != nil {
		t.Fatal(err)
	}
	c := preflightClient(t, terminatingCRD(time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)),
		&sympoziumv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "old-run", Namespace: "default", Finalizers: []string{"sympozium.ai/agentrun-finalizer"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "celln-system"}})
	err = installPreflight(context.Background(), c, ch, defaultInstallValues(t), true, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a Terminating CRD must stop the install before CRDs are applied")
	}
	for _, want := range []string{"CustomResourceDefinition agentruns.sympozium.ai has been Terminating since 2026-09-15T08:00:00Z", "AgentRun default/old-run (sympozium.ai/agentrun-finalizer)",
		"kubectl patch -n default agentruns.sympozium.ai old-run", "skips the cleanup", "Namespace celln-system"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal lacks %q:\n%v", want, err)
		}
	}
	// Nothing is adopted while the install cannot proceed anyway.
	var ns corev1.Namespace
	if err := c.Get(context.Background(), types.NamespacedName{Name: "celln-system"}, &ns); err != nil || len(ns.Labels) != 0 {
		t.Fatalf("no adoption before the blockers are gone: %v %v", err, ns.Labels)
	}
}
