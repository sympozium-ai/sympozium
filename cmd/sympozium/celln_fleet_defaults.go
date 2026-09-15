package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
)

// The one-liner: a build that pins a starter package turns a bare
// `sympozium install` into a fleet install. The package identity comes from
// the build, the model backend from the environment or a prompt, and every
// node the probe finds KVM on joins by itself.

const defaultFleetScope = "starter"

// The starter tools the default install lends, stated so approving them by
// running the default is an informed act rather than a silent one.
const starterToolGrants = "run-owned files (read, write, list, append, search, delete) and bounded HTTPS (GET and JSON POST) to the --celln-fleet-https-host list, default example.com"

func defaultFleetOutputDir(scope string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("--celln-fleet-output-dir is required when the home directory is unknown: %w", err)
	}
	return filepath.Join(home, ".sympozium", "celln-fleet", scope), nil
}

// applyStarterDefaults fills the package identity, scope and output directory
// from this build's pinned starter package when the flags omit them.
func (f *cellnFleetFlags) applyStarterDefaults() error {
	if f.options.PackageImage == "" && f.options.PackageHash == "" && f.options.Publisher == "" {
		d := defaultCellnStarter()
		if !d.complete() {
			return fmt.Errorf("--celln-fleet needs --celln-fleet-package-image, --celln-fleet-package-hash and --celln-fleet-publisher: this build carries no default starter package")
		}
		f.options.PackageImage, f.options.PackageHash, f.options.Publisher = d.Image, d.PackageHash, d.Publisher
		fmt.Printf("  Using the default Celln starter package (Celln %s) at %s\n", d.CellnVersion, d.Image)
	}
	if f.options.Scope == "" {
		f.options.Scope = defaultFleetScope
	}
	if f.outputDir == "" {
		dir, err := defaultFleetOutputDir(f.options.Scope)
		if err != nil {
			return err
		}
		f.outputDir = dir
	}
	return nil
}

// providerDefault is a keyed provider the environment can select by its key.
type providerDefault struct {
	env, provider, model string
}

var keyedProviders = []providerDefault{
	{env: "DEEPSEEK_API_KEY", provider: cellninstall.ModelProviderDeepSeek},
	{env: "OPENAI_API_KEY", provider: cellninstall.ModelProviderOpenAI, model: "gpt-4o-mini"},
	{env: "ANTHROPIC_API_KEY", provider: cellninstall.ModelProviderAnthropic, model: "claude-sonnet-5"},
}

// backendsFromEnvironment finds a model backend without flags: an explicit
// SYMPOZIUM_CELLN_BACKEND (the --celln-fleet-backend syntax) or the first
// provider key present. The first backend found is the fleet's default one.
func backendsFromEnvironment() ([]cellninstall.FleetBackend, string, error) {
	if spec := strings.TrimSpace(os.Getenv("SYMPOZIUM_CELLN_BACKEND")); spec != "" {
		backends, err := parseFleetBackends([]string{spec})
		if err != nil {
			return nil, "", fmt.Errorf("SYMPOZIUM_CELLN_BACKEND: %w", err)
		}
		return backends, "SYMPOZIUM_CELLN_BACKEND", nil
	}
	for _, p := range keyedProviders {
		if strings.TrimSpace(os.Getenv(p.env)) == "" {
			continue
		}
		return []cellninstall.FleetBackend{{Name: cellnplatform.DefaultBackend, Model: cellninstall.FleetModel{Provider: p.provider, Name: p.model}, CredentialEnv: p.env}}, p.env, nil
	}
	return nil, "", nil
}

// promptFleetBackend asks an interactive operator for one backend. An empty
// answer, or "skip", installs without the fleet.
func promptFleetBackend(reader *bufio.Reader) (*cellninstall.FleetBackend, string, error) {
	fmt.Println("\n  Celln agents run against a model backend whose key stays on the fleet nodes.")
	fmt.Println("  Set DEEPSEEK_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY or SYMPOZIUM_CELLN_BACKEND to skip this prompt.")
	provider := strings.ToLower(prompt(reader, "  Model provider (deepseek, openai, anthropic, llama-server, or skip)", cellninstall.ModelProviderDeepSeek))
	switch provider {
	case "", "skip", "none", "no":
		return nil, "", nil
	case cellninstall.ModelProviderLlamaServer:
		endpoint := prompt(reader, "  llama-server chat endpoint (e.g. http://HOST:8080/v1/chat/completions)", "")
		model := prompt(reader, "  Model name the server reports", "")
		if endpoint == "" || model == "" {
			return nil, "", fmt.Errorf("llama-server needs an endpoint and a model name")
		}
		insecure := strings.HasPrefix(strings.ToLower(endpoint), "http://")
		if insecure {
			fmt.Println("  Plain HTTP: approved as a private-network endpoint.")
		}
		return &cellninstall.FleetBackend{Name: cellnplatform.DefaultBackend, Model: cellninstall.FleetModel{Provider: provider, Name: model, Endpoint: endpoint, AllowInsecure: insecure}}, "", nil
	case cellninstall.ModelProviderDeepSeek, cellninstall.ModelProviderOpenAI, cellninstall.ModelProviderAnthropic:
		key := promptSecret(reader, "  API key")
		if key == "" {
			return nil, "", nil
		}
		model := ""
		for _, p := range keyedProviders {
			if p.provider == provider {
				model = p.model
			}
		}
		if model != "" {
			model = prompt(reader, "  Model", model)
		}
		return &cellninstall.FleetBackend{Name: cellnplatform.DefaultBackend, Model: cellninstall.FleetModel{Provider: provider, Name: model}}, key, nil
	default:
		return nil, "", fmt.Errorf("unknown provider %q; use --celln-fleet-backend for other providers", provider)
	}
}

// materializeCredentials writes every backend key taken from the environment
// or a prompt to a private file under the fleet's output directory, which is
// where a flag-supplied credential file would otherwise be read from.
func materializeCredentials(backends []cellninstall.FleetBackend, prompted map[string]string, dir string) error {
	credentials := filepath.Join(dir, "credentials")
	for i := range backends {
		b := &backends[i]
		key := prompted[b.Name]
		if b.CredentialEnv != "" {
			key = strings.TrimSpace(os.Getenv(b.CredentialEnv))
			if key == "" {
				return fmt.Errorf("backend %s: %s is empty", b.Name, b.CredentialEnv)
			}
		}
		if key == "" {
			continue
		}
		if err := os.MkdirAll(credentials, 0700); err != nil {
			return err
		}
		path := filepath.Join(credentials, b.Name)
		if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
			return err
		}
		b.CredentialFile, b.CredentialEnv = path, ""
	}
	return nil
}

func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// enableByDefault decides whether a bare `sympozium install` runs the fleet:
// only when this build pins a starter package and a model backend is known
// (environment) or given (prompt). It reports what it chose and why.
func (f *cellnFleetFlags) enableByDefault(reader *bufio.Reader) (bool, error) {
	if !defaultCellnStarter().complete() {
		return false, nil
	}
	backends, source, err := backendsFromEnvironment()
	if err != nil {
		return false, err
	}
	prompted := map[string]string{}
	if len(backends) == 0 && stdinIsTerminal() {
		b, key, err := promptFleetBackend(reader)
		if err != nil {
			return false, err
		}
		if b != nil {
			backends = []cellninstall.FleetBackend{*b}
			if key != "" {
				prompted[b.Name] = key
			}
			source = "the prompt"
		}
	}
	if len(backends) == 0 {
		fmt.Println("\n  Celln parents not enabled: no model backend was given. Set DEEPSEEK_API_KEY, OPENAI_API_KEY,")
		fmt.Println("  ANTHROPIC_API_KEY or SYMPOZIUM_CELLN_BACKEND=name=native,provider=…,model=…,endpoint=… and rerun")
		fmt.Println("  sympozium install; the fleet then serves every namespace from nodes with KVM.")
		return false, nil
	}
	if err := f.applyStarterDefaults(); err != nil {
		return false, err
	}
	if err := os.MkdirAll(f.outputDir, 0700); err != nil {
		return false, err
	}
	if err := materializeCredentials(backends, prompted, f.outputDir); err != nil {
		return false, err
	}
	f.options.Backends = append(f.options.Backends, backends...)
	f.defaulted = true
	fmt.Printf("  Celln fleet: scope %q, backend %s (%s) from %s, state under %s.\n", f.options.Scope, backends[0].Name, backends[0].Model.Provider, source, f.outputDir)
	fmt.Printf("  The starter tools are approved by this default: %s.\n", starterToolGrants)
	return true, nil
}
