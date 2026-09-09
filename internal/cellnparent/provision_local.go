package cellnparent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/zeebo/blake3"
)

// LocalProvisioner is protected operator configuration for a co-located Celln
// authority store. Every replica must retain and share Journal. This does not
// provide remote host provisioning, nor infer that Target owns Root.
type LocalProvisioner struct {
	Binary    string `json:"binary"`
	Root      string `json:"root"`
	Journal   string `json:"journal"`
	Approvals string `json:"approvals"`
	Target    string `json:"target"`
	TokenFile string `json:"tokenFile"`
	CAFile    string `json:"caFile,omitempty"`
}

// ProvisionAndApprove pins host/plan choice before invoking the local issuer,
// then revalidates live intent before publishing the one-use registration. An
// error never removes the durable choice or refunds host issuance authority.
func (p LocalProvisioner) ProvisionAndApprove(ctx context.Context, loader cellnauthority.Loader, intent ProvisionIntent, template HostProvisionTemplate) (RunApproval, error) {
	var zero RunApproval
	if err := validateOwnerOrigin(p.Target); err != nil {
		return zero, err
	}
	for _, directory := range []string{p.Root, p.Journal, p.Approvals} {
		info, err := os.Stat(directory)
		if !filepath.IsAbs(directory) || err != nil || !info.IsDir() {
			return zero, fmt.Errorf("existing absolute local provision directories required")
		}
	}
	if !filepath.IsAbs(p.Binary) || !filepath.IsAbs(p.TokenFile) || (p.CAFile != "" && !filepath.IsAbs(p.CAFile)) {
		return zero, fmt.Errorf("absolute operator executable and credential paths required")
	}
	raw, err := BuildHostProvisionPlan(ctx, loader, intent, template)
	if err != nil {
		return zero, err
	}
	expected, err := provisionIncarnation(template.Scope, string(intent.Selection.Run.UID))
	if err != nil {
		return zero, err
	}
	selection, err := ParentSelectionDigest(intent.Selection.Snapshot)
	if err != nil {
		return zero, err
	}
	registration := ParentLaunchRegistration{APIVersion: "sympozium.ai/celln-parent-registration-v1", SelectionSHA256: selection, Target: p.Target, Principal: template.Principal, Incarnation: expected, TokenFile: p.TokenFile, CAFile: p.CAFile, Model: template.Model, SystemPrompt: intent.Spec.SystemPrompt, HostLimits: template.HostLimits}
	sum := sha256.Sum256(raw)
	choice, err := json.Marshal(struct {
		APIVersion string           `json:"apiVersion"`
		Host       LocalProvisioner `json:"host"`
		PlanSHA256 string           `json:"planSHA256"`
	}{"sympozium.ai/celln-parent-local-choice-v1", p, fmt.Sprintf("sha256:%x", sum)})
	if err != nil {
		return zero, err
	}
	if err := publishParentRecord(p.Journal, "provision-"+approvalFileName(string(intent.Selection.Run.UID)), choice); err != nil {
		return zero, fmt.Errorf("preserve original provision host/plan: %w", err)
	}
	if err := intent.Revalidate(ctx, loader); err != nil {
		return zero, err
	}
	result, err := p.invoke(ctx, raw, template.Principal)
	if err != nil {
		return zero, err
	}
	if result.Incarnation != expected {
		return zero, fmt.Errorf("local issuer returned another run incarnation")
	}
	if err := intent.Revalidate(ctx, loader); err != nil {
		return zero, err
	}
	registration.LaunchProfile = result.LaunchProfile
	return PublishRegisteredParent(ctx, loader, intent.Selection, registration, p.Journal, p.Approvals)
}

type localProvisionResult struct {
	APIVersion    string `json:"apiVersion"`
	LaunchProfile string `json:"launchProfile"`
	Incarnation   string `json:"incarnation"`
}

func (p LocalProvisioner) invoke(ctx context.Context, plan []byte, principal string) (localProvisionResult, error) {
	var result localProvisionResult
	file, err := os.CreateTemp(p.Journal, ".parent-provision-plan-")
	if err != nil {
		return result, fmt.Errorf("local provision staging unavailable")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(plan); err != nil {
		return result, err
	}
	if err := file.Sync(); err != nil {
		return result, err
	}
	if err := file.Close(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, p.Binary, "--root", p.Root, "parent-provision", file.Name(), "--principal", principal)
	// No shell, inherited model keys, user startup files, or ambient CELLN_ROOT.
	command.Env = []string{"PATH=/usr/bin:/bin"}
	command.WaitDelay = time.Second
	stdout, stderr := &provisionOutput{}, &provisionOutput{}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return result, fmt.Errorf("local parent issuer failed; preserve issuance state")
	}
	if stdout.overflow || stderr.overflow {
		return result, fmt.Errorf("local parent issuer exceeded output bound")
	}
	d := json.NewDecoder(bytes.NewReader(stdout.data))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF || result.APIVersion != "celln.parent-provisioned/v1" || !hashPattern.MatchString(result.LaunchProfile) || !hashPattern.MatchString(result.Incarnation) {
		return localProvisionResult{}, fmt.Errorf("invalid local parent issuer result")
	}
	return result, nil
}

type provisionOutput struct {
	data     []byte
	overflow bool
}

func (b *provisionOutput) Write(raw []byte) (int, error) {
	n := len(raw)
	remaining := 4096 - len(b.data)
	if len(raw) > remaining {
		b.overflow = true
		raw = raw[:remaining]
	}
	b.data = append(b.data, raw...)
	return n, nil
}

// Match Rust serde_json's tuple encoding without Go's HTML/U+2028 escaping.
// Control characters are already forbidden by the shared identity contract.
func provisionIncarnation(scope, uid string) (string, error) {
	if !boundedIdentity(scope, 512) || !boundedIdentity(uid, 128) || !utf8.ValidString(scope) || !utf8.ValidString(uid) {
		return "", fmt.Errorf("invalid parent run identity")
	}
	quote := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	raw := `["celln.parent-run-incarnation/v1",` + quote(scope) + `,` + quote(uid) + `]`
	sum := blake3.Sum256([]byte(raw))
	return fmt.Sprintf("blake3:%x", sum), nil
}
