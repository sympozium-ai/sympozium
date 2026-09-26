// Partial installed tenant and NetworkPolicy qualification; mutates review
// resources and is not a release gate. Uses real
// TokenRequest credentials, never administrator impersonation or provider keys.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	authv1 "k8s.io/api/authentication/v1"
	core "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const evidenceHold = "sympozium.ai/qualification-evidence"

type check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}
type report struct {
	InstalledAcceptance bool           `json:"installedAcceptance"`
	Journey             string         `json:"journey"`
	Epoch               string         `json:"epoch"`
	Checks              []check        `json:"checks"`
	Subjects            []string       `json:"subjects"`
	Runs                []api.AgentRun `json:"runs"`
	RecoveryRequired    bool           `json:"recoveryRequired"`
}
type harness struct {
	admin                    kubernetes.Interface
	objects                  client.Client
	config                   *rest.Config
	scheme                   *runtime.Scheme
	report                   report
	templates, review, image string
	journey, providerSecret  string
	actors                   []*actor
}
type actor struct {
	namespace, name     string
	kube                kubernetes.Interface
	objects             client.Client
	saUID               types.UID
	roleUID, bindingUID types.UID
}

func main() {
	kubeconfig := flag.String("kubeconfig", "", "explicit kubeconfig")
	contextName := flag.String("context", "", "explicit Kubernetes context")
	output := flag.String("output", "", "new private output directory")
	templates := flag.String("templates", "", "operator fixture template directory")
	review := flag.String("review", "495", "existing isolated review identifier")
	image := flag.String("probe-image", "", "immutable existing review image with curl and sh")
	journey := flag.String("journey", "all", "all, direct, model, or isolation (always partial evidence)")
	providerSecret := flag.String("provider-secret", "review-provider-credential", "exact reviewed fixture Secret name; never reads as administrator")
	flag.Parse()
	if *kubeconfig == "" || *contextName == "" || *output == "" || (*templates == "" && *journey != "isolation") || !validProbeImage(*image) || !validJourney(*journey) || *providerSecret == "" {
		fmt.Fprintln(os.Stderr, "explicit context, output, templates and immutable probe image required")
		os.Exit(2)
	}
	if err := os.Mkdir(*output, 0700); err != nil {
		fmt.Fprintln(os.Stderr, "output must be a new private directory")
		os.Exit(2)
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig}, &clientcmd.ConfigOverrides{CurrentContext: *contextName}).ClientConfig()
	if err != nil {
		os.Exit(2)
	}
	cfg.Timeout = 20 * time.Second
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	objects, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		os.Exit(2)
	}
	admin, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		os.Exit(2)
	}
	nonce := make([]byte, 5)
	if _, err = rand.Read(nonce); err != nil {
		os.Exit(2)
	}
	h := &harness{admin: admin, objects: objects, config: cfg, scheme: scheme, templates: *templates, review: *review, image: *image, report: report{Epoch: hex.EncodeToString(nonce)}}
	h.journey, h.providerSecret = *journey, *providerSecret
	h.report.Journey = *journey
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err = h.run(ctx); err != nil {
		h.record("qualification-completed", false, "qualification stopped; inspect retained resource identities, not raw API diagnostics")
	}
	// Never remove native/controller finalizers or infer teardown from absence.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
	h.cleanup(cleanupCtx)
	cleanupCancel()
	if writeErr := writeReport(*output, h.report); writeErr != nil {
		fmt.Fprintln(os.Stderr, "qualification report could not be persisted; retain review resources for inspection")
		os.Exit(1)
	}
	passed := err == nil && !h.report.RecoveryRequired
	for _, c := range h.report.Checks {
		passed = passed && c.Passed
		fmt.Printf("%t %s: %s\n", c.Passed, c.Name, c.Detail)
	}
	if !passed {
		os.Exit(1)
	}
}
func writeReport(output string, value report) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(output, "report.json"), encoded, 0600)
}
func (h *harness) record(name string, passed bool, detail string) {
	h.report.Checks = append(h.report.Checks, check{name, passed, detail})
}

// Validate every target before creating even the first temporary identity.
// Labels are an operator targeting check, not proof of dedicated isolation.
func (h *harness) preflightNamespaces(ctx context.Context) error {
	for _, suffix := range []string{"a", "b", "system"} {
		ns := "celln-review-" + suffix + "-" + h.review
		namespace, err := h.admin.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		if err != nil {
			return err
		}
		tenant := namespace.Labels["sympozium.ai/celln-review-tenant"]
		if namespace.Labels["sympozium.ai/celln-review"] != h.review || (suffix != "system" && tenant != "enabled") || (suffix == "system" && tenant != "") || namespace.DeletionTimestamp != nil || namespace.Status.Phase != core.NamespaceActive {
			return errors.New("namespace ownership/enablement/lifecycle mismatch")
		}
	}
	return nil
}
func (h *harness) run(ctx context.Context) error {
	if err := h.preflightNamespaces(ctx); err != nil {
		return err
	}
	for _, suffix := range []string{"a", "b"} {
		ns := "celln-review-" + suffix + "-" + h.review
		_, err := h.newActor(ctx, ns, suffix)
		if err != nil {
			return err
		}
	}
	for i, a := range h.actors {
		other := h.actors[1-i]
		_, err := a.kube.CoreV1().Secrets(a.namespace).Get(ctx, h.providerSecret, metav1.GetOptions{})
		h.record(a.namespace+"/own-provider-secret-denied", kerrors.IsForbidden(err), "actual tenant token, get Secret")
		_, err = a.kube.CoreV1().Secrets(other.namespace).Get(ctx, h.providerSecret, metav1.GetOptions{})
		h.record(a.namespace+"/other-provider-secret-denied", kerrors.IsForbidden(err), "actual tenant token, cross-namespace get Secret")
		ns := &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: a.namespace}}
		_, err = a.kube.CoreV1().Namespaces().Patch(ctx, ns.Name, types.MergePatchType, []byte(`{"metadata":{"labels":{"sympozium.ai/qualification-forbidden":"true"}}}`), metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}})
		h.record(a.namespace+"/namespace-label-edit-denied", kerrors.IsForbidden(err), "actual tenant token, server dry-run namespace patch denied")
		if err == nil {
			return errors.New("namespace write unexpectedly authorized")
		}
		for _, mode := range []string{"direct", "model"} {
			if h.journey == "isolation" || (h.journey != "" && h.journey != "all" && h.journey != mode) {
				continue
			}
			if err := h.execute(ctx, a, mode); err != nil {
				return err
			}
		}
	}
	for _, a := range h.actors {
		other := h.actors[0]
		if other == a {
			other = h.actors[1]
		}
		name := "qualification-model-" + h.report.Epoch
		var run api.AgentRun
		err := a.objects.Get(ctx, client.ObjectKey{Namespace: other.namespace, Name: name}, &run)
		h.record(a.namespace+"/other-run-read-denied", kerrors.IsForbidden(err), "actual tenant token, cross-namespace GET denied; runtime ownership not tested")
		var runs api.AgentRunList
		err = a.objects.List(ctx, &runs, client.InNamespace(other.namespace))
		h.record(a.namespace+"/other-run-list-denied", kerrors.IsForbidden(err), "cross-namespace result inventory denied")
		target := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: other.namespace, Name: name}}
		err = a.objects.Delete(ctx, target, client.DryRunAll)
		h.record(a.namespace+"/other-run-delete-denied", kerrors.IsForbidden(err), "cross-namespace server dry-run deletion denied; not a cancellation lifecycle proof")
		if err == nil {
			return errors.New("cross-namespace deletion authorized")
		}
		turn := &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "qualification-forbidden-" + h.report.Epoch, Namespace: other.namespace}, Spec: api.AgentRunTurnSpec{RunName: name, RunUID: "not-authority", Message: "forbidden"}}
		err = a.objects.Create(ctx, turn, client.DryRunAll)
		h.record(a.namespace+"/other-turn-create-denied", kerrors.IsForbidden(err), "cross-namespace server dry-run turn creation denied")
		if err == nil {
			return errors.New("cross-namespace turn creation authorized")
		}
	}
	return h.network(ctx)
}

// Deliberately do not copy administrator client certificates, exec plugins,
// auth-provider settings, impersonation or username/password into tenant config.
func tenantConfig(admin *rest.Config, token string) *rest.Config {
	return &rest.Config{Host: admin.Host, TLSClientConfig: rest.TLSClientConfig{CAFile: admin.CAFile, CAData: append([]byte(nil), admin.CAData...), ServerName: admin.ServerName}, BearerToken: token, Timeout: 20 * time.Second}
}
func (h *harness) newActor(ctx context.Context, ns, suffix string) (*actor, error) {
	name := "qualification-" + suffix + "-" + h.report.Epoch
	sa, err := h.admin.CoreV1().ServiceAccounts(ns).Create(ctx, &core.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name}, AutomountServiceAccountToken: boolp(false)}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	a := &actor{namespace: ns, name: name, saUID: sa.UID}
	// Record immediately so partial setup is visible and can be cleaned safely.
	h.actors = append(h.actors, a)
	role, err := h.admin.RbacV1().Roles(ns).Create(ctx, &rbac.Role{ObjectMeta: metav1.ObjectMeta{Name: name}, Rules: []rbac.PolicyRule{{APIGroups: []string{"sympozium.ai"}, Resources: []string{"agentruns", "agentrunturns"}, Verbs: []string{"get", "list", "watch", "create", "delete"}}}}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	a.roleUID = role.UID
	binding, err := h.admin.RbacV1().RoleBindings(ns).Create(ctx, &rbac.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name}, Subjects: []rbac.Subject{{Kind: "ServiceAccount", Name: name, Namespace: ns}}, RoleRef: rbac.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: name}}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	a.bindingUID = binding.UID
	lifetime := int64(600)
	token, err := h.admin.CoreV1().ServiceAccounts(ns).CreateToken(ctx, name, &authv1.TokenRequest{Spec: authv1.TokenRequestSpec{ExpirationSeconds: &lifetime}}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	config := tenantConfig(h.config, token.Status.Token)
	a.kube, err = kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	a.objects, err = client.New(config, client.Options{Scheme: h.scheme})
	if err != nil {
		return nil, err
	}
	identity, err := a.kube.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	expected := "system:serviceaccount:" + ns + ":" + name
	if identity.Status.UserInfo.Username != expected {
		return nil, errors.New("actual subject mismatch")
	}
	h.report.Subjects = append(h.report.Subjects, expected)
	// Caller does not append again; partial entries above are retained on error.
	return a, nil
}
func (h *harness) execute(ctx context.Context, a *actor, mode string) error {
	file := "direct.yaml"
	if mode == "model" {
		file = "model-one-shot.yaml"
	}
	raw, err := os.ReadFile(filepath.Join(h.templates, file))
	if err != nil || len(raw) > 65536 {
		return errors.New("invalid public template")
	}
	var run api.AgentRun
	if err = yaml.UnmarshalStrict(raw, &run); err != nil {
		return err
	}
	run.Name = "qualification-" + mode + "-" + h.report.Epoch
	run.Namespace = a.namespace
	run.UID = ""
	run.ResourceVersion = ""
	run.Finalizers = []string{evidenceHold}
	text := "sentinel-" + a.namespace + "-" + h.report.Epoch
	expected := strings.ToUpper(text)
	if mode == "direct" {
		raw, _ := json.Marshal(map[string]string{"text": text})
		run.Spec.Task = api.NewStringTask(string(raw))
		raw, _ = json.Marshal(map[string]string{"text": expected})
		expected = string(raw)
	} else {
		run.Spec.Task = api.NewStringTask(text)
	}
	if err = a.objects.Create(ctx, &run); err != nil {
		return err
	}
	h.report.Runs = append(h.report.Runs, run)
	index := len(h.report.Runs) - 1
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		var current api.AgentRun
		if err = a.objects.Get(ctx, client.ObjectKeyFromObject(&run), &current); err != nil {
			return err
		}
		if current.UID != run.UID {
			return errors.New("run identity changed")
		}
		h.report.Runs[index] = current
		if current.Status.CellnScoped != nil && current.Status.CellnScoped.CleanupConfirmed {
			passed := current.Status.Phase == api.AgentRunPhaseSucceeded && current.Status.Result == expected && current.Status.CellnScoped.ReceiptDigest != "" && current.Status.CellnScoped.CellID != "" && current.Status.JobName == ""
			h.record(a.namespace+"/"+mode+"-execution", passed, "actual tenant-created native run, exact namespace sentinel, receipt and cleanup")
			if !passed {
				return errors.New("native result mismatch")
			}
			jobs, err := h.admin.BatchV1().Jobs(a.namespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				return err
			}
			for _, job := range jobs.Items {
				for _, owner := range job.OwnerReferences {
					if owner.UID == run.UID {
						return errors.New("Job fallback observed")
					}
				}
			}
			return nil
		}
		if current.Status.Phase == api.AgentRunPhaseFailed {
			h.record(a.namespace+"/"+mode+"-execution", false, "terminal failure; inspect retained run conditions; cleanup not asserted")
			return errors.New("native execution failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("execution or cleanup unconfirmed")
}
func boolp(v bool) *bool { return &v }
func validProbeImage(image string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9._/:+-]+@sha256:[0-9a-f]{64}$`).MatchString(image)
}
func validJourney(journey string) bool {
	return journey == "all" || journey == "direct" || journey == "model" || journey == "isolation"
}
func (h *harness) cleanup(ctx context.Context) {
	for i := range h.report.Runs {
		run := &h.report.Runs[i]
		if run.Status.CellnScoped == nil || !run.Status.CellnScoped.CleanupConfirmed {
			h.report.RecoveryRequired = true
			continue
		}
		// Only remove this harness's observer hold after recorded native confirmation;
		// optimistic locking and UID preconditions protect against replacement.
		current := &api.AgentRun{}
		if h.objects.Get(ctx, client.ObjectKeyFromObject(run), current) != nil || current.UID != run.UID {
			h.report.RecoveryRequired = true
			continue
		}
		patch := client.MergeFromWithOptions(current.DeepCopy(), client.MergeFromWithOptimisticLock{})
		finalizers := []string{}
		for _, value := range current.Finalizers {
			if value != evidenceHold {
				finalizers = append(finalizers, value)
			}
		}
		current.Finalizers = finalizers
		if h.objects.Patch(ctx, current, patch) != nil || h.objects.Delete(ctx, current, client.Preconditions{UID: &run.UID}) != nil {
			h.report.RecoveryRequired = true
		}
	}
	if h.report.RecoveryRequired {
		return
	}
	for _, a := range h.actors {
		for _, resource := range []string{"binding", "role", "sa"} {
			var err error
			switch resource {
			case "binding":
				if a.bindingUID != "" {
					err = h.admin.RbacV1().RoleBindings(a.namespace).Delete(ctx, a.name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &a.bindingUID}})
				}
			case "role":
				if a.roleUID != "" {
					err = h.admin.RbacV1().Roles(a.namespace).Delete(ctx, a.name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &a.roleUID}})
				}
			case "sa":
				err = h.admin.CoreV1().ServiceAccounts(a.namespace).Delete(ctx, a.name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &a.saUID}})
			}
			if err != nil && !kerrors.IsNotFound(err) {
				h.report.RecoveryRequired = true
			}
		}
	}
}
