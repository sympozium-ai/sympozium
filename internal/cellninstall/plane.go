package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	"github.com/zeebo/blake3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PlaneOptions connects an already reviewed installation to the managed plane.
// The owner credential is explicitly supplied; model credentials are never read.
type PlaneOptions struct {
	NodeName, OwnerTokenFile string
}

const ManagedRouterURL = "http://celln-router.celln-system.svc.cluster.local:8787"

// ConfigurePlane publishes immutable installation configuration, never run
// authority. Existing Secrets are refused rather than silently replacing a live
// owner's identity. Call only after Install, then apply the returned Helm values.
func ConfigurePlane(ctx context.Context, store client.Client, o Options, p PlaneOptions) ([]string, error) {
	if len(validation.IsDNS1123Subdomain(p.NodeName)) != 0 || !filepath.IsAbs(p.OwnerTokenFile) || o.ControllerNamespace == "" || o.OwnerTarget != ManagedRouterURL {
		return nil, fmt.Errorf("managed Celln requires node name, absolute owner token file and controller namespace")
	}
	var registration cellnparent.RegistrationConfig
	if _, err := read(filepath.Join(o.OutputDir, "registrations.json"), &registration); err != nil {
		return nil, err
	}
	if registration.LocalProvisioner == nil || len(registration.HostTemplates) != 1 || len(registration.Registrations) != 0 {
		return nil, fmt.Errorf("managed Celln requires a fresh local starter registration")
	}
	root := filepath.Join(o.StatePath, "authority")
	if registration.LocalProvisioner.Root != root || registration.Approvals != filepath.Join(o.StatePath, "approvals") || registration.Journal != filepath.Join(o.StatePath, "journal") {
		return nil, fmt.Errorf("registration must use the shared installation state")
	}
	for _, path := range []string{root, registration.Approvals, registration.Journal, filepath.Join(root, "parent-issuance"), filepath.Join(root, "trusted-parent-permits"), filepath.Join(root, "trusted-parent-launches")} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("prepared shared state directory unavailable: %s", path)
		}
	}
	// Bounded read; never include token bytes in an error or generated values.
	info, err := os.Lstat(p.OwnerTokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, fmt.Errorf("bounded regular owner credential required")
	}
	raw, err := os.ReadFile(p.OwnerTokenFile)
	token := strings.TrimSpace(string(raw))
	if err != nil || len(token) < 24 || len(raw) > 4096 || strings.ContainsAny(token, " \t\r\n") {
		return nil, fmt.Errorf("invalid owner credential")
	}
	var authority struct {
		APIVersion string `json:"apiVersion"`
		Clients    []struct {
			Principal string `json:"principal"`
			TokenHash string `json:"tokenHash"`
		} `json:"clients"`
	}
	if _, err := read(filepath.Join(root, "trusted-parent-clients.json"), &authority); err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("blake3:%x", blake3.Sum256([]byte(token)))
	matches := 0
	for _, entry := range authority.Clients {
		if entry.TokenHash == digest && entry.Principal == registration.HostTemplates[0].Principal {
			matches++
		}
	}
	if authority.APIVersion != "celln.parent-clients/v1" || matches != 1 {
		return nil, fmt.Errorf("owner token does not match the prepared parent principal")
	}
	// The prepared model profile lives in the SAME authority root as launches.
	// Inspect only the path/hash, never the credential bytes referenced by it.
	modelHash := registration.HostTemplates[0].Native.ModelProfile
	if len(modelHash) != 71 || !strings.HasPrefix(modelHash, "blake3:") || strings.ContainsAny(modelHash, "/\\") {
		return nil, fmt.Errorf("invalid prepared model profile hash")
	}
	var model struct {
		CredentialFile string `json:"credentialFile"`
	}
	actual, err := read(filepath.Join(root, "trusted-parent-models", strings.TrimPrefix(modelHash, "blake3:")+".json"), &model)
	if err != nil || actual != modelHash || !filepath.IsAbs(model.CredentialFile) || filepath.Clean(model.CredentialFile) != model.CredentialFile || strings.ContainsAny(model.CredentialFile, ",{}[]\\\n\r") {
		return nil, fmt.Errorf("prepared model profile must exist in the shared authority root with a clean absolute credential path")
	}
	// Both endpoint families now use the controller's existing router identity.
	registration.LocalProvisioner.TokenFile = "/etc/sympozium/celln/token"
	registration.LocalProvisioner.CAFile = ""
	registration.LocalProvisioner.Target = o.OwnerTarget
	data, err := json.Marshal(registration)
	if err != nil {
		return nil, err
	}
	for _, secret := range []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "celln-parent-config", Namespace: o.ControllerNamespace}, Data: map[string][]byte{"registrations.json": data}},
		{ObjectMeta: metav1.ObjectMeta{Name: "celln-router-parent", Namespace: "celln-system"}, Data: map[string][]byte{"token": []byte(token)}},
	} {
		if err := store.Create(ctx, secret); err != nil {
			return nil, fmt.Errorf("publish %s: %w; partial installation retained", secret.Name, err)
		}
	}
	return []string{
		"celln.dispatcher.enduring.enabled=true",
		"celln.dispatcher.maxCells=4",
		fmt.Sprintf("celln.dispatcher.memoryBytes=%d", registration.HostTemplates[0].Native.ReservedMemoryBytes+268435456),
		"celln.dispatcher.enduring.nodeName=" + p.NodeName,
		"celln.dispatcher.enduring.statePath=" + o.StatePath,
		"celln.dispatcher.enduring.parentConfigSecret=celln-parent-config",
		"celln.router.parentTokenSecret=celln-router-parent",
		"celln.nativeParent.enabled=false",
		"celln.dispatcher.enduring.modelCredentialFiles[0]=" + model.CredentialFile,
	}, nil
}
