package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
)

// cellnFleetFlags configure `sympozium install --celln-fleet`: one reviewed
// package, one scope, and every node labeled celln.dev/kvm=true joins.
type cellnFleetFlags struct {
	enabled       bool
	options       cellninstall.FleetOptions
	backendSpecs  []string
	skipPreflight bool
	// modelParametersFile is the single-backend form of parameters-file=.
	modelParametersFile string
	// modelMaxOutputTokens is the single-backend form of max-output-tokens=.
	modelMaxOutputTokens int64
	// outputTokensCeilingSet reports whether the operator passed
	// --celln-fleet-max-output-tokens; unset, the ceiling is sized for the
	// most expensive backend. Nil (tests) treats a non-zero limit as given.
	outputTokensCeilingSet func() bool
	// replacePackage approves moving an installed scope to another package
	// or scope, which ends every live parent on the fleet.
	replacePackage bool
	outputDir      string
	authorise      string
	// mediateBackends and mediatedRouteSpecs declare which providers an Agent
	// may bring its own key for (chart values celln.mediation.mediateBackends
	// and celln.mediation.routes). Nothing is declared by default.
	mediateBackends    bool
	mediatedRouteSpecs []string
	// defaulted is set when a bare `sympozium install` chose the fleet.
	defaulted bool
	wait      time.Duration
}

// noKVMNodeHintAfter is how long a fleet wait runs before an empty fleet is
// explained to the operator.
const noKVMNodeHintAfter = 45 * time.Second

func (f *cellnFleetFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.enabled, "celln-fleet", false, "Run the native Celln plane as a per-node fleet: every node labeled celln.dev/kvm=true prepares the reviewed starter package and serves enduring parents (requires the --celln-fleet-* inputs and --celln-native-approve-starter-tools)")
	cmd.Flags().StringVar(&f.options.Scope, "celln-fleet-scope", "", "Stable installation identity; node state lives at /var/lib/sympozium-celln/<scope> (default starter)")
	cmd.Flags().StringVar(&f.options.PackageImage, "celln-fleet-package-image", "", "Digest-pinned OCI image (repository@sha256:...) carrying the reviewed 'celln starter-package' output at /package (default: the starter package this build pins)")
	cmd.Flags().StringVar(&f.options.PackageHash, "celln-fleet-package-hash", "", "Exact operator-approved package BLAKE3 identity from 'celln starter-inspect'")
	cmd.Flags().StringVar(&f.options.Publisher, "celln-fleet-publisher", "", "Explicitly approved publisher key of the package from 'celln starter-inspect'")
	cmd.Flags().StringVar(&f.options.Principal, "celln-fleet-principal", "sympozium:celln", "Parent principal every fleet owner authenticates")
	cmd.Flags().StringVar(&f.options.Model.Provider, "celln-fleet-model-provider", cellninstall.ModelProviderDeepSeek, "Model backend for the starter agent: deepseek, openai, anthropic or llama-server (any other name needs --celln-fleet-model-endpoint and --celln-fleet-model-protocol)")
	cmd.Flags().StringVar(&f.options.Model.Name, "celln-fleet-model", "", "Model name the backend serves (required except for deepseek, which defaults to deepseek-chat)")
	cmd.Flags().StringVar(&f.options.Model.Endpoint, "celln-fleet-model-endpoint", "", "Full chat endpoint URL; defaults per provider (required for llama-server, e.g. http://HOST:8080/v1/chat/completions)")
	cmd.Flags().StringVar(&f.options.Model.Protocol, "celln-fleet-model-protocol", "", "openai-chat or anthropic-messages; defaults per provider")
	cmd.Flags().BoolVar(&f.options.Model.AllowInsecure, "celln-fleet-model-allow-insecure", false, "Approve a plain-HTTP or private model endpoint such as a LAN llama-server")
	cmd.Flags().Int64Var(&f.options.Limits.LeaseSeconds, "celln-fleet-max-lease-seconds", cellninstall.DefaultFleetLimits.LeaseSeconds, "Longest a parent may live (60–86400); the policy ceiling every run in the scope is admitted under")
	cmd.Flags().Int64Var(&f.options.Limits.MaxTurns, "celln-fleet-max-turns", cellninstall.DefaultFleetLimits.MaxTurns, "Most turns one parent may take (1–1024)")
	cmd.Flags().Int64Var(&f.options.Limits.MaxModelRequests, "celln-fleet-max-model-requests", cellninstall.DefaultFleetLimits.MaxModelRequests, fmt.Sprintf("Most model requests one parent may make over its life (%d–%d); every turn reserves %d, so size it as turns × %d", cellninstall.MinFleetModelRequests, cellninstall.MaxFleetModelRequests, sympoziumv1alpha1.TurnModelRequests, sympoziumv1alpha1.TurnModelRequests))
	cmd.Flags().Int64Var(&f.options.Limits.MaxOutputTokens, "celln-fleet-max-output-tokens", cellninstall.DefaultFleetLimits.MaxOutputTokens, fmt.Sprintf("Most model output tokens one parent may consume over its life (%d–%d); every turn reserves %d requests × the backend's max output tokens per request (%d by default, up to %d), so size it as turns × that for the most expensive backend. Left unset it is %d turns × the most expensive backend's turn", cellninstall.MinFleetOutputTokens, cellninstall.MaxFleetOutputTokens, sympoziumv1alpha1.TurnModelRequests, sympoziumv1alpha1.TurnOutputTokens, sympoziumv1alpha1.MaxTurnOutputTokens, cellninstall.DefaultFleetLimits.MaxTurns))
	f.outputTokensCeilingSet = func() bool { return cmd.Flags().Changed("celln-fleet-max-output-tokens") }
	cmd.Flags().Int64Var(&f.modelMaxOutputTokens, "celln-fleet-model-max-output-tokens", 0, fmt.Sprintf("Most output tokens one model request of the default backend may produce (%d–%d; default %d). %d suits chat with thinking disabled; a reasoning model left thinking needs 2048–4096, which costs 4–8× the tokens per turn. Needs a Celln newer than %s on the nodes and a starter package built by it; cannot change once the backend is published", cellninstall.MinModelMaxOutputTokens, cellninstall.MaxModelMaxOutputTokens, cellninstall.DefaultModelMaxOutputTokens, cellninstall.DefaultModelMaxOutputTokens, cellninstall.ModelMaxOutputTokensMinCelln))
	cmd.Flags().StringVar(&f.authorise, "celln-fleet-authorise", "all", "Which namespaces may run on the fleet: 'all' (every namespace except kube-*, cert-manager, the control-plane namespaces and namespaces labeled celln.sympozium.ai/excluded) or 'labeled' (only namespaces labeled celln.sympozium.ai/scope=<scope>)")
	cmd.Flags().StringVar(&f.options.ModelCredentialFile, "celln-fleet-model-credential-file", "", "Local file holding the default backend's provider credential to publish once as a Secret in celln-system (omit to keep an existing Secret; not needed for llama-server)")
	cmd.Flags().StringVar(&f.modelParametersFile, "celln-fleet-model-parameters-file", "", "Absolute path of a JSON object the Celln host merges into every provider request of the default backend, e.g. {\"chat_template_kwargs\":{\"enable_thinking\":false}} for a reasoning model on llama-server (needs a Celln newer than "+cellninstall.ModelParametersMinCelln+" on the nodes; cannot change once the backend is published)")
	cmd.Flags().StringArrayVar(&f.backendSpecs, "celln-fleet-backend", nil, "A model backend of this fleet, repeatable: name=NAME,provider=PROVIDER,model=MODEL[,endpoint=URL][,protocol=openai-chat|anthropic-messages][,credential-file=/path][,allow-insecure=true][,parameters-file=/abs/path.json][,max-output-tokens=N] (parameters-file: a JSON object the Celln host merges into every provider request of the backend; max-output-tokens: most output tokens per model request, 256–4096, default 512). Every node configures every backend and a namespace may run parents on any of them side by side. Without this flag the --celln-fleet-model-* flags define the single backend named native")
	cmd.Flags().BoolVar(&f.mediateBackends, "celln-mediate-backends", false, "With mediated model access (--set celln.mediation.enabled=true ...): also let an Agent use its own key, from a Secret in its namespace, for every HTTPS backend of this fleet. Plain-HTTP and port-bearing backends are never offered")
	cmd.Flags().StringArrayVar(&f.mediatedRouteSpecs, "celln-mediated-route", nil, "A provider route an Agent may bring its own key for, repeatable: provider=PROVIDER,protocol=openai-chat|anthropic-messages,origin=https://HOST,models=MODEL[+MODEL...] (origin and models take several values joined with +, or repeat the key). Matching is exact on provider, protocol, model and origin; there is no wildcard and none is declared by default. Needs mediated model access enabled in this install's values; routes are only ever added to a scope's policy")
	cmd.Flags().StringArrayVar(&f.options.HTTPSHosts, "celln-fleet-https-host", nil, "An exact host the https-fetch and https-post-json starter tools may reach, repeatable (lowercase DNS name; default example.com). Every backend's nodes configure the same list")
	cmd.Flags().BoolVar(&f.skipPreflight, "celln-fleet-skip-preflight", false, "Skip the one-token chat probe of every backend with its key (use when only the nodes can reach the endpoint)")
	cmd.Flags().BoolVar(&f.replacePackage, "celln-fleet-replace-package", false, "Approve moving an installed fleet to this package or scope (e.g. after upgrading to a sympozium release whose starter package inputs changed; most releases keep the package): nodes publish the new configuration, the scope's catalogue is replaced and every namespace's platform wrappers are rebound. Every live parent on the fleet is lost")
	cmd.Flags().StringVar(&f.outputDir, "celln-fleet-output-dir", "", "Absolute private directory for the materialized configuration and installation records (default ~/.sympozium/celln-fleet/<scope>)")
	cmd.Flags().DurationVar(&f.wait, "celln-fleet-wait", 15*time.Minute, "How long to wait for the first labeled node to publish the starter configuration")
}

// installCellnFleet runs the two-phase fleet installation: deploy the plane so
// labeled nodes prepare themselves, then bind the catalogue and controller to
// the configuration the nodes published. Every step refuses to replace state
// left by an earlier attempt, so a rerun after labeling more nodes is safe.
func installCellnFleet(ctx context.Context, f cellnFleetFlags, imageTag string, setValues []string, approve bool) error {
	if !approve && !f.defaulted {
		return fmt.Errorf("--celln-fleet requires --celln-native-approve-starter-tools: grants include %s", starterToolGrants)
	}
	// The declaration is checked, and refused with the reason, before
	// anything is written locally or in the cluster.
	mediationValues, err := f.mediationValues(setValues)
	if err != nil {
		return err
	}
	if err := f.applyBackendFlags(); err != nil {
		return err
	}
	if err := f.applyStarterDefaults(); err != nil {
		return err
	}
	if err := os.MkdirAll(f.outputDir, 0700); err != nil {
		return err
	}
	if err := materializeCredentials(f.options.Backends, nil, f.outputDir); err != nil {
		return err
	}
	resolved, err := f.options.ResolvedBackends()
	if err != nil {
		return err
	}
	sized, err := f.sizeLimits(resolved)
	if err != nil {
		return err
	}
	if sized != "" {
		fmt.Println("  " + sized)
	}
	fleetValues, err := cellninstall.FleetValues(f.options)
	if err != nil {
		return err
	}
	if err := cellninstall.ValidateBackendCredentialFiles(resolved); err != nil {
		return err
	}
	if !filepath.IsAbs(f.outputDir) || filepath.Clean(f.outputDir) != f.outputDir {
		return fmt.Errorf("--celln-fleet-output-dir must be a clean absolute directory")
	}
	if err := os.MkdirAll(f.outputDir, 0700); err != nil {
		return err
	}
	if err := initClient(); err != nil {
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
	// A published backend keeps the parameters it was configured with; asking
	// for others is refused rather than silently ignored.
	if err := cellninstall.CheckPublishedModelParameters(ctx, k8sClient, f.options); err != nil {
		return err
	}
	if notice := fleetPackageUnchangedNotice(publication, f.options.Scope, f.options.PackageHash); notice != "" {
		fmt.Println("  " + notice)
	}
	// Every backend answers a one-token chat request with its key before the
	// cluster changes, so a dead provider or bad key is reported here rather
	// than as a lost parent on the first run.
	if !f.skipPreflight {
		for _, b := range resolved {
			credential, err := cellninstall.PreflightCredential(ctx, k8sClient, b)
			if err != nil {
				return err
			}
			if err := cellninstall.PreflightBackend(ctx, nil, b, credential); err != nil {
				return err
			}
			fmt.Printf("  Backend %s answered a probe at %s\n", b.Name, b.Model.Endpoint)
		}
	}
	values := append(append(append([]string{}, setValues...), fleetValues...), mediationValues...)
	publishCredentials := func() error {
		for _, b := range resolved {
			if err := cellninstall.PublishFleetBackendCredential(ctx, k8sClient, b); err != nil {
				return fmt.Errorf("backend %s: %w", b.Name, err)
			}
		}
		return nil
	}
	// A rerun keeps the controller wired to the fleet through this upgrade and
	// publishes a new backend's key before the nodes configure it.
	if wired, err := cellninstall.ExistingFleetWiring(ctx, k8sClient, helmNamespace); err != nil {
		return err
	} else if wired {
		values = append(values, cellninstall.FleetWiringValues()...)
	}
	if rerun, err := cellninstall.FleetNamespaceExists(ctx, k8sClient); err != nil {
		return err
	} else if rerun {
		if err := publishCredentials(); err != nil {
			return err
		}
	}
	if replacing {
		fmt.Printf("  Moving the fleet from package %s to %s in scope %s; every live parent on the fleet is lost.\n", publication.Package, f.options.PackageHash, f.options.Scope)
		if err := cellninstall.ApproveFleetReplacement(ctx, k8sClient, publication, f.options.Scope, f.options.PackageHash); err != nil {
			return err
		}
	}
	if err := runInstall(imageTag, values); err != nil {
		return err
	}
	// The chart owns celln-system; the nodes and the router block on these
	// objects until they exist, so publishing after the install is safe.
	if err := publishCredentials(); err != nil {
		return err
	}
	if err := cellninstall.PrepareFleetTrust(ctx, k8sClient, f.options.Principal); err != nil {
		return err
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
				hint := ""
				for _, b := range resolved {
					if reason := cellninstall.HintForBackendModel(len(b.Model.Parameters) != 0, b.Model.MaxOutputTokens); reason != "" {
						hint = ". Backend " + b.Name + ": " + reason
						break
					}
				}
				return fmt.Errorf("%s did not happen within %s; label a KVM node (or give a Kind node a kernel), check the celln-node-configure logs in celln-system, then rerun this command%s", what, f.wait, hint)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
	}
	expected := make([]string, 0, len(resolved))
	for _, b := range resolved {
		expected = append(expected, b.Name)
	}
	published, err := cellninstall.PublishedBackendNames(ctx, k8sClient)
	if err != nil {
		return err
	}
	if err := wait("publishing the starter configuration for every backend", func() (bool, error) {
		return cellninstall.ReadFleetConfigurationFor(ctx, k8sClient, configuration, f.options.PackageHash, expected)
	}); err != nil {
		return err
	}
	if added := cellninstall.AddedBackends(published, expected); len(added) != 0 && len(published) != 0 {
		// The nodes publish a backend once its key reached their kubelet; the
		// running owners' mount of the same Secret follows within the
		// kubelet's sync period. Wait it out before offering the backend.
		fmt.Printf("  Backend(s) %s configured on every node; waiting %s for the key to reach the running owners...\n", strings.Join(added, ", "), cellninstall.FleetCredentialPropagationGrace)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cellninstall.FleetCredentialPropagationGrace):
		}
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
	// The mediation declaration is read back from the record the chart just
	// rendered, the one place the API server reads it from too, so both
	// install the same routes whoever runs next.
	mediation, err := cellninstall.ReadMediationRecord(ctx, k8sClient)
	if err != nil {
		return err
	}
	if len(mediationValues) != 0 && !mediation.Enabled {
		return fmt.Errorf("the release did not record the declared mediated routes (no ConfigMap celln-system/%s); nothing was added to the policy", cellninstall.MediationRecordConfigMap)
	}
	mediation.Apply(&platform)
	if err := cellninstall.InstallPlatform(ctx, k8sClient, platform); err != nil {
		return err
	}
	o := cellninstall.Options{Namespace: namespace, OutputDir: platform.OutputDir, OwnerTarget: cellninstall.ManagedRouterURL, Scope: f.options.Scope, ControllerNamespace: helmNamespace, PackageHash: f.options.PackageHash}
	wiring, err := cellninstall.ConfigureFleet(ctx, k8sClient, o)
	if err != nil {
		return err
	}
	if err := runInstall(imageTag, append(values, wiring...)); err != nil {
		return err
	}
	names := make([]string, 0, len(resolved))
	for _, b := range resolved {
		names = append(names, b.Name+" ("+b.Model.Provider+"/"+b.Model.Name+")")
	}
	if mediation.Enabled {
		fmt.Println("  " + mediationSummary(mediation))
	}
	fmt.Printf("  Model backends on this fleet: %s. Each backend is an AgentRuntime wrapper in every namespace (celln-<backend>; the default backend keeps celln-native).\n", strings.Join(names, ", "))
	if f.authorise == cellnplatform.AuthoriseLabeled {
		fmt.Printf("  Enabled enduring Celln runs on fleet %q for namespaces labeled %s=%s; %s is labeled and carries the wrapper objects. No run submitted.\n", f.options.Scope, cellninstall.ScopeLabel, f.options.Scope, namespace)
	} else {
		fmt.Printf("  Enabled enduring Celln runs on fleet %q for every namespace except the system exclusions and namespaces labeled %s; %s carries the wrapper objects and any other namespace gets them on first use. No run submitted.\n", f.options.Scope, cellnplatform.ExcludedLabel, namespace)
	}
	return nil
}

// sizeLimits settles the scope's ceilings for its backends. One policy bounds
// every backend, so the ceilings are sized and checked for the backend whose
// turns cost most; the returned line says when the default was scaled.
func (f *cellnFleetFlags) sizeLimits(resolved []cellninstall.FleetBackend) (string, error) {
	if f.outputTokensCeilingSet != nil && !f.outputTokensCeilingSet() {
		f.options.Limits.MaxOutputTokens = 0
	}
	limits, sized, err := f.options.Limits.ResolveFor(resolved)
	if err != nil {
		return "", err
	}
	f.options.Limits = limits
	return sized, nil
}

// applyBackendFlags turns the backend flags into the install's backends:
// every --celln-fleet-backend spec, then the single-backend settings that
// have no place in the --celln-fleet-model-* route itself.
func (f *cellnFleetFlags) applyBackendFlags() error {
	backends, err := parseFleetBackends(f.backendSpecs)
	if err != nil {
		return err
	}
	f.options.Backends = append(f.options.Backends, backends...)
	if f.modelParametersFile != "" {
		if len(f.backendSpecs) != 0 {
			return fmt.Errorf("--celln-fleet-model-parameters-file configures the single --celln-fleet-model-* backend; with --celln-fleet-backend give parameters-file=/abs/path.json in the backend's spec")
		}
		parameters, err := cellninstall.ReadModelParametersFile(f.modelParametersFile)
		if err != nil {
			return fmt.Errorf("--celln-fleet-model-parameters-file: %w", err)
		}
		if err := f.applyToDefaultBackend("--celln-fleet-model-parameters-file", func(m *cellninstall.FleetModel) { m.Parameters = parameters }); err != nil {
			return err
		}
	}
	if f.modelMaxOutputTokens != 0 {
		if len(f.backendSpecs) != 0 {
			return fmt.Errorf("--celln-fleet-model-max-output-tokens configures the single --celln-fleet-model-* backend; with --celln-fleet-backend give max-output-tokens=N in the backend's spec")
		}
		if err := cellninstall.ValidateModelMaxOutputTokens(f.modelMaxOutputTokens); err != nil {
			return fmt.Errorf("--celln-fleet-model-max-output-tokens: %w", err)
		}
		if err := f.applyToDefaultBackend("--celln-fleet-model-max-output-tokens", func(m *cellninstall.FleetModel) { m.MaxOutputTokens = f.modelMaxOutputTokens }); err != nil {
			return err
		}
	}
	return nil
}

// applyToDefaultBackend applies a single-backend flag to the default backend:
// the --celln-fleet-model-* route, or, when a bare install took its backends
// from the environment or a prompt, the one named native.
func (f *cellnFleetFlags) applyToDefaultBackend(flag string, apply func(*cellninstall.FleetModel)) error {
	if len(f.options.Backends) == 0 {
		apply(&f.options.Model)
		return nil
	}
	applied := false
	for i := range f.options.Backends {
		if f.options.Backends[i].Name == cellnplatform.DefaultBackend {
			apply(&f.options.Backends[i].Model)
			applied = true
		}
	}
	if !applied {
		return fmt.Errorf("%s: this install has no backend named %s", flag, cellnplatform.DefaultBackend)
	}
	return nil
}

// parseFleetBackends parses repeated --celln-fleet-backend values of the form
// key=value pairs separated by commas.
func parseFleetBackends(specs []string) ([]cellninstall.FleetBackend, error) {
	var out []cellninstall.FleetBackend
	for _, spec := range specs {
		var b cellninstall.FleetBackend
		for _, pair := range strings.Split(spec, ",") {
			key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok {
				return nil, fmt.Errorf("--celln-fleet-backend %q: expected key=value pairs", spec)
			}
			switch key {
			case "name":
				b.Name = value
			case "provider":
				b.Model.Provider = value
			case "model":
				b.Model.Name = value
			case "endpoint":
				b.Model.Endpoint = value
			case "protocol":
				b.Model.Protocol = value
			case "credential-file":
				b.CredentialFile = value
			case "credential-env":
				b.CredentialEnv = value
			case "allow-insecure":
				b.Model.AllowInsecure = value == "true" || value == "1" || value == "yes"
			case "parameters-file":
				// A file, because JSON contains the commas that separate pairs.
				parameters, err := cellninstall.ReadModelParametersFile(value)
				if err != nil {
					return nil, fmt.Errorf("--celln-fleet-backend %q: %w", spec, err)
				}
				b.Model.Parameters = parameters
			case "max-output-tokens":
				tokens, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("--celln-fleet-backend %q: max-output-tokens must be a whole number", spec)
				}
				if err := cellninstall.ValidateModelMaxOutputTokens(tokens); err != nil {
					return nil, fmt.Errorf("--celln-fleet-backend %q: %w", spec, err)
				}
				if tokens == 0 {
					return nil, fmt.Errorf("--celln-fleet-backend %q: max-output-tokens must be %d–%d (omit it for the default %d)", spec, cellninstall.MinModelMaxOutputTokens, cellninstall.MaxModelMaxOutputTokens, cellninstall.DefaultModelMaxOutputTokens)
				}
				b.Model.MaxOutputTokens = tokens
			default:
				return nil, fmt.Errorf("--celln-fleet-backend %q: unknown key %q (name, provider, model, endpoint, protocol, credential-file, credential-env, allow-insecure, parameters-file, max-output-tokens)", spec, key)
			}
		}
		if b.Name == "" {
			return nil, fmt.Errorf("--celln-fleet-backend %q: name is required", spec)
		}
		out = append(out, b)
	}
	return out, nil
}

// mediationValues validates the mediation flags and returns the chart values
// that record them. The flags declare routes only; they never switch mediation
// on, because that needs the bootstrapped trust and the gateway's inputs, so
// they are refused unless this install's values enable it.
func (f *cellnFleetFlags) mediationValues(setValues []string) ([]string, error) {
	routes, err := parseMediatedRoutes(f.mediatedRouteSpecs)
	if err != nil {
		return nil, err
	}
	values, err := cellninstall.MediationValues(f.mediateBackends, routes)
	if err != nil {
		return nil, fmt.Errorf("--celln-mediated-route: %w", err)
	}
	if len(values) == 0 {
		return nil, nil
	}
	helmValues, err := buildHelmValues("", setValues)
	if err != nil {
		return nil, err
	}
	celln, _ := helmValues["celln"].(map[string]interface{})
	mediation, _ := celln["mediation"].(map[string]interface{})
	if enabled, _ := mediation["enabled"].(bool); !enabled {
		return nil, fmt.Errorf("--celln-mediated-route and --celln-mediate-backends declare routes for mediated model access, which this install does not enable: pass the values from 'sympozium celln-mediation bootstrap' and the gateway's inputs with --set (celln.mediation.enabled=true, ...); see docs/guides/celln-mediated-model-access.md")
	}
	if set, declared := mediation["routes"]; declared && set != nil && len(f.mediatedRouteSpecs) != 0 {
		return nil, fmt.Errorf("declare mediated routes with --celln-mediated-route or with --set celln.mediation.routes, not both")
	}
	return values, nil
}

// parseMediatedRoutes parses repeated --celln-mediated-route values, in the
// key=value style of --celln-fleet-backend. origin and models take several
// values joined with "+" (a comma separates pairs), or the key repeated.
func parseMediatedRoutes(specs []string) ([]cellninstall.MediatedRoute, error) {
	var out []cellninstall.MediatedRoute
	for _, spec := range specs {
		var route cellninstall.MediatedRoute
		for _, pair := range strings.Split(spec, ",") {
			key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok {
				return nil, fmt.Errorf("--celln-mediated-route %q: expected key=value pairs", spec)
			}
			switch key {
			case "provider":
				route.Provider = value
			case "protocol":
				route.Protocol = value
			case "auth":
				route.Auth = value
			case "allowInsecure":
				if value != "true" && value != "false" {
					return nil, fmt.Errorf("--celln-mediated-route: allowInsecure must be true or false")
				}
				route.AllowInsecure = value == "true"
			case "origin", "origins":
				route.EndpointOrigins = append(route.EndpointOrigins, strings.Split(value, "+")...)
			case "model", "models":
				route.Models = append(route.Models, strings.Split(value, "+")...)
			default:
				return nil, fmt.Errorf("--celln-mediated-route %q: unknown key %q (provider, protocol, origin, models)", spec, key)
			}
		}
		if _, err := route.PolicyRoute(); err != nil {
			return nil, fmt.Errorf("--celln-mediated-route %q: %w", spec, err)
		}
		out = append(out, route)
	}
	return out, nil
}

// mediationSummary says what the scope's policy was offered for Agents' own keys.
func mediationSummary(record cellninstall.MediationRecord) string {
	if !record.MediateBackends && len(record.Routes) == 0 {
		return "Mediated model access is enabled, but no provider route is declared: an Agent with its own key is refused AUTH_ROUTE_MISMATCH until you declare one with --celln-mediated-route."
	}
	names := make([]string, 0, len(record.Routes)+1)
	if record.MediateBackends {
		names = append(names, "every HTTPS backend of this fleet")
	}
	for _, route := range record.Routes {
		names = append(names, fmt.Sprintf("%s/%s (%s) at %s", route.Provider, strings.Join(route.Models, "+"), route.Protocol, strings.Join(route.EndpointOrigins, "+")))
	}
	return "Agents may bring their own key for: " + strings.Join(names, "; ") + "."
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
