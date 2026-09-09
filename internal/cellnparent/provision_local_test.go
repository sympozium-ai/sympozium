package cellnparent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/zeebo/blake3"
	"k8s.io/apimachinery/pkg/types"
)

// Synthetic issuer responses test the process/registration boundary only.
// They are deliberately not evidence of Celln issuance or hardware execution.
func testLocalProvisioner(t *testing.T, ctx context.Context, loader cellnauthority.Loader, intent ProvisionIntent, template HostProvisionTemplate) {
	t.Helper()
	p := LocalProvisioner{Binary: filepath.Join(t.TempDir(), "issuer"), Root: t.TempDir(), Journal: t.TempDir(), Approvals: t.TempDir(), Target: "https://owner.example", TokenFile: "/operator/token"}
	incarnation, err := provisionIncarnation(template.Scope, string(intent.Selection.Run.UID))
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(localProvisionResult{APIVersion: "celln.parent-provisioned/v1", LaunchProfile: "blake3:" + strings.Repeat("a", 64), Incarnation: incarnation})
	script := "#!/bin/sh\ntest \"$1\" = --root || exit 1\ntest \"$3\" = parent-provision || exit 1\ntest -f \"$4\" || exit 1\ntest \"$(stat -c %a \"$4\")\" = 600 || exit 1\ntest -z \"$CELLN_PARENT_TEST_SECRET\" || exit 1\nprintf '%s' '" + string(response) + "'\n"
	if err := os.WriteFile(p.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CELLN_PARENT_TEST_SECRET", "must-not-inherit")
	first, err := p.ProvisionAndApprove(ctx, loader, intent, template)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.ProvisionAndApprove(ctx, loader, intent, template)
	if err != nil || first != second {
		t.Fatalf("retry changed approval: %v", err)
	}
	if first.Binding.Incarnation != incarnation || first.Binding.RunUID != string(intent.Selection.Run.UID) {
		t.Fatal("unbound result")
	}
	config := RegistrationConfig{APIVersion: "sympozium.ai/celln-parent-registrations-v1", Journal: p.Journal, Approvals: p.Approvals, OperatorSource: loader.OperatorSource, RuntimeSource: loader.RuntimeSource, AgentSource: loader.AgentSource, LocalProvisioner: &p, HostTemplates: []HostProvisionTemplate{template}}
	configPath := filepath.Join(t.TempDir(), "local-provisioner.json")
	writeConfig := func() {
		t.Helper()
		raw, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig()
	dispatcher, err := LoadRegistrationDispatcher(configPath, p.Approvals, loader.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: intent.Selection.Run.Namespace, Name: intent.Selection.Run.Name}
	if err := dispatcher.Admit(ctx, key); err != nil {
		t.Fatal(err)
	}
	config.HostTemplates = append(config.HostTemplates, template)
	writeConfig()
	if err := dispatcher.Admit(ctx, key); err == nil {
		t.Fatal("ambiguous host templates accepted")
	}
	config.HostTemplates = nil
	writeConfig()
	if err := dispatcher.Admit(ctx, key); err == nil {
		t.Fatal("withdrawn host template accepted")
	}
	if _, err := os.Stat(filepath.Join(p.Approvals, approvalFileName(first.Binding.RunUID))); err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.Root = t.TempDir()
	if _, err := changed.ProvisionAndApprove(ctx, loader, intent, template); err == nil {
		t.Fatal("retry switched host root")
	}
	changed = p
	changed.Target = "https://another-owner.example"
	if _, err := changed.ProvisionAndApprove(ctx, loader, intent, template); err == nil {
		t.Fatal("retry switched owner")
	}
	template.Native.AdmissionWindowMs++
	if _, err := p.ProvisionAndApprove(ctx, loader, intent, template); err == nil {
		t.Fatal("retry switched plan")
	}
	entries, err := os.ReadDir(p.Journal)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".parent-") {
			t.Fatal("temporary plan leaked")
		}
	}
}

func TestLocalProvisionInvocationRefusesMalformedAndFailedResults(t *testing.T) {
	valid := `{"apiVersion":"celln.parent-provisioned/v1","launchProfile":"blake3:` + strings.Repeat("a", 64) + `","incarnation":"blake3:` + strings.Repeat("b", 64) + `"}`
	for name, output := range map[string]string{"invalid": "{}", "trailing": valid + " {}", "unknown": strings.TrimSuffix(valid, "}") + `,"extra":true}`, "oversize": strings.Repeat("x", 4097)} {
		t.Run(name, func(t *testing.T) {
			p := LocalProvisioner{Binary: filepath.Join(t.TempDir(), "issuer"), Root: t.TempDir(), Journal: t.TempDir()}
			if err := os.WriteFile(p.Binary, []byte("#!/bin/sh\nprintf '%s' '"+output+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := p.invoke(context.Background(), []byte(`{}`), "tenant"); err == nil {
				t.Fatal("invalid issuer result accepted")
			}
			entries, _ := os.ReadDir(p.Journal)
			if len(entries) != 0 {
				t.Fatal("staging file leaked")
			}
		})
	}
}

func TestProvisionIncarnationMatchesRustJSONTuple(t *testing.T) {
	for _, scope := range []string{"cluster", `quotes"and\slashes`, "cluster<>&", "cluster\u2028east"} {
		got, err := provisionIncarnation(scope, "run-uid")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal([]string{"celln.parent-run-incarnation/v1", scope, "run-uid"})
		// Rust leaves these Unicode/HTML characters literal.
		raw = []byte(strings.NewReplacer(`\u003c`, "<", `\u003e`, ">", `\u0026`, "&", `\u2028`, "\u2028").Replace(string(raw)))
		sum := blake3.Sum256(raw)
		if got != "blake3:"+hex.EncodeToString(sum[:]) {
			t.Fatal("cross-language identity drift")
		}
	}
	if _, err := provisionIncarnation("cluster\n", "uid"); err == nil {
		t.Fatal("control identity accepted")
	}
}
