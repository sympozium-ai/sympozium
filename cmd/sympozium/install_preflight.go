package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/releaseutil"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/sympozium-ai/sympozium/internal/helmchart"
)

// What Helm checks before it takes over an object that already exists
// ("invalid ownership metadata"). An install on a used machine fails on the
// first object lacking them; the preflight finds every such object at once.
const (
	helmManagedByLabel      = "app.kubernetes.io/managed-by"
	helmReleaseNameAnno     = "meta.helm.sh/release-name"
	helmReleaseNamespaceAnn = "meta.helm.sh/release-namespace"
)

// installAdoptExisting is `sympozium install --adopt-existing`.
var installAdoptExisting bool

// adoptableKinds may be handed to the release as they are: the chart keeps
// their content (a Secret's token, a claim's data) or replaces a spec that
// cannot be incompatible. Workloads are never adopted: an old Deployment or
// DaemonSet may carry an immutable selector or a spec the chart cannot patch.
var adoptableKinds = map[string]bool{
	"Namespace": true, "ServiceAccount": true, "ConfigMap": true, "Secret": true,
	"Service": true, "NetworkPolicy": true, "PersistentVolumeClaim": true,
}

// clusterScopedKinds are the cluster-scoped kinds a chart may render; remedy
// commands for them carry no namespace.
var clusterScopedKinds = map[string]bool{
	"Namespace": true, "ClusterRole": true, "ClusterRoleBinding": true, "CustomResourceDefinition": true,
	"ValidatingWebhookConfiguration": true, "MutatingWebhookConfiguration": true, "PriorityClass": true,
	"StorageClass": true, "RuntimeClass": true, "GatewayClass": true, "ClusterIssuer": true, "DeviceClass": true,
	"ValidatingAdmissionPolicy": true, "ValidatingAdmissionPolicyBinding": true,
}

// adoptable reports whether an existing object may be handed to the release.
// The chart's own custom resources (default policies, skill packs, ensembles)
// qualify too: deleting an Ensemble would take its agents with it, and the
// chart replaces the spec under the CRDs this install has just applied.
func adoptable(obj *unstructured.Unstructured) bool {
	return adoptableKinds[obj.GetKind()] || obj.GroupVersionKind().Group == "sympozium.ai"
}

// renderReleaseObjects renders the chart exactly as the install would, without
// a cluster, and returns the objects the release would own. CRDs are applied
// separately by the installer and are not part of the release.
func renderReleaseObjects(ch *chart.Chart, vals map[string]interface{}) ([]*unstructured.Unstructured, error) {
	cfg := &action.Configuration{
		Releases:     storage.Init(driver.NewMemory()),
		KubeClient:   &kubefake.PrintingKubeClient{Out: io.Discard},
		Capabilities: chartutil.DefaultCapabilities,
		Log:          func(string, ...interface{}) {},
	}
	install := action.NewInstall(cfg)
	install.DryRun, install.ClientOnly, install.Replace = true, true, true
	install.ReleaseName, install.Namespace = helmReleaseName, helmNamespace
	rel, err := install.Run(ch, vals)
	if err != nil {
		return nil, fmt.Errorf("rendering the chart: %w", err)
	}
	var objects []*unstructured.Unstructured
	for _, doc := range releaseutil.SplitManifests(rel.Manifest) {
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(doc), &obj.Object); err != nil {
			return nil, fmt.Errorf("parsing a rendered object: %w", err)
		}
		if obj.GetKind() == "" || obj.GetName() == "" {
			continue
		}
		if clusterScopedKinds[obj.GetKind()] {
			obj.SetNamespace("")
		} else if obj.GetNamespace() == "" {
			obj.SetNamespace(helmNamespace)
		}
		objects = append(objects, obj)
	}
	sort.SliceStable(objects, func(i, j int) bool { return objectRef(objects[i]) < objectRef(objects[j]) })
	return objects, nil
}

// kubectlResource names an object's type the way kubectl resolves it
// unambiguously: kind, or kind.group outside the core group.
func kubectlResource(obj *unstructured.Unstructured) string {
	kind := strings.ToLower(obj.GetKind())
	if group := obj.GroupVersionKind().Group; group != "" {
		return kind + "." + group
	}
	return kind
}

func objectRef(obj *unstructured.Unstructured) string {
	if obj.GetNamespace() == "" {
		return obj.GetKind() + " " + obj.GetName()
	}
	return obj.GetKind() + " " + obj.GetNamespace() + "/" + obj.GetName()
}

func kubectlTarget(obj *unstructured.Unstructured) string {
	target := kubectlResource(obj) + " " + obj.GetName()
	if obj.GetNamespace() != "" {
		target = "-n " + obj.GetNamespace() + " " + target
	}
	return target
}

// collision is an object the chart would create that exists without this
// release's ownership metadata.
type collision struct {
	Object    string `json:"object"`
	Problem   string `json:"problem"`
	Adoptable bool   `json:"adoptable"`
	Remedy    string `json:"remedy"`
	existing  *unstructured.Unstructured
}

// ownershipCollisions checks every rendered object in one pass. An object
// that does not exist, or whose kind the cluster does not serve yet, is no
// collision; any other read failure is returned, never guessed at.
func ownershipCollisions(ctx context.Context, reader client.Reader, objects []*unstructured.Unstructured) ([]collision, error) {
	var out []collision
	for _, want := range objects {
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(want.GroupVersionKind())
		err := reader.Get(ctx, client.ObjectKey{Namespace: want.GetNamespace(), Name: want.GetName()}, existing)
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", objectRef(want), err)
		}
		problem := ownershipProblem(existing)
		if problem == "" {
			continue
		}
		c := collision{Object: objectRef(want), Problem: problem, Adoptable: adoptable(want), existing: existing}
		// An object another Helm release claims is never taken from it
		// automatically; the operator decides with the commands below.
		otherRelease := existing.GetAnnotations()[helmReleaseNameAnno] != ""
		if otherRelease {
			c.Adoptable = false
		}
		if adoptable(want) {
			c.Remedy = fmt.Sprintf("kubectl label %s %s=Helm --overwrite && kubectl annotate %s %s=%s %s=%s --overwrite",
				kubectlTarget(want), helmManagedByLabel, kubectlTarget(want), helmReleaseNameAnno, helmReleaseName, helmReleaseNamespaceAnn, helmNamespace)
		} else {
			c.Remedy = "kubectl delete " + kubectlTarget(want)
		}
		out = append(out, c)
	}
	return out, nil
}

// ownershipProblem mirrors Helm's ownership check; empty means the release
// may take the object.
func ownershipProblem(obj *unstructured.Unstructured) string {
	var missing []string
	if got := obj.GetLabels()[helmManagedByLabel]; got != "Helm" {
		missing = append(missing, fmt.Sprintf("label %s is %q, not \"Helm\"", helmManagedByLabel, got))
	}
	for key, want := range map[string]string{helmReleaseNameAnno: helmReleaseName, helmReleaseNamespaceAnn: helmNamespace} {
		if got := obj.GetAnnotations()[key]; got != want {
			missing = append(missing, fmt.Sprintf("annotation %s is %q, not %q", key, got, want))
		}
	}
	if len(missing) == 3 && obj.GetLabels()[helmManagedByLabel] == "" && obj.GetAnnotations()[helmReleaseNameAnno] == "" {
		return "carries no Helm ownership metadata"
	}
	sort.Strings(missing)
	return strings.Join(missing, "; ")
}

// adoptCollisions hands every adoptable object to the release by writing the
// ownership metadata, and returns the collisions that remain.
func adoptCollisions(ctx context.Context, c client.Client, collisions []collision, out io.Writer) ([]collision, error) {
	var remaining []collision
	for _, col := range collisions {
		if !col.Adoptable {
			remaining = append(remaining, col)
			continue
		}
		obj := col.existing
		patch := client.MergeFrom(obj.DeepCopy())
		labels, annotations := obj.GetLabels(), obj.GetAnnotations()
		if labels == nil {
			labels = map[string]string{}
		}
		if annotations == nil {
			annotations = map[string]string{}
		}
		labels[helmManagedByLabel] = "Helm"
		annotations[helmReleaseNameAnno], annotations[helmReleaseNamespaceAnn] = helmReleaseName, helmNamespace
		obj.SetLabels(labels)
		obj.SetAnnotations(annotations)
		if err := c.Patch(ctx, obj, patch); err != nil {
			return nil, fmt.Errorf("adopting %s: %w", col.Object, err)
		}
		fmt.Fprintf(out, "  Adopted %s into release %s.\n", col.Object, helmReleaseName)
	}
	return remaining, nil
}

// finalizerHolder is a custom resource that keeps a Terminating CRD or
// namespace alive because nothing is left to run its finalizer.
type finalizerHolder struct {
	Object     string   `json:"object"`
	Finalizers []string `json:"finalizers"`
	Remedy     string   `json:"remedy"`
}

// terminating is a CRD or namespace stuck in deletion.
type terminating struct {
	Object  string            `json:"object"`
	Since   string            `json:"since"`
	Reasons []string          `json:"reasons,omitempty"`
	Holders []finalizerHolder `json:"holders,omitempty"`
}

const clearFinalizersWarning = "Clearing a finalizer skips the cleanup its controller would have done (an AgentRun's pods, Jobs and per-run RBAC are left behind); delete such leftovers by hand. Do it only for objects of an install that is gone."

var crdGVK = schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}

// chartCRDs parses the chart's CRDs.
func chartCRDs(ch *chart.Chart) ([]*unstructured.Unstructured, error) {
	var crds []*unstructured.Unstructured
	for _, c := range ch.CRDObjects() {
		for _, doc := range releaseutil.SplitManifests(string(c.File.Data)) {
			obj := &unstructured.Unstructured{}
			if err := yaml.Unmarshal([]byte(doc), &obj.Object); err != nil {
				return nil, fmt.Errorf("parsing CRD %s: %w", c.Name, err)
			}
			if obj.GetKind() == crdGVK.Kind {
				crds = append(crds, obj)
			}
		}
	}
	return crds, nil
}

// crdResources lists every object of a CRD (in one namespace, or all when
// namespace is empty) that still carries finalizers.
func crdResources(ctx context.Context, reader client.Reader, crd *unstructured.Unstructured, namespace string) ([]finalizerHolder, error) {
	group, _, _ := unstructured.NestedString(crd.Object, "spec", "group")
	kind, _, _ := unstructured.NestedString(crd.Object, "spec", "names", "kind")
	plural, _, _ := unstructured.NestedString(crd.Object, "spec", "names", "plural")
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	version := ""
	for _, v := range versions {
		if m, ok := v.(map[string]interface{}); ok && (version == "" || m["storage"] == true) {
			version, _ = m["name"].(string)
		}
	}
	if group == "" || kind == "" || version == "" {
		return nil, nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: version, Kind: kind + "List"})
	if err := reader.List(ctx, list, client.InNamespace(namespace)); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing %s.%s: %w", plural, group, err)
	}
	var holders []finalizerHolder
	for i := range list.Items {
		item := &list.Items[i]
		if len(item.GetFinalizers()) == 0 {
			continue
		}
		target := plural + "." + group + " " + item.GetName()
		if item.GetNamespace() != "" {
			target = "-n " + item.GetNamespace() + " " + target
		}
		holders = append(holders, finalizerHolder{
			Object:     objectRef(item),
			Finalizers: item.GetFinalizers(),
			Remedy:     "kubectl patch " + target + ` --type=merge -p '{"metadata":{"finalizers":null}}'`,
		})
	}
	return holders, nil
}

// terminatingBlockers reports the chart's CRDs and the given namespaces that
// are being deleted, with what holds each of them.
func terminatingBlockers(ctx context.Context, reader client.Reader, crds []*unstructured.Unstructured, namespaces []string) ([]terminating, error) {
	var out []terminating
	for _, crd := range crds {
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(crdGVK)
		err := reader.Get(ctx, client.ObjectKey{Name: crd.GetName()}, live)
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading CRD %s: %w", crd.GetName(), err)
		}
		if live.GetDeletionTimestamp() == nil {
			continue
		}
		holders, err := crdResources(ctx, reader, live, "")
		if err != nil {
			return nil, err
		}
		out = append(out, terminating{Object: "CustomResourceDefinition " + crd.GetName(), Since: live.GetDeletionTimestamp().UTC().Format("2006-01-02T15:04:05Z"), Holders: holders})
	}
	for _, name := range namespaces {
		ns := &unstructured.Unstructured{}
		ns.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"})
		err := reader.Get(ctx, client.ObjectKey{Name: name}, ns)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading namespace %s: %w", name, err)
		}
		if ns.GetDeletionTimestamp() == nil {
			continue
		}
		t := terminating{Object: "Namespace " + name, Since: ns.GetDeletionTimestamp().UTC().Format("2006-01-02T15:04:05Z")}
		// The namespace controller says what is left in its conditions.
		conditions, _, _ := unstructured.NestedSlice(ns.Object, "status", "conditions")
		for _, c := range conditions {
			if m, ok := c.(map[string]interface{}); ok && m["status"] == "True" {
				if message, _ := m["message"].(string); message != "" {
					t.Reasons = append(t.Reasons, message)
				}
			}
		}
		for _, crd := range crds {
			holders, err := crdResources(ctx, reader, crd, name)
			if err != nil {
				return nil, err
			}
			t.Holders = append(t.Holders, holders...)
		}
		out = append(out, t)
	}
	return out, nil
}

// releaseNamespaces are the namespaces the rendered release touches.
func releaseNamespaces(objects []*unstructured.Unstructured) []string {
	seen := map[string]bool{helmNamespace: true}
	for _, obj := range objects {
		if obj.GetKind() == "Namespace" {
			seen[obj.GetName()] = true
		} else if obj.GetNamespace() != "" {
			seen[obj.GetNamespace()] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// installPreflight runs before anything in the cluster changes: it stops the
// install with every Terminating CRD or namespace and every ownership
// collision at once, each with its remedy. With adopt, the objects that are
// safe to hand to the release are adopted first.
func installPreflight(ctx context.Context, c client.Client, ch *chart.Chart, vals map[string]interface{}, adopt bool, out io.Writer) error {
	objects, err := renderReleaseObjects(ch, vals)
	if err != nil {
		return err
	}
	crds, err := chartCRDs(ch)
	if err != nil {
		return err
	}
	stuck, err := terminatingBlockers(ctx, c, crds, releaseNamespaces(objects))
	if err != nil {
		return err
	}
	var report strings.Builder
	if len(stuck) != 0 {
		report.WriteString(describeTerminating(stuck))
	}
	collisions, err := ownershipCollisions(ctx, c, objects)
	if err != nil {
		return err
	}
	// Nothing is adopted while the install cannot go ahead anyway.
	adopt = adopt && len(stuck) == 0
	if adopt {
		if collisions, err = adoptCollisions(ctx, c, collisions, out); err != nil {
			return err
		}
	}
	if len(collisions) != 0 {
		report.WriteString(describeCollisions(collisions, adopt))
	}
	if report.Len() == 0 {
		return nil
	}
	return fmt.Errorf("this cluster carries leftovers of an earlier install that would fail the install part-way; nothing was changed.\n%s\n  Rerun 'sympozium install' afterwards; 'sympozium doctor' shows the same list without installing", strings.TrimRight(report.String(), "\n"))
}

// runInstallPreflight renders what this install would create, from a fresh
// copy of the chart and values so the real install sees neither touched.
func runInstallPreflight(imageTag string, setValues []string) error {
	if k8sClient == nil {
		if err := initClient(); err != nil {
			return err
		}
	}
	ch, err := helmchart.Load()
	if err != nil {
		return fmt.Errorf("loading embedded chart: %w", err)
	}
	vals, err := buildHelmValues(imageTag, setValues)
	if err != nil {
		return err
	}
	fmt.Println("  Checking for leftovers of earlier installs...")
	return installPreflight(context.Background(), k8sClient, ch, vals, installAdoptExisting, os.Stdout)
}

func describeTerminating(stuck []terminating) string {
	var b strings.Builder
	for _, t := range stuck {
		fmt.Fprintf(&b, "\n  %s has been Terminating since %s and cannot be reused until it is gone.\n", t.Object, t.Since)
		for _, reason := range t.Reasons {
			fmt.Fprintf(&b, "    %s\n", reason)
		}
		if len(t.Holders) == 0 {
			fmt.Fprintf(&b, "    No custom resource with a finalizer holds it; inspect it with: kubectl get %s -o yaml\n", strings.ToLower(strings.Replace(t.Object, "CustomResourceDefinition", "crd", 1)))
			continue
		}
		fmt.Fprintf(&b, "    Held by %d object(s) whose finalizer nothing is running to remove:\n", len(t.Holders))
		for _, h := range t.Holders {
			fmt.Fprintf(&b, "      %s (%s)\n        %s\n", h.Object, strings.Join(h.Finalizers, ", "), h.Remedy)
		}
	}
	fmt.Fprintf(&b, "    Warning: %s\n", clearFinalizersWarning)
	return b.String()
}

func describeCollisions(collisions []collision, adoptTried bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %d object(s) the chart creates already exist and do not belong to Helm release %s/%s:\n", len(collisions), helmNamespace, helmReleaseName)
	adoptable := 0
	for _, c := range collisions {
		verb := "delete it, the chart recreates it (an old spec may be incompatible, so this kind is never adopted)"
		if c.Adoptable {
			verb = "adopt it"
			adoptable++
		} else if strings.HasPrefix(c.Remedy, "kubectl label") {
			verb = "another Helm release claims it; uninstall that release, or take it over by hand"
		}
		fmt.Fprintf(&b, "    %s: %s\n      %s:\n        %s\n", c.Object, c.Problem, verb, c.Remedy)
	}
	if adoptable != 0 && !adoptTried {
		fmt.Fprintf(&b, "  'sympozium install --adopt-existing' adopts the %d adoptable object(s) for you.\n", adoptable)
	}
	return b.String()
}
