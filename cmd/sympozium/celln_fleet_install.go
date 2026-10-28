package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
)

// cellnFleetFlags configure the Celln fleet `sympozium install` deploys: one
// reviewed package, one scope, and every node labeled celln.dev/kvm=true
// joins. Nothing here names a model: the fleet calls no provider itself, and
// every Agent brings its own connection and key through the model gateway.
type cellnFleetFlags struct {
	enabled bool
	options cellninstall.FleetOptions
	// outputTokensCeilingSet reports whether the operator passed
	// --celln-fleet-max-output-tokens; unset, the ceiling is sized for the
	// largest per-request cap an Agent's connection may bring (4096), which
	// is what a model-free node is configured for. Nil (tests) treats a
	// non-zero limit as given.
	outputTokensCeilingSet func() bool
	// replacePackage approves moving an installed scope to another package
	// or scope, which ends every live parent on the fleet.
	replacePackage bool
	outputDir      string
	authorise      string
	// gatewayImage overrides the model gateway image this build pins.
	gatewayImage string
	// defaulted is set when a bare `sympozium install` chose the fleet.
	defaulted bool
	wait      time.Duration
}

// noKVMNodeHintAfter is how long a fleet wait runs before an empty fleet is
// explained to the operator.
const noKVMNodeHintAfter = 45 * time.Second

// fleetPrivilegeNotice is printed before the fleet is deployed: the privilege
// the fleet needs is part of the security envelope an operator installs, not
// something behind a flag.
const fleetPrivilegeNotice = `  Celln fleet: on every node labeled celln.dev/kvm=true this deploys two privileged DaemonSets
  (celln-node, celln-node-configure) that mount /dev/kvm, /boot, /lib/modules and a state directory
  under /var/lib/sympozium-celln, because booting micro-VMs needs them. Nodes hold no model key:
  every Agent's model requests leave through the model gateway with that Agent's own key.
  Pass --no-celln to install without the fleet.`

func (f *cellnFleetFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.enabled, "celln-fleet", false, "Deploy the Celln fleet explicitly (it is the default whenever this build pins a starter package; use this with the --celln-fleet-package-* flags for another package)")
	cmd.Flags().StringVar(&f.options.Scope, "celln-fleet-scope", "", "Stable installation identity; node state lives at /var/lib/sympozium-celln/<scope> (default starter)")
	cmd.Flags().StringVar(&f.options.PackageImage, "celln-fleet-package-image", "", "Digest-pinned OCI image (repository@sha256:...) carrying the reviewed 'celln starter-package' output at /package (default: the starter package this build pins)")
	cmd.Flags().StringVar(&f.options.PackageHash, "celln-fleet-package-hash", "", "Exact operator-approved package BLAKE3 identity from 'celln starter-inspect'")
	cmd.Flags().StringVar(&f.options.Publisher, "celln-fleet-publisher", "", "Explicitly approved publisher key of the package from 'celln starter-inspect'")
	cmd.Flags().StringVar(&f.options.Principal, "celln-fleet-principal", "sympozium:celln", "Parent principal every fleet owner authenticates")
	cmd.Flags().Int64Var(&f.options.Limits.LeaseSeconds, "celln-fleet-max-lease-seconds", cellninstall.DefaultFleetLimits.LeaseSeconds, "Longest a parent may live (60–86400); the policy ceiling every run in the scope is admitted under")
	cmd.Flags().Int64Var(&f.options.Limits.MaxTurns, "celln-fleet-max-turns", cellninstall.DefaultFleetLimits.MaxTurns, "Most turns one parent may take (1–1024)")
	cmd.Flags().Int64Var(&f.options.Limits.MaxModelRequests, "celln-fleet-max-model-requests", cellninstall.DefaultFleetLimits.MaxModelRequests, fmt.Sprintf("Most model requests one parent may make over its life (%d–%d); every turn reserves %d, so size it as turns × %d", cellninstall.MinFleetModelRequests, cellninstall.MaxFleetModelRequests, sympoziumv1alpha1.TurnModelRequests, sympoziumv1alpha1.TurnModelRequests))
	cmd.Flags().Int64Var(&f.options.Limits.MaxOutputTokens, "celln-fleet-max-output-tokens", 0, fmt.Sprintf("Most model output tokens one parent may consume over its life (%d–%d). Left unset it is %d turns × %d requests × %d tokens, the largest per-request cap an Agent's connection may set, so every connection fits", cellninstall.MinFleetOutputTokens, cellninstall.MaxFleetOutputTokens, cellninstall.DefaultFleetLimits.MaxTurns, sympoziumv1alpha1.TurnModelRequests, sympoziumv1alpha1.MaxRequestOutputTokens))
	f.outputTokensCeilingSet = func() bool { return cmd.Flags().Changed("celln-fleet-max-output-tokens") }
	cmd.Flags().StringVar(&f.authorise, "celln-fleet-authorise", "all", "Which namespaces may run on the fleet: 'all' (every namespace except kube-*, cert-manager, the control-plane namespaces and namespaces labeled celln.sympozium.ai/excluded) or 'labeled' (only namespaces labeled celln.sympozium.ai/scope=<scope>)")
	cmd.Flags().StringVar(&f.gatewayImage, "model-gateway-image", "", "Digest-pinned model gateway image (repository@sha256:...) (default: the image this release pins; a source build pins none and needs this flag, because the fleet serves model requests only through the gateway)")
	cmd.Flags().StringArrayVar(&f.options.HTTPSHosts, "celln-fleet-https-host", nil, "Restrict the https-fetch and https-post-json starter tools to this exact host, repeatable (lowercase DNS name). Unset, they may reach any public HTTPS host; private addresses are always refused")
	cmd.Flags().BoolVar(&f.replacePackage, "celln-fleet-replace-package", false, "Approve moving an installed fleet to this package or scope (e.g. after upgrading to a sympozium release whose starter package inputs changed; most releases keep the package): nodes publish the new configuration, the scope's catalogue is replaced and every namespace's platform wrappers are rebound. Every live parent on the fleet is lost")
	cmd.Flags().StringVar(&f.outputDir, "celln-fleet-output-dir", "", "Absolute private directory for the materialized configuration and installation records (default ~/.sympozium/celln-fleet/<scope>)")
	cmd.Flags().DurationVar(&f.wait, "celln-fleet-wait", 15*time.Minute, "How long to wait for the first labeled node to publish the starter configuration")
}

// installCellnFleet runs the two-phase fleet installation: deploy the plane so
// labeled nodes prepare themselves, then bind the catalogue and controller to
// the configuration the nodes published. Every step refuses to replace state
// left by an earlier attempt, so a rerun after labeling more nodes is safe.
func installCellnFleet(ctx context.Context, f cellnFleetFlags, imageTag string, setValues []string) error {
	if err := initClient(); err != nil {
		return err
	}
	// The gateway is how every model request leaves a cell, so mediation is
	// not a choice here; only its image may be.
	record, err := cellninstall.ReadMediationRecord(ctx, k8sClient)
	if err != nil {
		return err
	}
	plan, err := planMediation(mediationInputs{flagImage: f.gatewayImage, setValues: setValues, pinned: pinnedModelGatewayImage(), deployedImage: deployedModelGatewayImage(), clusterMediated: record.Enabled})
	if err != nil {
		return err
	}
	if plan.notice != "" {
		fmt.Println("  " + plan.notice)
	}
	if err := f.applyStarterDefaults(); err != nil {
		return err
	}
	if !filepath.IsAbs(f.outputDir) || filepath.Clean(f.outputDir) != f.outputDir {
		return fmt.Errorf("--celln-fleet-output-dir must be a clean absolute directory")
	}
	if err := os.MkdirAll(f.outputDir, 0700); err != nil {
		return err
	}
	if f.outputTokensCeilingSet != nil && !f.outputTokensCeilingSet() {
		f.options.Limits.MaxOutputTokens = 0
	}
	limits, sized, err := f.options.Limits.ResolveModelFree()
	if err != nil {
		return err
	}
	f.options.Limits = limits
	if sized != "" {
		fmt.Println("  " + sized)
	}
	fleetValues, err := cellninstall.FleetValues(f.options)
	if err != nil {
		return err
	}
	// A scope carries one package at a time. Moving it is an explicit,
	// disruptive decision, checked before anything in the cluster changes.
	publication, err := cellninstall.ReadFleetPublication(ctx, k8sClient)
	if err != nil {
		return err
	}
	replacing := publication.Replaces(f.options.Scope, f.options.PackageHash)
	if replacing && !f.replacePackage {
		return publication.ReplacementRefusal(f.options.Scope, f.options.PackageHash)
	}
	if notice := fleetPackageUnchangedNotice(publication, f.options.Scope, f.options.PackageHash); notice != "" {
		fmt.Println("  " + notice)
	}
	// The trust's identity is settled before the first install renders it:
	// an existing bootstrap keeps its key id (verified, never rotated), a
	// new one gets an id chosen here and is published right after the chart
	// creates the namespaces. The values are derived again on every run.
	var mediationValues []string
	var trustOptions cellninstall.MediationOptions
	if plan.auto {
		gatewayHosts, receiverHosts, systemNamespace, err := mediationHosts(setValues)
		if err != nil {
			return err
		}
		keyID, found, err := cellninstall.ExistingMediationKeyID(ctx, k8sClient, systemNamespace)
		if err != nil {
			return err
		}
		if !found {
			if keyID, err = cellninstall.NewMediationKeyID(time.Now()); err != nil {
				return err
			}
		}
		clusterID, err := cellninstall.ClusterIdentity(ctx, k8sClient)
		if err != nil {
			return err
		}
		if mediationValues, err = cellninstall.AutoMediationValues(clusterID, keyID, plan.image); err != nil {
			return err
		}
		trustOptions = cellninstall.MediationOptions{ClusterID: clusterID, SystemNamespace: systemNamespace, GatewayHosts: gatewayHosts, ReceiverHosts: receiverHosts, KeyID: keyID}
	}
	if plan.operator {
		// Last, so it also settles a flag against a --set of the image.
		mediationValues = []string{"modelGateway.image=" + plan.image}
	}
	values := append(append(append([]string{}, setValues...), fleetValues...), mediationValues...)
	if replacing {
		fmt.Printf("  Moving the fleet from package %s to %s in scope %s; every live parent on the fleet is lost.\n", publication.Package, f.options.PackageHash, f.options.Scope)
		if err := cellninstall.ApproveFleetReplacement(ctx, k8sClient, publication, f.options.Scope, f.options.PackageHash); err != nil {
			return err
		}
	}
	fmt.Println(fleetPrivilegeNotice)
	if err := runInstall(imageTag, values); err != nil {
		return err
	}
	// The chart owns celln-system; the nodes and the router block on these
	// objects until they exist, so publishing after the install is safe.
	if err := cellninstall.PrepareFleetTrust(ctx, k8sClient, f.options.Principal); err != nil {
		return err
	}
	if plan.auto {
		trust, err := cellninstall.PrepareMediationTrust(ctx, k8sClient, trustOptions)
		if err != nil {
			return err
		}
		state := "verified the existing"
		if trust.Created {
			state = "published new"
		}
		fmt.Printf("  Model gateway: %s trust (issuer key id %s, cluster %s); certificates for %s and %s.\n", state, trust.KeyID, trust.ClusterID, trustOptions.GatewayHosts[0], trustOptions.ReceiverHosts[0])
	}
	fmt.Println("  Fleet plane deployed. Nodes with /dev/kvm and a boot kernel are labelled celln.dev/kvm=true by the node probe; label others by hand.")
	fmt.Printf("  Waiting up to %s for the first node to admit the package and publish the starter configuration...\n", f.wait)
	// One directory per package: files materialized for an earlier package
	// must never be compared with, or mistaken for, this one's.
	configuration := filepath.Join(f.outputDir, "configuration-"+strings.TrimPrefix(f.options.PackageHash, "blake3:")[:16])
	deadline := time.Now().Add(f.wait)
	// An empty fleet is the usual reason a wait stalls (no node with KVM and
	// a kernel, or a Kind node); say so once, early, instead of at the deadline.
	emptyFleetCheck := time.Now().Add(noKVMNodeHintAfter)
	hinted := false
	wait := func(what string, ready func() (bool, error)) error {
		for {
			done, err := ready()
			if err != nil || done {
				return err
			}
			if !hinted && time.Now().After(emptyFleetCheck) {
				hinted = true
				if labelled, unlabelled, err := cellninstall.KVMNodes(ctx, k8sClient); err == nil && len(labelled) == 0 {
					fmt.Print(cellninstall.NoKVMNodeHint(unlabelled))
				}
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s did not happen within %s; label a KVM node (or give a Kind node a kernel), check the celln-node-configure logs in celln-system, then rerun this command", what, f.wait)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
	}
	if err := wait("publishing the starter configuration", func() (bool, error) {
		return cellninstall.ReadFleetConfigurationFor(ctx, k8sClient, configuration, f.options.PackageHash, []string{cellnplatform.DefaultBackend})
	}); err != nil {
		return err
	}
	if err := wait("the controller rollout", func() (bool, error) {
		return cellninstall.ControllerRolledOut(ctx, k8sClient, helmNamespace)
	}); err != nil {
		return err
	}
	clusterID, err := cellninstall.ClusterIdentity(ctx, k8sClient)
	if err != nil {
		return err
	}
	platform := cellninstall.PlatformOptions{Namespace: namespace, ConfigurationDir: configuration, OutputDir: filepath.Join(f.outputDir, "installation"), Scope: f.options.Scope, ClusterID: clusterID, PackageHash: f.options.PackageHash, Principal: f.options.Principal, ControllerNamespace: helmNamespace, Authorise: f.authorise}
	if replacing {
		platform.Replacing = publication
	}
	if err := cellninstall.InstallPlatform(ctx, k8sClient, platform); err != nil {
		return err
	}
	fmt.Println("  Model access: every Celln Agent brings its own connection (any OpenAI- or Anthropic-compatible endpoint) and its own key; the key is pinned to that endpoint's origin and never reaches a node. Narrow where keys may go with allowedOrigins/deniedOrigins on the scope's CellnExecutionPolicy; approve a private (LAN) endpoint once with modelGateway.privateOrigins.")
	if f.authorise == cellnplatform.AuthoriseLabeled {
		fmt.Printf("  Enabled Celln runs on fleet %q for namespaces labeled %s=%s; %s is labeled and carries the wrapper objects. No run submitted.\n", f.options.Scope, cellninstall.ScopeLabel, f.options.Scope, namespace)
	} else {
		fmt.Printf("  Enabled Celln runs on fleet %q for every namespace except the system exclusions and namespaces labeled %s; %s carries the wrapper objects and any other namespace gets them on first use. No run submitted.\n", f.options.Scope, cellnplatform.ExcludedLabel, namespace)
	}
	return nil
}

// fleetPackageUnchangedNotice tells an operator upgrading an installed fleet
// that this install keeps the package and scope the nodes published, so no
// dispatcher restarts and no live parent is lost. It is empty for a first
// install and for a replacement, which says what it costs instead.
func fleetPackageUnchangedNotice(p cellninstall.FleetPublication, scope, packageHash string) string {
	if !p.Exists || p.Replaces(scope, packageHash) {
		return ""
	}
	return fmt.Sprintf("Celln fleet package unchanged (%s); live conversations are kept", packageHash)
}
