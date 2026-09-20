package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/helmchart"
)

var doctorNow = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

const (
	installedPackage = "blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newPackage       = "blake3:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func testDoctor(t *testing.T, objects ...client.Object) *doctor {
	t.Helper()
	ch, err := helmchart.Load()
	if err != nil {
		t.Fatal(err)
	}
	return &doctor{
		client:        preflightClient(t, objects...),
		serverVersion: func() (string, error) { return "v1.31.2", nil },
		release:       func() (*releaseInfo, error) { return nil, nil },
		logTail:       func(context.Context, string, string, string, bool) string { return "" },
		now:           func() time.Time { return doctorNow },
		chart:         ch,
		defaultValues: func() ([]string, string, error) {
			set, err := cellnInstallSetValues(context.Background(), "", "", nil, 1, false)
			return set, "a plain 'sympozium install'", err
		},
		scope:       defaultFleetScope,
		packageHash: newPackage,
	}
}

func findingOf(t *testing.T, report doctorReport, check string) doctorFinding {
	t.Helper()
	for _, f := range report.Findings {
		if f.Check == check {
			return f
		}
	}
	t.Fatalf("no %q finding in %+v", check, report.Findings)
	return doctorFinding{}
}

func kvmNode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{cellninstall.KVMNodeLabel: "true"}}}
}

func TestDoctorPassesOnACleanCluster(t *testing.T) {
	report := testDoctor(t, kvmNode("node-a")).run(context.Background())
	if !report.OK {
		t.Fatalf("clean cluster must pass: %+v", report.Findings)
	}
	for _, f := range report.Findings {
		if f.Status != statusPass {
			t.Fatalf("%s is %s: %s", f.Check, f.Status, f.Summary)
		}
	}
	if got := findingOf(t, report, "Cluster").Summary; got != "reachable, Kubernetes v1.31.2" {
		t.Fatal(got)
	}
}

func TestDoctorStopsAtAnUnreachableCluster(t *testing.T) {
	d := testDoctor(t)
	d.serverVersion = func() (string, error) { return "", errors.New("connection refused") }
	report := d.run(context.Background())
	if report.OK || len(report.Findings) != 1 || report.Findings[0].Status != statusFail || len(report.Findings[0].Remedy) == 0 {
		t.Fatalf("got %+v", report)
	}
}

func TestDoctorReleaseState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		release *releaseInfo
		status  string
		want    string
	}{
		{"none", nil, statusPass, "first install"},
		{"deployed", &releaseInfo{Status: "deployed", Revision: 3, Chart: "0.10.78"}, statusPass, "revision 3 (chart 0.10.78) is deployed"},
		{"failed", &releaseInfo{Status: "failed", Revision: 1, Chart: "0.10.78"}, statusWarn, "uninstalls this revision"},
		{"pending", &releaseInfo{Status: "pending-install", Revision: 1}, statusWarn, "pending-install"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDoctor(t, kvmNode("node-a"))
			d.release = func() (*releaseInfo, error) { return tc.release, nil }
			f := findingOf(t, d.run(context.Background()), "Helm release")
			if f.Status != tc.status || !strings.Contains(f.Summary, tc.want) {
				t.Fatalf("got %s: %s", f.Status, f.Summary)
			}
		})
	}
}

func TestDoctorReportsEveryCollisionWithItsRemedy(t *testing.T) {
	report := testDoctor(t, append(leftovers(), kvmNode("node-a"))...).run(context.Background())
	f := findingOf(t, report, "Ownership")
	if report.OK || f.Status != statusFail || len(f.Details) != 6 {
		t.Fatalf("got %s with %d details: %+v", f.Status, len(f.Details), f.Details)
	}
	remedies := map[string]string{}
	for _, d := range f.Details {
		remedies[d.Object] = d.Remedy
	}
	if !strings.HasPrefix(remedies["Namespace celln-system"], "kubectl label namespace celln-system") ||
		remedies["Deployment celln-system/celln-router"] != "kubectl delete -n celln-system deployment.apps celln-router" {
		t.Fatalf("remedies: %v", remedies)
	}
	if len(f.Remedy) != 1 || !strings.Contains(f.Remedy[0], "sympozium install --adopt-existing") {
		t.Fatalf("remedy: %v", f.Remedy)
	}
}

func TestDoctorRendersADeployedReleaseWithItsOwnValues(t *testing.T) {
	// The release was installed without Celln, so an unowned celln-system is
	// not something its upgrade would create.
	d := testDoctor(t, kvmNode("node-a"), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "celln-system"}})
	d.release = func() (*releaseInfo, error) {
		return &releaseInfo{Status: "deployed", Revision: 1, Config: map[string]interface{}{"createNamespace": false}}, nil
	}
	f := findingOf(t, d.run(context.Background()), "Ownership")
	if f.Status != statusPass || !strings.Contains(f.Summary, "the release's own values") {
		t.Fatalf("got %s: %s", f.Status, f.Summary)
	}
}

func TestDoctorNamesWhatHoldsATerminatingCRD(t *testing.T) {
	since := time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)
	var objects []client.Object
	for _, name := range []string{"run-1", "run-2", "run-3", "run-4"} {
		objects = append(objects, &sympoziumv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Finalizers: []string{"sympozium.ai/agentrun-finalizer"}}})
	}
	report := testDoctor(t, append(objects, terminatingCRD(since), kvmNode("node-a"))...).run(context.Background())
	f := findingOf(t, report, "Terminating")
	if report.OK || f.Status != statusFail || len(f.Details) != 4 || f.Warning == "" {
		t.Fatalf("got %+v", f)
	}
	if !strings.Contains(f.Summary, "CustomResourceDefinition agentruns.sympozium.ai (since 2026-09-15T08:00:00Z)") {
		t.Fatal(f.Summary)
	}
	if d := f.Details[0]; d.Object != "AgentRun default/run-1" || !strings.Contains(d.Problem, "sympozium.ai/agentrun-finalizer") ||
		d.Remedy != `kubectl patch -n default agentruns.sympozium.ai run-1 --type=merge -p '{"metadata":{"finalizers":null}}'` {
		t.Fatalf("detail %+v", d)
	}
}

func TestDoctorKVMNodes(t *testing.T) {
	f := findingOf(t, testDoctor(t, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "plain"}}).run(context.Background()), "KVM nodes")
	if f.Status != statusWarn || !strings.Contains(f.Summary, "plain") || !strings.Contains(f.Remedy[0], "kubectl label node <name> celln.dev/kvm=true") {
		t.Fatalf("got %+v", f)
	}
	f = findingOf(t, testDoctor(t, kvmNode("node-a"), kvmNode("node-b")).run(context.Background()), "KVM nodes")
	if f.Status != statusPass || !strings.Contains(f.Summary, "node-a, node-b") {
		t.Fatalf("got %+v", f)
	}
}

func fleetPod(name string, init, main corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cellnSystemNamespace}, Spec: corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{Phase: corev1.PodPending, InitContainerStatuses: []corev1.ContainerStatus{init}, ContainerStatuses: []corev1.ContainerStatus{main}}}
}

func TestDoctorFleetPods(t *testing.T) {
	running := func(ago time.Duration) corev1.ContainerState {
		return corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(doctorNow.Add(-ago))}}
	}
	waiting := func(reason string) corev1.ContainerState {
		return corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}
	}
	done := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
	for _, tc := range []struct {
		name       string
		pod        *corev1.Pod
		logs       string
		status     string
		want       []string
		wantRemedy string
	}{
		{name: "healthy", status: statusPass,
			pod: fleetPod("celln-node-ok", corev1.ContainerStatus{Name: "wait-prepared", State: done}, corev1.ContainerStatus{Name: "dispatcher", Ready: true, State: running(time.Hour)})},
		{name: "init container just started", status: statusPass,
			pod: fleetPod("celln-node-new", corev1.ContainerStatus{Name: "wait-prepared", State: running(30 * time.Second)}, corev1.ContainerStatus{Name: "dispatcher", State: waiting("PodInitializing")})},
		{name: "init container waiting for ever", status: statusFail,
			pod:  fleetPod("celln-node-x7k2p", corev1.ContainerStatus{Name: "wait-prepared", State: running(14 * time.Minute)}, corev1.ContainerStatus{Name: "dispatcher", State: waiting("PodInitializing")}),
			logs: "waiting for celln-node-configure to admit package abc on this node",
			want: []string{"Pod celln-node-x7k2p on node node-a", "init container wait-prepared has been running for 14m0s", "last log: waiting for celln-node-configure to admit package abc"}, wantRemedy: "kubectl -n celln-system logs celln-node-x7k2p -c wait-prepared"},
		{name: "init container crash loops on an unreadable state directory", status: statusFail,
			pod:  fleetPod("celln-node-q1", corev1.ContainerStatus{Name: "wait-prepared", RestartCount: 4, State: waiting("CrashLoopBackOff")}, corev1.ContainerStatus{Name: "dispatcher", State: waiting("PodInitializing")}),
			logs: "cannot read the Celln state directory /var/lib/sympozium-celln/starter on node node-a: owner 10001:10001 mode 700",
			want: []string{"CrashLoopBackOff (4 restarts)", "cannot read the Celln state directory"}, wantRemedy: "kubectl -n celln-system logs celln-node-q1 -c wait-prepared --previous"},
		{name: "image cannot be pulled", status: statusFail,
			pod:  fleetPod("celln-router-1", corev1.ContainerStatus{Name: "init", State: done}, corev1.ContainerStatus{Name: "router", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "manifest unknown"}}}),
			want: []string{"container router is waiting: ImagePullBackOff", "manifest unknown"}},
		{name: "unschedulable", status: statusWarn,
			pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "celln-router-2", Namespace: cellnSystemNamespace}, Status: corev1.PodStatus{Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Message: "0/1 nodes are available"}}}},
			want: []string{"not scheduled: 0/1 nodes are available"}, wantRemedy: "kubectl -n celln-system describe pod celln-router-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDoctor(t, tc.pod, kvmNode("node-a"))
			d.logTail = func(_ context.Context, namespace, pod, container string, previous bool) string {
				if namespace != cellnSystemNamespace || pod != tc.pod.Name {
					t.Fatalf("logs asked of %s/%s", namespace, pod)
				}
				return tc.logs
			}
			f := findingOf(t, d.run(context.Background()), "Fleet pods")
			if f.Status != tc.status {
				t.Fatalf("got %s: %+v", f.Status, f)
			}
			text := f.Summary
			for _, detail := range f.Details {
				text += "\n" + detail.Object + ": " + detail.Problem
			}
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("lacks %q:\n%s", want, text)
				}
			}
			if tc.wantRemedy != "" && (len(f.Details) != 1 || f.Details[0].Remedy != tc.wantRemedy) {
				t.Fatalf("remedy: %+v", f.Details)
			}
		})
	}
}

func publishedFleet(packageHash, scope string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cellninstall.FleetConfigurationConfigMap, Namespace: cellnSystemNamespace,
		Annotations: map[string]string{"celln.sympozium.ai/package": packageHash, cellninstall.FleetScopeAnnotation: scope}}}
}

func TestDoctorFleetPackage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		published *corev1.ConfigMap
		cliHash   string
		scope     string
		status    string
		want      string
	}{
		{name: "nothing published", cliHash: newPackage, scope: "starter", status: statusPass, want: "no fleet configuration"},
		{name: "same package", published: publishedFleet(newPackage, "starter"), cliHash: newPackage, scope: "starter", status: statusPass, want: "the package this CLI installs"},
		{name: "upgrade moves the package", published: publishedFleet(installedPackage, "starter"), cliHash: newPackage, scope: "starter", status: statusWarn, want: "--celln-fleet-replace-package"},
		{name: "another scope", published: publishedFleet(newPackage, "starter"), cliHash: newPackage, scope: "other", status: statusWarn, want: "--celln-fleet-replace-package"},
		{name: "source build pins nothing", published: publishedFleet(installedPackage, "starter"), scope: "starter", status: statusWarn, want: "pins no starter package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := []client.Object{kvmNode("node-a")}
			if tc.published != nil {
				objects = append(objects, tc.published)
			}
			d := testDoctor(t, objects...)
			d.packageHash, d.scope = tc.cliHash, tc.scope
			f := findingOf(t, d.run(context.Background()), "Fleet package")
			if f.Status != tc.status || !strings.Contains(f.Summary+strings.Join(f.Remedy, "\n"), tc.want) {
				t.Fatalf("got %s: %s %v", f.Status, f.Summary, f.Remedy)
			}
		})
	}
}

func configureDaemonSet() *appsv1.DaemonSet {
	labels, annotations := owned()
	return &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: cellninstall.FleetConfigureDaemonSet, Namespace: cellnSystemNamespace, Labels: labels, Annotations: annotations},
		Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "configure", Env: []corev1.EnvVar{
			{Name: "FLEET_SCOPE", Value: "starter"}, {Name: "FLEET_PRINCIPAL", Value: "sympozium:celln"}, {Name: "FLEET_PACKAGE_HASH", Value: newPackage},
			{Name: "FLEET_BACKENDS", Value: `[{"name":"native","provider":"deepseek","model":"deepseek-chat"},{"name":"openai","provider":"openai","model":"gpt-4o-mini"}]`},
		}}}}}}}
}

func TestDoctorModelCredentials(t *testing.T) {
	secret := func(keys ...string) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: cellninstall.FleetModelCredentialSecret, Namespace: cellnSystemNamespace}, Data: map[string][]byte{}}
		for _, k := range keys {
			s.Data[k] = []byte("key")
		}
		return s
	}
	for _, tc := range []struct {
		name    string
		objects []client.Object
		status  string
		missing []string
		remedy  string
	}{
		{name: "no fleet", status: statusPass},
		{name: "every backend has a key", objects: []client.Object{configureDaemonSet(), secret("native", "openai")}, status: statusPass},
		{name: "one backend lacks its key", objects: []client.Object{configureDaemonSet(), secret("native")}, status: statusFail, missing: []string{"backend openai (openai/gpt-4o-mini)"}, remedy: "kubectl -n celln-system patch secret celln-fleet-model-credentials"},
		{name: "no Secret at all", objects: []client.Object{configureDaemonSet()}, status: statusFail, missing: []string{"backend native (deepseek/deepseek-chat)", "backend openai (openai/gpt-4o-mini)"}, remedy: "kubectl -n celln-system create secret generic celln-fleet-model-credentials --from-file=native=/path/to/key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := findingOf(t, testDoctor(t, append(tc.objects, kvmNode("node-a"))...).run(context.Background()), "Model credentials")
			if f.Status != tc.status || len(f.Details) != len(tc.missing) {
				t.Fatalf("got %+v", f)
			}
			for i, want := range tc.missing {
				if f.Details[i].Object != want {
					t.Fatalf("detail %d: %+v", i, f.Details[i])
				}
			}
			if tc.remedy != "" && !strings.HasPrefix(f.Details[0].Remedy, tc.remedy) {
				t.Fatalf("remedy %q", f.Details[0].Remedy)
			}
		})
	}
}

// usedMachine is the cluster of epic #591 in one fixture.
func usedMachine(t *testing.T) *doctor {
	objects := append(leftovers(), terminatingCRD(time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)), kvmNode("kubeadm-1"),
		&sympoziumv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "old-run", Namespace: "default", Finalizers: []string{"sympozium.ai/agentrun-finalizer"}}},
		publishedFleet(installedPackage, "starter"),
		fleetPod("celln-node-x7k2p", corev1.ContainerStatus{Name: "wait-prepared", RestartCount: 6, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
			corev1.ContainerStatus{Name: "dispatcher", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}}))
	d := testDoctor(t, objects...)
	d.logTail = func(context.Context, string, string, string, bool) string {
		return "If it does not, on the node either move it aside (sudo mv /var/lib/sympozium-celln/starter /var/lib/sympozium-celln/starter.old) or sudo chown 0:0 ..."
	}
	return d
}

func TestDoctorOutput(t *testing.T) {
	report := usedMachine(t).run(context.Background())
	var text bytes.Buffer
	printDoctorReport(&text, report)
	t.Logf("sample output:\n%s", text.String())
	for _, want := range []string{
		"  PASS  Cluster: reachable, Kubernetes v1.31.2", "  FAIL  Ownership: 6 object(s)", "  FAIL  Terminating:", "  FAIL  Fleet pods:", "  WARN  Fleet package:",
		"          - AgentRun default/old-run", "              kubectl patch -n default agentruns.sympozium.ai old-run", "          Warning: Clearing a finalizer",
		"          Remedy: sympozium install --adopt-existing", "warning(s), 3 failed.",
	} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, text.String())
		}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		OK       bool `json:"ok"`
		Findings []struct {
			Check, Status string
			Details       []struct{ Object, Problem, Remedy string }
		} `json:"findings"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.OK || len(decoded.Findings) != len(report.Findings) || decoded.Findings[2].Details[0].Remedy == "" {
		t.Fatalf("json: %v %s", err, raw)
	}
}

func TestDoctorCommandExitsNonZeroOnlyOnFail(t *testing.T) {
	if report := usedMachine(t).run(context.Background()); report.OK {
		t.Fatal("a FAIL must fail the report")
	}
	// Warnings alone (no KVM node) keep the exit code 0.
	if report := testDoctor(t).run(context.Background()); !report.OK {
		t.Fatalf("warnings must not fail: %+v", report.Findings)
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("zero\nfirst\nsecond\n\n  \nthird\n", 3); got != "first | second | third" {
		t.Fatal(got)
	}
	if got := lastLines("waiting\nwaiting\nwaiting\n", 3); got != "waiting" {
		t.Fatal(got)
	}
}

func TestSourceBuildDefaultsToTheChartAppVersion(t *testing.T) {
	appVersion, err := helmchart.AppVersion()
	if err != nil {
		t.Fatal(err)
	}
	chartTag := "v" + appVersion
	installerTag := func(t *testing.T, installerImage string) string {
		t.Helper()
		set, err := cellnInstallSetValues(context.Background(), "", installerImage, nil, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, kv := range set {
			if tag, ok := strings.CutPrefix(kv, "celln.image.tag="); ok {
				return tag
			}
		}
		t.Fatal("celln.image.tag not set")
		return ""
	}
	for _, tc := range []struct {
		name, version, installerImage, wantTag string
		wantNote                               bool
	}{
		{name: "source build", version: "dev", wantTag: chartTag, wantNote: true},
		{name: "unset version", version: "", wantTag: chartTag, wantNote: true},
		{name: "release build", version: "v0.10.78", wantTag: "v0.10.78"},
		{name: "source build, explicit tag wins", version: "dev", installerImage: "registry.local/celln-installer:mine", wantTag: "mine"},
		{name: "source build, bare repository", version: "dev", installerImage: "registry.local/celln-installer", wantTag: chartTag, wantNote: true},
		{name: "release build, explicit tag wins", version: "v0.10.78", installerImage: "registry.local/celln-installer:mine", wantTag: "mine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := version
			version = tc.version
			t.Cleanup(func() { version = previous })
			if got := installerTag(t, tc.installerImage); got != tc.wantTag || got == "latest" {
				t.Fatalf("installer tag %q, want %q", got, tc.wantTag)
			}
			note := sourceBuildInstallerNote(tc.installerImage)
			if (note != "") != tc.wantNote || (tc.wantNote && !strings.Contains(note, chartTag)) {
				t.Fatalf("note %q", note)
			}
		})
	}
}

func TestImageTagFlagStillWinsForControlPlaneImages(t *testing.T) {
	previous := version
	version = "dev"
	t.Cleanup(func() { version = previous })
	ch, err := helmchart.Load()
	if err != nil {
		t.Fatal(err)
	}
	images := func(imageTag string) (controller, installer string) {
		set, err := cellnInstallSetValues(context.Background(), "", "", nil, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		vals, err := buildHelmValues(imageTag, set)
		if err != nil {
			t.Fatal(err)
		}
		objects, err := renderReleaseObjects(ch, vals)
		if err != nil {
			t.Fatal(err)
		}
		for _, obj := range objects {
			if obj.GetKind() != "Deployment" {
				continue
			}
			var d appsv1.Deployment
			raw, _ := json.Marshal(obj.Object)
			if err := json.Unmarshal(raw, &d); err != nil {
				t.Fatal(err)
			}
			switch d.Name {
			case "sympozium-controller-manager":
				controller = d.Spec.Template.Spec.Containers[0].Image
			case "celln-dispatcher":
				installer = d.Spec.Template.Spec.Containers[0].Image
			}
		}
		return controller, installer
	}
	chartTag := ":v" + ch.Metadata.AppVersion
	if controller, installer := images(""); !strings.HasSuffix(controller, chartTag) || !strings.HasSuffix(installer, "/celln-installer"+chartTag) {
		t.Fatalf("source build images: %s %s", controller, installer)
	}
	if controller, installer := images("sideloaded"); !strings.HasSuffix(controller, ":sideloaded") || !strings.HasSuffix(installer, "/celln-installer"+chartTag) {
		t.Fatalf("--image-tag must win for control-plane images and leave the installer image to --celln-installer-image: %s %s", controller, installer)
	}
}
