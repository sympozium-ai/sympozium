package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	"helm.sh/helm/v3/pkg/strvals"

	"github.com/sympozium-ai/sympozium/internal/helmchart"
)

func newUpgradeCmd() *cobra.Command {
	var imageTag string
	var setValues []string
	var noErgoz bool
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade the Sympozium installation in the current cluster to this CLI's release",
		Long: `Upgrades an existing Sympozium installation to the Helm chart embedded in
this CLI. Run 'sympozium update' first to get the latest CLI.

Unlike 'install', upgrade keeps the configuration the installation was made
with: it reuses the deployed release's values (Celln plane, fleet backends,
--set overrides) and only moves the version-bound pins forward — the
control-plane image tag, the Celln installer image tag, the Celln router
image tag and, with mediated model access, the model gateway image digest,
where they still point at the default images. It then applies the
chart's CRDs and upgrades the Helm release, and upgrades ergoz if installed.

Use --set to change values on top of the reused ones, and --image-tag to pin
the control-plane images to a specific tag. It refuses a cluster with no
deployed release; use 'sympozium install' there.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(imageTag, setValues, noErgoz, dryRun)
		},
	}
	cmd.Flags().StringVar(&imageTag, "image-tag", "", "Pin the control-plane image tag (default: the embedded chart's appVersion)")
	cmd.Flags().StringArrayVar(&setValues, "set", nil, "Set Helm values on top of the deployed ones (key=value, can be repeated)")
	cmd.Flags().BoolVar(&noErgoz, "no-ergoz", false, "Do not upgrade an installed ergoz release")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would change without touching the cluster")
	return cmd
}

func runUpgrade(imageTag string, setValues []string, noErgoz, dryRun bool) error {
	deployed, err := helmReleaseInfo()
	if err != nil {
		return fmt.Errorf("read the deployed release: %w", err)
	}
	if deployed == nil {
		return fmt.Errorf("Sympozium is not installed in %s; run 'sympozium install'", helmNamespace)
	}
	if s := release.Status(deployed.Status); s != release.StatusDeployed && s != release.StatusSuperseded {
		return fmt.Errorf("the latest %s release is %q, not deployed; run 'sympozium install' to recover it", helmReleaseName, deployed.Status)
	}

	ch, err := helmchart.Load()
	if err != nil {
		return fmt.Errorf("loading embedded chart: %w", err)
	}
	releaseTag, _, err := defaultReleaseTag()
	if err != nil {
		return err
	}

	vals := deployed.Config
	if vals == nil {
		vals = map[string]interface{}{}
	}
	notes := refreshUpgradeValues(vals, imageTag, releaseTag, defaultCellnStarter())
	notes = append(notes, refreshModelGatewayImage(vals, pinnedModelGatewayImage())...)
	for _, kv := range setValues {
		if !strings.Contains(kv, "=") {
			return fmt.Errorf("invalid --set value %q (expected key=value)", kv)
		}
		if err := strvals.ParseInto(kv, vals); err != nil {
			return fmt.Errorf("parsing --set %q: %w", kv, err)
		}
	}

	fmt.Printf("  Upgrading Sympozium chart %s (revision %d) → %s (app %s)\n",
		deployed.Chart, deployed.Revision, ch.Metadata.Version, ch.Metadata.AppVersion)
	for _, n := range notes {
		fmt.Println("  " + n)
	}
	if dryRun {
		fmt.Println("  Dry run: nothing changed.")
		return nil
	}

	if err := applyCRDs(ch); err != nil {
		return err
	}
	fmt.Println("  Updating Gateway API CRDs...")
	if err := kubectlRetry(4, "apply", "--server-side", "--force-conflicts", "-f", gatewayAPICRDsURL); err != nil {
		return fmt.Errorf("apply Gateway API CRDs: %w", err)
	}
	if err := helmInstallOrUpgrade(ch, vals); err != nil {
		return err
	}
	if !noErgoz {
		installed, err := helmReleaseExists(ergozReleaseName, ergozNamespace)
		if err != nil {
			fmt.Printf("  ergoz not upgraded: %v\n", err)
		} else if installed {
			_ = installErgozUnless(false)
		}
	}
	fmt.Println("\n  Sympozium upgraded. Pods roll to the new images as their Deployments update.")
	return nil
}

// refreshUpgradeValues moves the version-bound pins in a deployed release's
// user-supplied values to this build, in place, and describes each change.
// Pins pointing at a non-default repository or a digest are the operator's
// and stay as they are.
func refreshUpgradeValues(vals map[string]interface{}, imageTag, releaseTag string, starter cellnStarter) []string {
	var notes []string
	old, _ := nestedString(vals, "image", "tag")
	switch {
	case imageTag != "":
		setNested(vals, imageTag, "image", "tag")
		if old != imageTag {
			notes = append(notes, fmt.Sprintf("Control-plane image tag: %s → %s", orDefault(old), imageTag))
		}
	case old != "":
		deleteNested(vals, "image", "tag")
		notes = append(notes, fmt.Sprintf("Control-plane image tag: dropping pinned %s; images follow the chart appVersion", old))
	}

	if enabled, _ := nestedValue(vals, "celln", "enabled").(bool); !enabled {
		return notes
	}
	if repo, _ := nestedString(vals, "celln", "image", "repository"); repo == "" || repo == defaultCellnInstallerRepo {
		if old, _ := nestedString(vals, "celln", "image", "tag"); old != releaseTag {
			setNested(vals, releaseTag, "celln", "image", "tag")
			notes = append(notes, fmt.Sprintf("Celln installer image tag: %s → %s", orDefault(old), releaseTag))
		}
	}
	routerRepo, _ := nestedString(vals, "celln", "router", "image", "repository")
	routerDigest, _ := nestedString(vals, "celln", "router", "image", "digest")
	if routerRepo == defaultCellnRouterRepo && routerDigest == "" {
		if old, _ := nestedString(vals, "celln", "router", "image", "tag"); old != defaultCellnRouterTag {
			setNested(vals, defaultCellnRouterTag, "celln", "router", "image", "tag")
			notes = append(notes, fmt.Sprintf("Celln router image tag: %s → %s", orDefault(old), defaultCellnRouterTag))
		}
	}
	if fleet, _ := nestedValue(vals, "celln", "fleet", "enabled").(bool); fleet && starter.complete() {
		if pkg, _ := nestedString(vals, "celln", "fleet", "package", "image"); pkg != starter.Image {
			notes = append(notes, "Celln fleet: this release pins a different starter package; the fleet keeps its current one. "+
				"To move it, rerun 'sympozium install --celln-fleet --celln-fleet-replace-package' (every live parent is lost).")
		}
	}
	return notes
}

// refreshModelGatewayImage moves a mediated release's model gateway to the
// image this build pins, when the release runs the default repository's
// gateway; an operator's own repository stays as it is. A build without a pin
// changes nothing.
func refreshModelGatewayImage(vals map[string]interface{}, pinned string) []string {
	if pinned == "" {
		return nil
	}
	if enabled, _ := nestedValue(vals, "celln", "mediation", "enabled").(bool); !enabled {
		return nil
	}
	old, _ := nestedString(vals, "modelGateway", "image")
	if old == pinned || (old != "" && !strings.HasPrefix(old, defaultModelGatewayRepo+"@")) {
		return nil
	}
	setNested(vals, pinned, "modelGateway", "image")
	return []string{fmt.Sprintf("Model gateway image: %s → %s", orDefault(old), pinned)}
}

// helmReleaseExists reports whether a release has any history in namespace.
func helmReleaseExists(name, namespace string) (bool, error) {
	cfg, err := newHelmConfig(namespace)
	if err != nil {
		return false, err
	}
	history := action.NewHistory(cfg)
	history.Max = 1
	revisions, err := history.Run(name)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return false, nil
	}
	return err == nil && len(revisions) > 0, err
}

func orDefault(s string) string {
	if s == "" {
		return "(chart default)"
	}
	return s
}

func nestedValue(m map[string]interface{}, path ...string) interface{} {
	var cur interface{} = m
	for _, k := range path {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func nestedString(m map[string]interface{}, path ...string) (string, bool) {
	s, ok := nestedValue(m, path...).(string)
	return s, ok
}

func setNested(m map[string]interface{}, value interface{}, path ...string) {
	for _, k := range path[:len(path)-1] {
		next, ok := m[k].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			m[k] = next
		}
		m = next
	}
	m[path[len(path)-1]] = value
}

func deleteNested(m map[string]interface{}, path ...string) {
	parent, ok := nestedValue(m, path[:len(path)-1]...).(map[string]interface{})
	if ok {
		delete(parent, path[len(path)-1])
	}
}
