package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

func TestBackendsFromEnvironmentPrefersTheExplicitSpecThenTheFirstKey(t *testing.T) {
	for _, env := range []string{"SYMPOZIUM_CELLN_BACKEND", "DEEPSEEK_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(env, "")
	}
	if backends, _, err := backendsFromEnvironment(); err != nil || len(backends) != 0 {
		t.Fatalf("empty environment must find nothing: %v %v", backends, err)
	}
	t.Setenv("OPENAI_API_KEY", "sk-"+strings.Repeat("x", 40))
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-"+strings.Repeat("y", 40))
	backends, source, err := backendsFromEnvironment()
	if err != nil || len(backends) != 1 || backends[0].Name != "native" || backends[0].Model.Provider != "openai" || backends[0].Model.Name != "gpt-4o-mini" || backends[0].CredentialEnv != "OPENAI_API_KEY" || source != "OPENAI_API_KEY" {
		t.Fatalf("first provider key must become the default backend: %+v %q %v", backends, source, err)
	}
	t.Setenv("SYMPOZIUM_CELLN_BACKEND", "name=native,provider=llama-server,model=q.gguf,endpoint=http://10.0.0.5:8080/v1/chat/completions,allow-insecure=true")
	backends, source, err = backendsFromEnvironment()
	if err != nil || len(backends) != 1 || backends[0].Model.Provider != "llama-server" || !backends[0].Model.AllowInsecure || source != "SYMPOZIUM_CELLN_BACKEND" {
		t.Fatalf("the explicit spec wins over provider keys: %+v %q %v", backends, source, err)
	}
	t.Setenv("SYMPOZIUM_CELLN_BACKEND", "provider=openai")
	if _, _, err := backendsFromEnvironment(); err == nil {
		t.Fatal("a spec without a name must be refused")
	}
}

func TestMaterializeCredentialsWritesPrivateFilesAndClearsTheEnvReference(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENAI_API_KEY", "sk-"+strings.Repeat("x", 40))
	t.Setenv("EMPTY_KEY", "")
	backends := []cellninstall.FleetBackend{
		{Name: "native", CredentialEnv: "OPENAI_API_KEY"},
		{Name: "local", Model: cellninstall.FleetModel{Provider: "llama-server"}},
		{Name: "typed"},
	}
	if err := materializeCredentials(backends, map[string]string{"typed": "sk-" + strings.Repeat("z", 40)}, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"native", "typed"} {
		path := filepath.Join(dir, "credentials", name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s: private credential file expected: %v %v", name, info, err)
		}
	}
	if backends[0].CredentialFile != filepath.Join(dir, "credentials", "native") || backends[0].CredentialEnv != "" {
		t.Fatalf("env reference must become a file: %+v", backends[0])
	}
	if backends[1].CredentialFile != "" {
		t.Fatalf("a keyless backend gets no file: %+v", backends[1])
	}
	if err := materializeCredentials([]cellninstall.FleetBackend{{Name: "x", CredentialEnv: "EMPTY_KEY"}}, nil, dir); err == nil {
		t.Fatal("an empty key variable must be refused")
	}
}

func TestPromptFleetBackendSkipsAndReadsAKey(t *testing.T) {
	if b, key, err := promptFleetBackend(bufio.NewReader(strings.NewReader("skip\n"))); err != nil || b != nil || key != "" {
		t.Fatalf("skip must install without the fleet: %+v %q %v", b, key, err)
	}
	b, key, err := promptFleetBackend(bufio.NewReader(strings.NewReader("anthropic\nsk-ant-secret\n\n")))
	if err != nil || b == nil || b.Name != "native" || b.Model.Provider != "anthropic" || b.Model.Name != "claude-sonnet-5" || key != "sk-ant-secret" {
		t.Fatalf("a keyed provider yields the default backend and its key: %+v %q %v", b, key, err)
	}
	b, key, err = promptFleetBackend(bufio.NewReader(strings.NewReader("llama-server\nhttp://10.0.0.5:8080/v1/chat/completions\nq.gguf\n")))
	if err != nil || b == nil || !b.Model.AllowInsecure || b.Model.Endpoint == "" || key != "" {
		t.Fatalf("llama-server over plain HTTP is approved as private: %+v %q %v", b, key, err)
	}
	if _, _, err := promptFleetBackend(bufio.NewReader(strings.NewReader("bedrock\n"))); err == nil {
		t.Fatal("an unknown provider must be refused with the flag to use")
	}
}

func TestDefaultStarterIsEmptyInADevelopmentBuild(t *testing.T) {
	if defaultCellnStarter().complete() {
		t.Fatal("a development build must not pin a package; the release workflow sets the linker flags")
	}
	if !(cellnStarter{Image: "r/p@sha256:x", PackageHash: "blake3:y", Publisher: "k"}).complete() || (cellnStarter{Image: "r/p@sha256:x", PackageHash: "blake3:y", Publisher: "k v"}).complete() {
		t.Fatal("a package identity is complete only with all three fields and no separators")
	}
	if got := cellnStarterLDFlags(cellnStarter{Image: "i", PackageHash: "h", Publisher: "p", CellnVersion: "v"}); got != "-X main.cellnStarterImage=i -X main.cellnStarterHash=h -X main.cellnStarterPublisher=p -X main.cellnStarterCellnVersion=v" {
		t.Fatalf("ldflags: %q", got)
	}
	if err := (&cellnFleetFlags{}).applyStarterDefaults(); err == nil {
		t.Fatal("without a pinned package the explicit inputs are required")
	}
	f := &cellnFleetFlags{options: cellninstall.FleetOptions{PackageImage: "r/p@sha256:" + strings.Repeat("a", 64), PackageHash: "blake3:" + strings.Repeat("b", 64), Publisher: "k"}}
	if err := f.applyStarterDefaults(); err != nil || f.options.Scope != defaultFleetScope || !strings.HasSuffix(f.outputDir, filepath.Join(".sympozium", "celln-fleet", defaultFleetScope)) {
		t.Fatalf("scope and output directory default: %+v %q %v", f.options, f.outputDir, err)
	}
}
