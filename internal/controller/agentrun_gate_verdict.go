package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const (
	// runGateSecretPrefix names the per-run Secret holding a response gate's
	// verdict token.
	runGateSecretPrefix = "sympozium-run-gate-"

	// GateVerdictSecretKey is the key inside that Secret.
	GateVerdictSecretKey = "token"

	// GateAttemptKeySecretKey is the key, in the same Secret, holding the HMAC
	// key that signs in-place retry messages (see ipc.GateKeyEnv). It reaches
	// the agent container only; the gate hook gets GateVerdictSecretKey only,
	// so neither side holds the other's credential.
	GateAttemptKeySecretKey = "attempt-key"

	// gateVerdictTokenBytes is the entropy behind the token, matching the
	// web-proxy key in ensureServerAPIKeySecret.
	gateVerdictTokenBytes = 24
)

// GateVerdictSecretName returns the name of the Secret holding a run's gate
// verdict token. Exported because the API server authenticates a gate hook
// against this same Secret: one spelling, so the minter and the verifier
// cannot drift apart.
func GateVerdictSecretName(run string) string {
	return runGateSecretPrefix + run
}

func agentRunGateSecretName(agentRun *sympoziumv1alpha1.AgentRun) string {
	return GateVerdictSecretName(agentRun.Name)
}

// apiServerURL is the in-cluster base URL a gate hook posts its verdict to,
// set by the Helm chart on the controller Deployment.
//
// Empty means the chart predates this feature. The gate container is then
// given neither the URL nor the token, and a hook must fall back to patching
// the gate-verdict annotation itself — so an older chart degrades to the
// previous behaviour rather than handing out a credential nothing can use.
func apiServerURL() string {
	return os.Getenv("SYMPOZIUM_API_URL")
}

// ensureGateVerdictToken mints the bearer a response gate hook presents when it
// reports its verdict to the API server, so the hook needs no Kubernetes RBAC
// and no ServiceAccount token of its own.
//
// The token authorises one operation on one run: the API server accepts it only
// for the run that owns this Secret, and only while that run is PostRunning or
// AwaitingGate. One token serves every attempt of an in-place retry chain. A
// gate hook granted `patch` on agentruns — the mechanism this replaces — could
// instead write any run's verdict in the namespace.
//
// The Secret is owned by the AgentRun, so it is garbage-collected with it. This
// differs from ensureServerAPIKeySecret, whose Secret is named after the Agent
// and deliberately outlives any single run.
//
// The same Secret holds the in-place retry signing key (GateAttemptKeySecretKey).
//
// Idempotent by design: an existing Secret is reused, never rotated. The gate
// container reads the token once at startup, so re-minting mid-run would
// invalidate a token the hook already holds. A Secret minted before the signing
// key existed gets that key added, leaving its token alone.
func (r *AgentRunReconciler) ensureGateVerdictToken(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	if !hasResponseGateHook(agentRun) {
		return nil
	}

	secretName := agentRunGateSecretName(agentRun)
	key := types.NamespacedName{Name: secretName, Namespace: agentRun.Namespace}

	var existing corev1.Secret
	err := r.Get(ctx, key, &existing)
	switch {
	case err == nil && metav1.IsControlledBy(&existing, agentRun):
		// Ours already. Reuse it.
		if len(existing.Data[GateAttemptKeySecretKey]) > 0 {
			return nil
		}
		attemptKey, keyErr := randomHex(gateVerdictTokenBytes)
		if keyErr != nil {
			return fmt.Errorf("generate gate attempt key: %w", keyErr)
		}
		if existing.Data == nil {
			existing.Data = map[string][]byte{}
		}
		existing.Data[GateAttemptKeySecretKey] = []byte(attemptKey)
		return r.Update(ctx, &existing)
	case err == nil:
		// Someone else's. Secret names are derived from the run name, and run
		// names are predictable, so a Secret sitting at this name that this
		// run does not own may have been planted to pre-set a token for a run
		// that has not started yet. Adopting it would hand that party the
		// authority to approve this run's output, so replace it instead.
		log := logf.FromContext(ctx)
		log.Info("Replacing a gate verdict Secret this run does not own",
			"secret", secretName, "namespace", agentRun.Namespace)
		if delErr := r.Delete(ctx, &existing); delErr != nil && !errors.IsNotFound(delErr) {
			return fmt.Errorf("replacing unowned gate verdict Secret %s: %w", secretName, delErr)
		}
	case !errors.IsNotFound(err):
		return err
	}

	token, err := randomHex(gateVerdictTokenBytes)
	if err != nil {
		return fmt.Errorf("generate gate verdict token: %w", err)
	}
	attemptKey, err := randomHex(gateVerdictTokenBytes)
	if err != nil {
		return fmt.Errorf("generate gate attempt key: %w", err)
	}

	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"sympozium.ai/component": "gate-verdict",
				"sympozium.ai/agent-run": agentRun.Name,
				"sympozium.ai/instance":  agentRun.Spec.AgentRef,
			},
		},
		Data: map[string][]byte{
			GateVerdictSecretKey:    []byte(token),
			GateAttemptKeySecretKey: []byte(attemptKey),
		},
	}

	if err := controllerutil.SetControllerReference(agentRun, &secret, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, &secret); err != nil {
		if !errors.IsAlreadyExists(err) {
			return err
		}
		// A concurrent reconcile may have created it between the Get and the
		// Create. That Secret is as good as this one — but only if it is
		// actually ours, so re-read rather than assume.
		var raced corev1.Secret
		if getErr := r.Get(ctx, key, &raced); getErr != nil {
			return getErr
		}
		if !metav1.IsControlledBy(&raced, agentRun) {
			return fmt.Errorf("gate verdict Secret %s exists and is not owned by this run", secretName)
		}
	}
	return nil
}

// gateAttemptKey returns the run's in-place retry signing key.
//
// Only a Secret this run controls counts, the same rule ensureGateVerdictToken
// applies when minting: a Secret planted at the predictable name would
// otherwise let its author sign attempts and verdicts for this run.
func (r *AgentRunReconciler) gateAttemptKey(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) ([]byte, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Name: agentRunGateSecretName(agentRun), Namespace: agentRun.Namespace,
	}, &secret); err != nil {
		return nil, fmt.Errorf("reading gate Secret: %w", err)
	}
	if !metav1.IsControlledBy(&secret, agentRun) {
		return nil, fmt.Errorf("gate Secret %s is not owned by this run", secret.Name)
	}
	key := secret.Data[GateAttemptKeySecretKey]
	if len(key) == 0 {
		return nil, fmt.Errorf("gate Secret %s has no %s", secret.Name, GateAttemptKeySecretKey)
	}
	return key, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
