package cellnparent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RegistrationConfig is operator-only configuration. Grant ConfigMaps and both
// durable directories must be protected from tenant writes. All replicas sharing
// registrations must share the same journal; changing it is not pool renewal.
type RegistrationConfig struct {
	APIVersion       string                     `json:"apiVersion"`
	Journal          string                     `json:"journal"`
	Approvals        string                     `json:"approvals"`
	OperatorSource   types.NamespacedName       `json:"operatorSource"`
	RuntimeSource    types.NamespacedName       `json:"runtimeSource"`
	AgentSource      types.NamespacedName       `json:"agentSource"`
	Registrations    []ParentLaunchRegistration `json:"registrations"`
	LocalProvisioner *LocalProvisioner          `json:"localProvisioner,omitempty"`
	HostTemplates    []HostProvisionTemplate    `json:"hostTemplates,omitempty"`
}

type RegistrationDispatcher struct {
	path   string
	config RegistrationConfig
	reader client.Reader
}

func readRegistrationConfig(path string) (RegistrationConfig, error) {
	var config RegistrationConfig
	raw, err := boundedFile(path, 1<<20)
	if err != nil {
		return config, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&config) != nil || d.Decode(new(any)) != io.EOF || config.APIVersion != "sympozium.ai/celln-parent-registrations-v1" || len(config.Registrations) > 1024 {
		return config, fmt.Errorf("invalid prepared parent configuration")
	}
	if len(config.HostTemplates) > 1024 || (config.LocalProvisioner == nil && len(config.HostTemplates) > 0) || (config.LocalProvisioner != nil && (len(config.Registrations) > 0 || config.LocalProvisioner.Journal != config.Journal || config.LocalProvisioner.Approvals != config.Approvals)) {
		return config, fmt.Errorf("exclusive local provisioning mode and matching durable directories required")
	}
	refs := []types.NamespacedName{config.OperatorSource, config.RuntimeSource, config.AgentSource}
	seen := map[types.NamespacedName]bool{}
	for _, ref := range refs {
		if ref.Namespace == "" || ref.Name == "" || seen[ref] {
			return config, fmt.Errorf("three distinct explicit parent grant sources required")
		}
		seen[ref] = true
	}
	for _, path := range []string{config.Journal, config.Approvals} {
		info, err := os.Stat(path)
		if !filepath.IsAbs(path) || err != nil || !info.IsDir() {
			return config, fmt.Errorf("existing absolute parent journal and approval directories required")
		}
	}
	return config, nil
}

func LoadRegistrationDispatcher(path, approvals string, reader client.Reader) (*RegistrationDispatcher, error) {
	config, err := readRegistrationConfig(path)
	if err != nil {
		return nil, err
	}
	if reader == nil || config.Approvals != approvals {
		return nil, fmt.Errorf("uncached reader and matching parent approval directory required")
	}
	return &RegistrationDispatcher{path: path, config: config, reader: reader}, nil
}

// Admit only publishes approval for an unbound run. A saved parent is never
// replaced or reapproved by this path. Registrations are reread on every new
// admission so withdrawals are observed; routing changes require a restart.
func (d *RegistrationDispatcher) Admit(ctx context.Context, key types.NamespacedName) error {
	var run api.AgentRun
	if err := d.reader.Get(ctx, key, &run); err != nil {
		return err
	}
	if run.Status.CellnParent != nil {
		return nil
	}
	config, err := readRegistrationConfig(d.path)
	if err != nil {
		return err
	}
	old := d.config
	if config.Journal != old.Journal || config.Approvals != old.Approvals || config.OperatorSource != old.OperatorSource || config.RuntimeSource != old.RuntimeSource || config.AgentSource != old.AgentSource || !reflect.DeepEqual(config.LocalProvisioner, old.LocalProvisioner) {
		return fmt.Errorf("parent admission routing changed; preserve original journal")
	}
	loader := cellnauthority.Loader{Reader: d.reader, OperatorSource: config.OperatorSource, RuntimeSource: config.RuntimeSource, AgentSource: config.AgentSource}
	if config.LocalProvisioner != nil {
		intent, err := PrepareProvisionIntent(ctx, loader, key)
		if err != nil {
			return err
		}
		digest, err := ParentSelectionDigest(intent.Selection.Snapshot)
		if err != nil {
			return err
		}
		var selected *HostProvisionTemplate
		for i := range config.HostTemplates {
			candidate := &config.HostTemplates[i]
			var harness struct {
				System string `json:"system"`
			}
			if json.Unmarshal(candidate.Native.Template, &harness) != nil {
				return fmt.Errorf("invalid operator native template")
			}
			if candidate.SelectionSHA256 != digest || !reflect.DeepEqual(candidate.Model, intent.Spec.Model) || harness.System != intent.Spec.SystemPrompt {
				continue
			}
			if selected != nil {
				return fmt.Errorf("ambiguous operator parent host templates")
			}
			selected = candidate
		}
		if selected == nil {
			return fmt.Errorf("no matching operator parent host template")
		}
		_, err = config.LocalProvisioner.ProvisionAndApprove(ctx, loader, *intent, *selected)
		return err
	}
	_, err = SelectRegisteredParent(ctx, loader, key, config.Registrations, config.Journal, config.Approvals)
	return err
}
