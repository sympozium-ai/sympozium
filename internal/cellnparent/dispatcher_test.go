package cellnparent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestParentRegistrationConfigFailsClosed(t *testing.T) {
	for _, mode := range []string{"valid-empty", "unknown", "trailing", "version", "same-source", "missing-source", "missing-directory", "relative-directory", "approval-mismatch", "nil-reader", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			config := RegistrationConfig{APIVersion: "sympozium.ai/celln-parent-registrations-v1", Journal: t.TempDir(), Approvals: t.TempDir(), OperatorSource: types.NamespacedName{Namespace: "operator", Name: "operator"}, RuntimeSource: types.NamespacedName{Namespace: "operator", Name: "runtime"}, AgentSource: types.NamespacedName{Namespace: "operator", Name: "agent"}}
			switch mode {
			case "version":
				config.APIVersion = "unversioned"
			case "same-source":
				config.AgentSource = config.RuntimeSource
			case "missing-source":
				config.AgentSource.Name = ""
			case "missing-directory":
				config.Journal = filepath.Join(config.Journal, "missing")
			case "relative-directory":
				config.Journal = "."
			}
			raw, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unknown":
				raw = append([]byte(`{"unexpected":true,`), raw[1:]...)
			case "trailing":
				raw = append(raw, []byte(` {}`)...)
			case "oversized":
				raw = make([]byte, (1<<20)+1)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			approvals := config.Approvals
			if mode == "approval-mismatch" {
				approvals = t.TempDir()
			}
			reader := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()
			if mode == "nil-reader" {
				reader = nil
			}
			_, err = LoadRegistrationDispatcher(path, approvals, reader)
			if (err == nil) != (mode == "valid-empty") {
				t.Fatalf("unexpected configuration result: %v", err)
			}
		})
	}
}
