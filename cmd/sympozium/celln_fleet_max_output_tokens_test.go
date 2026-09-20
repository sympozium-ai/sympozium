package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

func TestFleetBackendSpecParsesMaxOutputTokens(t *testing.T) {
	backends, err := parseFleetBackends([]string{
		"name=native,provider=deepseek",
		"name=local,provider=llama-server,model=qwq.gguf,endpoint=http://10.0.0.5:8080,allow-insecure=true,max-output-tokens=2048",
		"name=stated,provider=deepseek,max-output-tokens=512",
	})
	if err != nil {
		t.Fatal(err)
	}
	if backends[0].Model.MaxOutputTokens != 0 || backends[1].Model.MaxOutputTokens != 2048 || backends[2].Model.MaxOutputTokens != 512 {
		t.Fatalf("parsed: %+v", backends)
	}
	for value, refusal := range map[string]string{
		"255":  "must be 256–4096 (omit it for the default 512)",
		"4097": "must be 256–4096",
		"0":    "must be 256–4096",
		"-5":   "must be 256–4096",
		"2k":   "must be a whole number",
		"1.5":  "must be a whole number",
		"":     "must be a whole number",
	} {
		if _, err := parseFleetBackends([]string{"name=local,provider=deepseek,max-output-tokens=" + value}); err == nil || !strings.Contains(err.Error(), refusal) {
			t.Fatalf("%q: want %q, got %v", value, refusal, err)
		}
	}
	if _, err := parseFleetBackends([]string{"name=local,provider=deepseek,max-tokens=2048"}); err == nil || !strings.Contains(err.Error(), "max-output-tokens") {
		t.Fatalf("the unknown-key message must name the key: %v", err)
	}
}

// fleetFlagsFrom parses installer arguments the way `sympozium install` does.
func fleetFlagsFrom(t *testing.T, args ...string) *cellnFleetFlags {
	t.Helper()
	var f cellnFleetFlags
	cmd := &cobra.Command{Use: "install", RunE: func(*cobra.Command, []string) error { return nil }}
	f.register(cmd)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return &f
}

func TestFleetModelMaxOutputTokensFlag(t *testing.T) {
	f := fleetFlagsFrom(t, "--celln-fleet-model-provider", "llama-server", "--celln-fleet-model-endpoint", "http://10.0.0.5:8080", "--celln-fleet-model", "qwq.gguf", "--celln-fleet-model-allow-insecure", "--celln-fleet-model-max-output-tokens", "4096")
	if err := f.applyBackendFlags(); err != nil || f.options.Model.MaxOutputTokens != 4096 {
		t.Fatalf("single backend: %+v %v", f.options.Model, err)
	}
	// A bare install's backends came from the environment: the flag applies
	// to the default one.
	f = fleetFlagsFrom(t, "--celln-fleet-model-max-output-tokens", "1024")
	f.options.Backends = []cellninstall.FleetBackend{{Name: "other", Model: cellninstall.FleetModel{Provider: "openai"}}, {Name: "native", Model: cellninstall.FleetModel{Provider: "deepseek"}}}
	if err := f.applyBackendFlags(); err != nil || f.options.Backends[1].Model.MaxOutputTokens != 1024 || f.options.Backends[0].Model.MaxOutputTokens != 0 {
		t.Fatalf("default backend: %+v %v", f.options.Backends, err)
	}
	f.options.Backends = f.options.Backends[:1]
	if err := f.applyBackendFlags(); err == nil || !strings.Contains(err.Error(), "no backend named native") {
		t.Fatalf("no default backend: %v", err)
	}
	f = fleetFlagsFrom(t, "--celln-fleet-model-max-output-tokens", "100")
	if err := f.applyBackendFlags(); err == nil || !strings.Contains(err.Error(), "--celln-fleet-model-max-output-tokens: max output tokens per request must be 256–4096") {
		t.Fatalf("out of range: %v", err)
	}
	f = fleetFlagsFrom(t, "--celln-fleet-model-max-output-tokens", "2048", "--celln-fleet-backend", "name=native,provider=deepseek")
	if err := f.applyBackendFlags(); err == nil || !strings.Contains(err.Error(), "give max-output-tokens=N in the backend's spec") {
		t.Fatalf("mixed forms: %v", err)
	}
}

// The scope's one set of ceilings is sized for the most expensive backend
// unless the operator chose the lifetime output tokens, which must then pay
// for one of its turns.
func TestFleetInstallSizesCeilingsForTheMostExpensiveBackend(t *testing.T) {
	const backends = "name=native,provider=deepseek"
	const thinker = "name=thinker,provider=llama-server,model=qwq.gguf,endpoint=http://10.0.0.5:8080,allow-insecure=true,max-output-tokens=4096"
	size := func(args ...string) (*cellnFleetFlags, string, error) {
		t.Helper()
		f := fleetFlagsFrom(t, args...)
		if err := f.applyBackendFlags(); err != nil {
			t.Fatal(err)
		}
		resolved, err := f.options.ResolvedBackends()
		if err != nil {
			t.Fatal(err)
		}
		note, err := f.sizeLimits(resolved)
		return f, note, err
	}
	f, note, err := size("--celln-fleet-backend", backends)
	if err != nil || note != "" || f.options.Limits != cellninstall.DefaultFleetLimits {
		t.Fatalf("default backends: %+v %q %v", f.options.Limits, note, err)
	}
	f, note, err = size("--celln-fleet-backend", backends, "--celln-fleet-backend", thinker)
	if err != nil || f.options.Limits.MaxOutputTokens != 256*6*4096 || f.options.Limits.MaxModelRequests != 1536 || f.options.Limits.MaxTurns != 256 {
		t.Fatalf("scaled: %+v %v", f.options.Limits, err)
	}
	for _, want := range []string{"sized to 6291456", "256 turns × 24576 tokens", "backend thinker allows 4096 output tokens per request"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note lacks %q: %s", want, note)
		}
	}
	// The flag's own default, passed explicitly, is the operator's choice.
	f, note, err = size("--celln-fleet-backend", backends, "--celln-fleet-backend", thinker, "--celln-fleet-max-output-tokens", "786432")
	if err != nil || note != "" || f.options.Limits.MaxOutputTokens != 786432 {
		t.Fatalf("explicit: %+v %q %v", f.options.Limits, note, err)
	}
	_, _, err = size("--celln-fleet-backend", backends, "--celln-fleet-backend", thinker, "--celln-fleet-max-output-tokens", "20000")
	if err == nil || !strings.Contains(err.Error(), "do not pay for one turn of backend thinker, which reserves 24576") {
		t.Fatalf("below one turn: %v", err)
	}
	if _, _, err = size("--celln-fleet-backend", backends, "--celln-fleet-max-output-tokens", "25165824", "--celln-fleet-max-turns", "1024", "--celln-fleet-max-model-requests", "6144"); err != nil {
		t.Fatalf("the new maximum refused: %v", err)
	}
	if _, _, err = size("--celln-fleet-backend", backends, "--celln-fleet-max-output-tokens", "25165825"); err == nil || !strings.Contains(err.Error(), "output tokens 3072–25165824") {
		t.Fatalf("above the maximum: %v", err)
	}
	// The values the chart gets carry the scaled ceiling.
	f, _, _ = size("--celln-fleet-backend", backends, "--celln-fleet-backend", thinker)
	f.options.Scope, f.options.Principal, f.options.Publisher = "starter", "sympozium:celln", "ed25519:operator"
	f.options.PackageImage, f.options.PackageHash = "registry.example/celln/starter@sha256:"+strings.Repeat("a", 64), "blake3:"+strings.Repeat("b", 64)
	values, err := cellninstall.FleetValues(f.options)
	if joined := strings.Join(values, "\n"); err != nil || !strings.Contains(joined, "celln.fleet.limits.maxOutputTokens=6291456") || !strings.Contains(joined, "celln.fleet.backends[1].maxOutputTokens=4096") {
		t.Fatalf("values: %v %s", err, joined)
	}
}
