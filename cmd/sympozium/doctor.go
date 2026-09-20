package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/helmchart"
)

// `sympozium doctor` answers "what is in the way of an install on this
// cluster?" without changing anything. Every check reads; none writes.

const (
	statusPass = "PASS"
	statusWarn = "WARN"
	statusFail = "FAIL"

	cellnSystemNamespace = "celln-system"
	// initContainerStallAfter is how long an init container may run before
	// doctor calls it stuck and shows what it last said.
	initContainerStallAfter = 2 * time.Minute
)

// doctorDetail is one object a finding is about, with its own remedy.
type doctorDetail struct {
	Object  string `json:"object"`
	Problem string `json:"problem,omitempty"`
	Remedy  string `json:"remedy,omitempty"`
}

// doctorFinding is one line of the checklist.
type doctorFinding struct {
	Check   string         `json:"check"`
	Status  string         `json:"status"`
	Summary string         `json:"summary"`
	Details []doctorDetail `json:"details,omitempty"`
	Remedy  []string       `json:"remedy,omitempty"`
	Warning string         `json:"warning,omitempty"`
}

type doctorReport struct {
	OK       bool            `json:"ok"`
	Findings []doctorFinding `json:"findings"`
}

// releaseInfo is what doctor needs of the Helm release.
type releaseInfo struct {
	Status   string
	Revision int
	Chart    string
	Config   map[string]interface{}
}

// doctor carries the cluster access of one run. Everything that is not the
// controller-runtime client is a function, so tests run against the fake
// client without a cluster.
type doctor struct {
	client        client.Client
	serverVersion func() (string, error)
	release       func() (*releaseInfo, error)
	logTail       func(ctx context.Context, namespace, pod, container string, previous bool) string
	now           func() time.Time
	chart         *chart.Chart
	// defaultValues are the --set values a first install would use.
	defaultValues func() ([]string, string, error)
	scope         string
	packageHash   string
}

func newDoctorCmd() *cobra.Command {
	var asJSON bool
	var scope, packageHash string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check what is in the way of an install, without changing anything",
		Long: `Reads the cluster and prints a checklist: PASS, WARN or FAIL per check, each
problem with the exact command that fixes it. Nothing is changed.

It checks that the cluster answers, the state of the Helm release, every object
the chart would create that already exists without belonging to the release
(the "invalid ownership metadata" Helm stops on, all of them in one pass), CRDs
and namespaces stuck Terminating and what holds them, nodes labelled
celln.dev/kvm=true, the Celln fleet's pods (including an init container that
has been waiting, with its last log line), the published fleet package against
the one this CLI installs, and each model backend's credential.

Exits 1 when any check FAILs.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := newClusterDoctor(scope, packageHash)
			if err != nil {
				return err
			}
			report := d.run(cmd.Context())
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return err
				}
			} else {
				printDoctorReport(cmd.OutOrStdout(), report)
			}
			if !report.OK {
				cmd.SilenceErrors = true
				return errors.New("doctor found problems")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the report as JSON")
	cmd.Flags().StringVar(&scope, "celln-fleet-scope", defaultFleetScope, "Fleet scope the install would use, for the package comparison")
	cmd.Flags().StringVar(&packageHash, "celln-fleet-package-hash", "", "Package the install would use (default: the starter package this build pins)")
	return cmd
}

// newClusterDoctor wires doctor to the cluster in the kubeconfig.
func newClusterDoctor(scope, packageHash string) (*doctor, error) {
	if err := initClient(); err != nil {
		return nil, err
	}
	config, err := loadRESTConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	ch, err := helmchart.Load()
	if err != nil {
		return nil, fmt.Errorf("loading embedded chart: %w", err)
	}
	if packageHash == "" {
		packageHash = defaultCellnStarter().PackageHash
	}
	return &doctor{
		client: k8sClient,
		serverVersion: func() (string, error) {
			v, err := clientset.Discovery().ServerVersion()
			if err != nil {
				return "", err
			}
			return v.GitVersion, nil
		},
		release: helmReleaseInfo,
		logTail: func(ctx context.Context, namespace, pod, container string, previous bool) string {
			lines := int64(10)
			raw, err := clientset.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container, TailLines: &lines, Previous: previous}).DoRaw(ctx)
			if err != nil {
				return ""
			}
			return lastLines(string(raw), 3)
		},
		now:           time.Now,
		chart:         ch,
		defaultValues: func() ([]string, string, error) { return doctorDefaultValues(scope) },
		scope:         scope,
		packageHash:   packageHash,
	}, nil
}

// lastLines returns the last few distinct non-empty log lines, oldest first:
// a multi-line diagnosis stays whole and a repeated wait message shows once.
func lastLines(logs string, max int) string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(logs))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && (len(lines) == 0 || lines[len(lines)-1] != line) {
			lines = append(lines, line)
		}
	}
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return strings.Join(lines, " | ")
}

// helmReleaseInfo reads the newest revision of the sympozium release; nil
// when there is none.
func helmReleaseInfo() (*releaseInfo, error) {
	cfg, err := newHelmConfig(helmNamespace)
	if err != nil {
		return nil, err
	}
	history := action.NewHistory(cfg)
	history.Max = 1
	revisions, err := history.Run(helmReleaseName)
	if errors.Is(err, driver.ErrReleaseNotFound) || (err == nil && len(revisions) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rel := revisions[len(revisions)-1]
	info := &releaseInfo{Revision: rel.Version, Config: rel.Config}
	if rel.Info != nil {
		info.Status = string(rel.Info.Status)
	}
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		info.Chart = rel.Chart.Metadata.Version
	}
	return info, nil
}

// doctorDefaultValues are the values a first `sympozium install` without
// flags would use here: the one-shot Celln plane, or the fleet when this
// build pins a starter package and the environment names a model backend.
func doctorDefaultValues(scope string) ([]string, string, error) {
	values, err := cellnInstallSetValues(context.Background(), "", "", nil, 1, false)
	if err != nil {
		return nil, "", err
	}
	source := "a plain 'sympozium install'"
	starter := defaultCellnStarter()
	backends, _, err := backendsFromEnvironment()
	if !starter.complete() || err != nil || len(backends) == 0 {
		return values, source, nil
	}
	fleet, err := cellninstall.FleetValues(cellninstall.FleetOptions{
		Scope: scope, PackageImage: starter.Image, PackageHash: starter.PackageHash, Publisher: starter.Publisher,
		Principal: "sympozium:celln", Backends: backends, Limits: cellninstall.DefaultFleetLimits,
	})
	if err != nil {
		return values, source, nil
	}
	return append(values, fleet...), "a plain 'sympozium install' with the Celln fleet (backend from the environment)", nil
}

func (d *doctor) run(ctx context.Context) doctorReport {
	if ctx == nil {
		ctx = context.Background()
	}
	var findings []doctorFinding
	cluster := d.checkCluster()
	findings = append(findings, cluster)
	if cluster.Status != statusFail {
		rel, releaseFinding := d.checkRelease()
		findings = append(findings, releaseFinding)
		findings = append(findings, d.checkLeftovers(ctx, rel)...)
		findings = append(findings, d.checkKVMNodes(ctx), d.checkFleetPods(ctx), d.checkFleetPackage(ctx), d.checkFleetBudget(ctx), d.checkModelCredentials(ctx), d.checkMediation(ctx, rel))
	}
	report := doctorReport{OK: true, Findings: findings}
	for _, f := range findings {
		if f.Status == statusFail {
			report.OK = false
		}
	}
	return report
}

func failed(check string, err error) doctorFinding {
	return doctorFinding{Check: check, Status: statusFail, Summary: "could not be checked: " + err.Error()}
}

func (d *doctor) checkCluster() doctorFinding {
	version, err := d.serverVersion()
	if err != nil {
		return doctorFinding{Check: "Cluster", Status: statusFail, Summary: "the API server does not answer: " + err.Error(),
			Remedy: []string{"kubectl cluster-info", "kubectl config current-context   # is this the cluster you mean? --kubeconfig selects another"}}
	}
	return doctorFinding{Check: "Cluster", Status: statusPass, Summary: "reachable, Kubernetes " + version}
}

func (d *doctor) checkRelease() (*releaseInfo, doctorFinding) {
	const check = "Helm release"
	rel, err := d.release()
	if err != nil {
		return nil, failed(check, err)
	}
	name := helmNamespace + "/" + helmReleaseName
	if rel == nil {
		return nil, doctorFinding{Check: check, Status: statusPass, Summary: "no release " + name + "; the next install is a first install"}
	}
	summary := fmt.Sprintf("%s revision %d (chart %s) is %s", name, rel.Revision, rel.Chart, rel.Status)
	switch release.Status(rel.Status) {
	case release.StatusDeployed, release.StatusSuperseded:
		return rel, doctorFinding{Check: check, Status: statusPass, Summary: summary + "; the next install upgrades it"}
	default:
		return rel, doctorFinding{Check: check, Status: statusWarn, Summary: summary + "; 'sympozium install' uninstalls this revision and installs afresh",
			Remedy: []string{"sympozium install", fmt.Sprintf("helm -n %s uninstall %s   # to clear it yourself first", helmNamespace, helmReleaseName)}}
	}
}

// checkLeftovers is the install's own preflight: ownership collisions and
// Terminating CRDs or namespaces, for the values the install would use.
func (d *doctor) checkLeftovers(ctx context.Context, rel *releaseInfo) []doctorFinding {
	const ownership, deletion = "Ownership", "Terminating"
	var vals map[string]interface{}
	var source string
	if rel != nil && len(rel.Config) != 0 {
		vals, source = rel.Config, "the release's own values"
	} else {
		set, from, err := d.defaultValues()
		if err != nil {
			return []doctorFinding{failed(ownership, err)}
		}
		if vals, err = buildHelmValues("", set); err != nil {
			return []doctorFinding{failed(ownership, err)}
		}
		source = from
	}
	objects, err := renderReleaseObjects(d.chart, vals)
	if err != nil {
		return []doctorFinding{failed(ownership, err)}
	}
	var findings []doctorFinding

	collisions, err := ownershipCollisions(ctx, d.client, objects)
	switch {
	case err != nil:
		findings = append(findings, failed(ownership, err))
	case len(collisions) == 0:
		findings = append(findings, doctorFinding{Check: ownership, Status: statusPass,
			Summary: fmt.Sprintf("none of the %d objects the chart creates (rendered with %s) exists outside the release", len(objects), source)})
	default:
		f := doctorFinding{Check: ownership, Status: statusFail,
			Summary: fmt.Sprintf("%d object(s) the chart creates (rendered with %s) exist without belonging to release %s/%s; Helm refuses each with \"invalid ownership metadata\"", len(collisions), source, helmNamespace, helmReleaseName)}
		adoptable := 0
		for _, c := range collisions {
			if c.Adoptable {
				adoptable++
			}
			f.Details = append(f.Details, doctorDetail{Object: c.Object, Problem: c.Problem, Remedy: c.Remedy})
		}
		if adoptable != 0 {
			f.Remedy = []string{fmt.Sprintf("sympozium install --adopt-existing   # adopts the %d adoptable object(s); workloads must still be deleted", adoptable)}
		}
		findings = append(findings, f)
	}

	crds, err := chartCRDs(d.chart)
	if err != nil {
		return append(findings, failed(deletion, err))
	}
	stuck, err := terminatingBlockers(ctx, d.client, crds, releaseNamespaces(objects))
	switch {
	case err != nil:
		findings = append(findings, failed(deletion, err))
	case len(stuck) == 0:
		findings = append(findings, doctorFinding{Check: deletion, Status: statusPass, Summary: "no CRD or namespace of the install is being deleted"})
	default:
		f := doctorFinding{Check: deletion, Status: statusFail, Warning: clearFinalizersWarning}
		var names []string
		for _, t := range stuck {
			names = append(names, t.Object+" (since "+t.Since+")")
			for _, reason := range t.Reasons {
				f.Details = append(f.Details, doctorDetail{Object: t.Object, Problem: reason})
			}
			if len(t.Holders) == 0 && len(t.Reasons) == 0 {
				f.Details = append(f.Details, doctorDetail{Object: t.Object, Problem: "no custom resource with a finalizer holds it", Remedy: "kubectl get " + kubectlName(t.Object) + " -o yaml   # see metadata.finalizers and status.conditions"})
			}
			for _, h := range t.Holders {
				f.Details = append(f.Details, doctorDetail{Object: h.Object, Problem: "holds " + t.Object + " with finalizer " + strings.Join(h.Finalizers, ", ") + "; nothing is running to remove it", Remedy: h.Remedy})
			}
		}
		f.Summary = "stuck in deletion, so an install cannot reuse them: " + strings.Join(names, "; ")
		findings = append(findings, f)
	}
	return findings
}

// kubectlName turns "CustomResourceDefinition x" into "crd x".
func kubectlName(object string) string {
	kind, name, _ := strings.Cut(object, " ")
	if kind == crdGVK.Kind {
		kind = "crd"
	}
	return strings.ToLower(kind) + " " + name
}

func (d *doctor) checkKVMNodes(ctx context.Context) doctorFinding {
	const check = "KVM nodes"
	labelled, unlabelled, err := cellninstall.KVMNodes(ctx, d.client)
	if err != nil {
		return failed(check, err)
	}
	label := cellninstall.KVMNodeLabel + "=true"
	if len(labelled) != 0 {
		return doctorFinding{Check: check, Status: statusPass, Summary: fmt.Sprintf("%d node(s) labelled %s: %s", len(labelled), label, strings.Join(labelled, ", "))}
	}
	return doctorFinding{Check: check, Status: statusWarn,
		Summary: "no node is labelled " + label + ", so the Celln fleet has nowhere to run (the node probe labels a node with /dev/kvm and a kernel under /boot); unlabelled: " + strings.Join(unlabelled, ", "),
		Remedy:  []string{"kubectl label node <name> " + label + "   # a node you know has /dev/kvm"}}
}

// fleetInstalled reports whether the fleet's configure DaemonSet exists.
func (d *doctor) fleetInstalled(ctx context.Context) (bool, error) {
	var ds appsv1.DaemonSet
	err := d.client.Get(ctx, types.NamespacedName{Namespace: cellnSystemNamespace, Name: cellninstall.FleetConfigureDaemonSet}, &ds)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (d *doctor) checkFleetPods(ctx context.Context) doctorFinding {
	const check = "Fleet pods"
	var pods corev1.PodList
	if err := d.client.List(ctx, &pods, client.InNamespace(cellnSystemNamespace)); err != nil {
		return failed(check, err)
	}
	if len(pods.Items) == 0 {
		return doctorFinding{Check: check, Status: statusPass, Summary: "no pods in " + cellnSystemNamespace + " (Celln not installed, or no node to run on)"}
	}
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	f := doctorFinding{Check: check, Status: statusPass}
	raise := func(status string) {
		if f.Status != statusFail {
			f.Status = status
		}
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodSucceeded {
			continue
		}
		before := len(f.Details)
		for _, s := range pod.Status.InitContainerStatuses {
			if detail, ok := d.stuckContainer(ctx, pod, s, true); ok {
				f.Details = append(f.Details, detail)
				raise(statusFail)
			}
		}
		for _, s := range pod.Status.ContainerStatuses {
			if detail, ok := d.stuckContainer(ctx, pod, s, false); ok {
				f.Details = append(f.Details, detail)
				raise(statusFail)
			}
		}
		if len(f.Details) == before && pod.Status.Phase == corev1.PodPending && pod.Spec.NodeName == "" {
			problem := "not scheduled"
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.PodScheduled && c.Message != "" {
					problem += ": " + c.Message
				}
			}
			f.Details = append(f.Details, doctorDetail{Object: "Pod " + pod.Name, Problem: problem, Remedy: "kubectl -n " + cellnSystemNamespace + " describe pod " + pod.Name})
			raise(statusWarn)
		}
	}
	if len(f.Details) == 0 {
		f.Summary = fmt.Sprintf("%d pod(s) in %s, none stuck", len(pods.Items), cellnSystemNamespace)
	} else {
		f.Summary = fmt.Sprintf("%d problem(s) among the %d pod(s) in %s", len(f.Details), len(pods.Items), cellnSystemNamespace)
	}
	return f
}

// stuckContainer reports a container that is not going to get better by
// itself: backing off, failing to pull, or an init container still running
// after initContainerStallAfter. Its last log line usually says why.
func (d *doctor) stuckContainer(ctx context.Context, pod *corev1.Pod, s corev1.ContainerStatus, init bool) (doctorDetail, bool) {
	role := "container"
	if init {
		role = "init container"
	}
	problem, previous := "", false
	switch {
	case s.State.Waiting != nil:
		switch s.State.Waiting.Reason {
		case "PodInitializing", "ContainerCreating", "":
			return doctorDetail{}, false
		}
		problem = fmt.Sprintf("%s %s is waiting: %s (%d restarts)", role, s.Name, s.State.Waiting.Reason, s.RestartCount)
		if s.State.Waiting.Message != "" && s.RestartCount == 0 {
			problem += ": " + s.State.Waiting.Message
		}
		previous = s.RestartCount > 0
	case init && s.State.Running != nil:
		running := d.now().Sub(s.State.Running.StartedAt.Time)
		if running < initContainerStallAfter {
			return doctorDetail{}, false
		}
		problem = fmt.Sprintf("%s %s has been running for %s without finishing", role, s.Name, running.Round(time.Second))
	case init && s.State.Terminated != nil && s.State.Terminated.ExitCode != 0:
		problem = fmt.Sprintf("%s %s exited %d", role, s.Name, s.State.Terminated.ExitCode)
	default:
		return doctorDetail{}, false
	}
	if line := d.logTail(ctx, pod.Namespace, pod.Name, s.Name, previous); line != "" {
		problem += "; last log: " + line
	}
	remedy := fmt.Sprintf("kubectl -n %s logs %s -c %s", pod.Namespace, pod.Name, s.Name)
	if previous {
		remedy += " --previous"
	}
	object := "Pod " + pod.Name
	if pod.Spec.NodeName != "" {
		object += " on node " + pod.Spec.NodeName
	}
	return doctorDetail{Object: object, Problem: problem, Remedy: remedy}, true
}

func (d *doctor) checkFleetPackage(ctx context.Context) doctorFinding {
	const check = "Fleet package"
	publication, err := cellninstall.ReadFleetPublication(ctx, d.client)
	if err != nil {
		return failed(check, err)
	}
	if !publication.Exists {
		return doctorFinding{Check: check, Status: statusPass, Summary: "no fleet configuration is published; an install publishes its own package"}
	}
	running := fmt.Sprintf("the fleet runs package %s in scope %s", publication.Package, publication.Scope)
	if d.packageHash == "" {
		return doctorFinding{Check: check, Status: statusWarn,
			Summary: running + "; this build pins no starter package, so there is nothing to compare it with",
			Remedy:  []string{"sympozium doctor --celln-fleet-package-hash <the hash you will install>"}}
	}
	if !publication.Replaces(d.scope, d.packageHash) {
		return doctorFinding{Check: check, Status: statusPass, Summary: running + ", the package this CLI installs"}
	}
	return doctorFinding{Check: check, Status: statusWarn,
		Summary: fmt.Sprintf("%s; this CLI installs package %s in scope %s. The install refuses the move unless approved, and moving ends every live parent on the fleet", running, d.packageHash, d.scope),
		Remedy: []string{
			"sympozium install --celln-fleet-replace-package   # move the fleet; live parents are lost",
			fmt.Sprintf("sympozium install --celln-fleet-scope %s --celln-fleet-package-hash %s --celln-fleet-package-image <image> --celln-fleet-publisher <key>   # keep the installed package", publication.Scope, publication.Package),
		}}
}

func (d *doctor) checkModelCredentials(ctx context.Context) doctorFinding {
	const check = "Model credentials"
	installed, err := d.fleetInstalled(ctx)
	if err != nil {
		return failed(check, err)
	}
	if !installed {
		return doctorFinding{Check: check, Status: statusPass, Summary: "no Celln fleet is installed; an install publishes each backend's key"}
	}
	facts, err := cellninstall.ReadFleetFacts(ctx, d.client)
	if err != nil {
		return failed(check, err)
	}
	extra, _, err := cellninstall.ReadExtraBackends(ctx, d.client)
	if err != nil {
		return failed(check, err)
	}
	var secret corev1.Secret
	err = d.client.Get(ctx, types.NamespacedName{Namespace: cellnSystemNamespace, Name: cellninstall.FleetModelCredentialSecret}, &secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return failed(check, err)
	}
	secretMissing := err != nil
	f := doctorFinding{Check: check, Status: statusPass}
	backends := append(append([]cellninstall.ExtraBackend{}, facts.InstallBackends...), extra...)
	for _, b := range backends {
		if len(secret.Data[b.Name]) != 0 {
			continue
		}
		f.Status = statusFail
		f.Details = append(f.Details, doctorDetail{
			Object:  "backend " + b.Name + " (" + b.Provider + "/" + b.Model + ")",
			Problem: fmt.Sprintf("Secret %s/%s has no key %q, so nodes wait for it and no parent can run on this backend", cellnSystemNamespace, cellninstall.FleetModelCredentialSecret, b.Name),
			Remedy:  credentialRemedy(b.Name, secretMissing),
		})
	}
	if f.Status == statusPass {
		f.Summary = fmt.Sprintf("Secret %s/%s carries a key for each of the %d backend(s)", cellnSystemNamespace, cellninstall.FleetModelCredentialSecret, len(backends))
	} else {
		f.Summary = fmt.Sprintf("%d of %d backend(s) have no credential", len(f.Details), len(backends))
	}
	return f
}

// credentialRemedy publishes one backend's key without touching the others.
func credentialRemedy(backend string, secretMissing bool) string {
	if secretMissing {
		return fmt.Sprintf("kubectl -n %s create secret generic %s --from-file=%s=/path/to/key", cellnSystemNamespace, cellninstall.FleetModelCredentialSecret, backend)
	}
	return fmt.Sprintf(`kubectl -n %s patch secret %s --type=merge -p "{\"data\":{\"%s\":\"$(base64 -w0 </path/to/key)\"}}"`, cellnSystemNamespace, cellninstall.FleetModelCredentialSecret, backend)
}

func printDoctorReport(out io.Writer, report doctorReport) {
	for _, f := range report.Findings {
		fmt.Fprintf(out, "  %s  %s: %s\n", f.Status, f.Check, f.Summary)
		for _, detail := range f.Details {
			fmt.Fprintf(out, "          - %s", detail.Object)
			if detail.Problem != "" {
				fmt.Fprintf(out, ": %s", detail.Problem)
			}
			fmt.Fprintln(out)
			if detail.Remedy != "" {
				fmt.Fprintf(out, "              %s\n", detail.Remedy)
			}
		}
		if f.Warning != "" {
			fmt.Fprintf(out, "          Warning: %s\n", f.Warning)
		}
		for _, remedy := range f.Remedy {
			fmt.Fprintf(out, "          Remedy: %s\n", remedy)
		}
	}
	counts := map[string]int{}
	for _, f := range report.Findings {
		counts[f.Status]++
	}
	fmt.Fprintf(out, "\n  %d passed, %d warning(s), %d failed.\n", counts[statusPass], counts[statusWarn], counts[statusFail])
}
