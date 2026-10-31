package controller

import (
	"context"
	stderrors "errors"
	"fmt"
	"path"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

const (
	runModelTokenBytes     = 32
	harnessGatewayCAVolume = "harness-gateway-ca"
	harnessGatewayCAMount  = "/etc/sympozium/harness-gateway"
	// modelMediatedEnv tells an adapter that its key is a token for the gateway, so
	// its base URL isn't a third party it must keep a real credential away from.
	modelMediatedEnv = "SYMPOZIUM_MODEL_MEDIATED"
)

// HarnessGatewayConfig is set when the chart deploys the harness gateway. Nil
// means it is not deployed, and a run that requires mediation fails.
type HarnessGatewayConfig struct {
	// URL is the gateway's in-cluster base URL.
	URL string
	// Profiles reads the operator's provider profiles.
	Profiles harnessgateway.ProfileSource
	// CASecret holds the gateway's CA, under harnessgateway.CASecretKey.
	CASecret types.NamespacedName
	// Reader reads CASecret. It must be uncached.
	Reader client.Reader
}

// modelMediationRequired reports whether the Agent's policy keeps model keys out
// of its harness pods. A run that got this far has passed validatePolicy, so a
// missing Agent or policy is an error, never "not required".
func modelMediationRequired(ctx context.Context, c client.Reader, namespace, agentName string) (bool, error) {
	var agent sympoziumv1alpha1.Agent
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentName}, &agent); err != nil {
		return false, fmt.Errorf("reading Agent %q for model mediation: %w", agentName, err)
	}
	if agent.Spec.PolicyRef == "" {
		return false, nil
	}
	var policy sympoziumv1alpha1.SympoziumPolicy
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agent.Spec.PolicyRef}, &policy); err != nil {
		return false, fmt.Errorf("reading policy %q for model mediation: %w", agent.Spec.PolicyRef, err)
	}
	return policy.Spec.HarnessPolicy != nil && policy.Spec.HarnessPolicy.ModelMediation == sympoziumv1alpha1.ModelMediationRequired, nil
}

// runIsMediated reports whether this run's model calls go through the gateway.
// Only a harness shares a container with its model. A run that names no model
// Secret has no key to protect, so it connects as it does today.
func (r *AgentRunReconciler) runIsMediated(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) (bool, error) {
	model := agentRun.Spec.Model
	if !harnessRun(agentRun) || (model.AuthSecretRef == "" && model.ProviderHeadersSecretRef == "") {
		return false, nil
	}
	// Rendering tests build a reconciler with no client. A running controller
	// always has one.
	if r.Client == nil {
		return false, nil
	}
	return modelMediationRequired(ctx, r.Client, agentRun.Namespace, agentRun.Spec.AgentRef)
}

// uncachedReader reads Secrets this reconcile may have just written, which the
// manager's cache can't be trusted to show yet. A stale miss here would fail the
// run, or try to mint a second token.
func (r *AgentRunReconciler) uncachedReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// mediationRoute resolves where the run's calls go, refusing what can't be
// mediated. The controller never falls back to injecting the key.
func (r *AgentRunReconciler) mediationRoute(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) (*harnessgateway.Route, error) {
	if r.HarnessGateway == nil {
		return nil, stderrors.New("harnessPolicy.modelMediation is required, but the harness gateway is not enabled; set harnessGateway.enabled in the chart")
	}
	if agentRun.Spec.AgentSandbox != nil && agentRun.Spec.AgentSandbox.Enabled {
		return nil, stderrors.New("harnessPolicy.modelMediation does not support the Agent Sandbox backend yet")
	}
	if agentRun.Spec.Model.AuthSecretRef == "" {
		return nil, stderrors.New("harnessPolicy.modelMediation needs spec.model.authSecretRef when providerHeadersSecretRef is set")
	}
	profiles, err := r.HarnessGateway.Profiles.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading harness gateway profiles: %w", err)
	}
	return profiles.Route(agentRun.Spec.Model.Provider, agentRun.Spec.Model.BaseURL)
}

// ensureRunModelToken prepares a mediated run: it pins the key to the origin the
// run's calls go to, and mints the token the harness holds in its place.
//
// Idempotent like ensureGateVerdictToken: an existing Secret is reused, never
// rotated, because a replayed reconcile may already have a pod holding the token
// and every in-place attempt shares it. A successor run has its own name, so it
// gets its own token.
func (r *AgentRunReconciler) ensureRunModelToken(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	mediated, err := r.runIsMediated(ctx, agentRun)
	if err != nil || !mediated {
		return err
	}
	route, err := r.mediationRoute(ctx, agentRun)
	if err != nil {
		return err
	}
	if err := r.pinRunModelKey(ctx, agentRun, route); err != nil {
		return err
	}
	var caSecret corev1.Secret
	if err := r.HarnessGateway.Reader.Get(ctx, r.HarnessGateway.CASecret, &caSecret); err != nil {
		return fmt.Errorf("reading the harness gateway CA: %w", err)
	}
	ca := caSecret.Data[harnessgateway.CASecretKey]
	if len(ca) == 0 {
		return fmt.Errorf("Secret %s has no %s", r.HarnessGateway.CASecret, harnessgateway.CASecretKey)
	}

	bound, realKey, err := r.runModelBinding(ctx, agentRun, route)
	if err != nil {
		return err
	}

	secretName := harnessgateway.RunTokenSecretName(agentRun.Name)
	key := types.NamespacedName{Name: secretName, Namespace: agentRun.Namespace}
	var existing corev1.Secret
	err = r.uncachedReader().Get(ctx, key, &existing)
	switch {
	case err == nil && metav1.IsControlledBy(&existing, agentRun):
		if string(existing.Data[harnessgateway.CASecretKey]) == string(ca) {
			return nil
		}
		existing.Data[harnessgateway.CASecretKey] = ca
		return r.Update(ctx, &existing)
	case err == nil:
		// The name is predictable, so a Secret here that this run does not own
		// may have been planted to pre-set the token. Replace it.
		logf.FromContext(ctx).Info("Replacing a run model token Secret this run does not own",
			"secret", secretName, "namespace", agentRun.Namespace)
		if delErr := r.Delete(ctx, &existing); delErr != nil && !errors.IsNotFound(delErr) {
			return fmt.Errorf("replacing unowned run model token Secret %s: %w", secretName, delErr)
		}
	case !errors.IsNotFound(err):
		return err
	}

	random, err := randomHex(runModelTokenBytes)
	if err != nil {
		return fmt.Errorf("generate run model token: %w", err)
	}
	token := route.Profile.TokenFor(realKey, random)
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        secretName,
			Namespace:   agentRun.Namespace,
			Annotations: bound,
			Labels: map[string]string{
				"sympozium.ai/component": "run-model-token",
				"sympozium.ai/agent-run": agentRun.Name,
				"sympozium.ai/instance":  agentRun.Spec.AgentRef,
			},
		},
		Data: map[string][]byte{
			harnessgateway.TokenSecretKey: []byte(token),
			harnessgateway.CASecretKey:    ca,
		},
	}
	if err := controllerutil.SetControllerReference(agentRun, &secret, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, &secret); err != nil {
		if !errors.IsAlreadyExists(err) {
			return err
		}
		// A concurrent reconcile may have created it. That one is as good, but
		// only if it is actually ours.
		var raced corev1.Secret
		if getErr := r.uncachedReader().Get(ctx, key, &raced); getErr != nil {
			return getErr
		}
		if !metav1.IsControlledBy(&raced, agentRun) {
			return fmt.Errorf("run model token Secret %s exists and is not owned by this run", secretName)
		}
	}
	return nil
}

// runModelBinding records what the token is minted for. The gateway refuses a
// call once any of it changes, as Celln's permit binds the Agent and Secret UIDs.
// An existing token keeps its binding, so an edited run stops working rather than
// moving the token to another key.
//
// It also returns the real key, only so the token can be shaped like its kind.
func (r *AgentRunReconciler) runModelBinding(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, route *harnessgateway.Route) (map[string]string, string, error) {
	reader := r.uncachedReader()
	var agent sympoziumv1alpha1.Agent
	if err := reader.Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: agentRun.Spec.AgentRef}, &agent); err != nil {
		return nil, "", fmt.Errorf("reading Agent %q: %w", agentRun.Spec.AgentRef, err)
	}
	var key corev1.Secret
	if err := reader.Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: agentRun.Spec.Model.AuthSecretRef}, &key); err != nil {
		return nil, "", fmt.Errorf("reading model key Secret %q: %w", agentRun.Spec.Model.AuthSecretRef, err)
	}
	bound := map[string]string{
		harnessgateway.AgentUIDAnnotation:     string(agent.UID),
		harnessgateway.KeySecretUIDAnnotation: string(key.UID),
		harnessgateway.OriginAnnotation:       route.Origin,
	}
	if name := agentRun.Spec.Model.ProviderHeadersSecretRef; name != "" {
		var headers corev1.Secret
		if err := reader.Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: name}, &headers); err != nil {
			return nil, "", fmt.Errorf("reading provider headers Secret %q: %w", name, err)
		}
		bound[harnessgateway.HeadersSecretUIDAnnotation] = string(headers.UID)
	}
	return bound, route.Profile.Key(key.Data), nil
}

// pinRunModelKey records the origin on the key. authorizeRunKeys has already
// held the key to this Agent. The gateway sends a key only to its pinned origin,
// so a later run that points the same key elsewhere is refused.
func (r *AgentRunReconciler) pinRunModelKey(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, route *harnessgateway.Route) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var secret corev1.Secret
		if err := r.uncachedReader().Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: agentRun.Spec.Model.AuthSecretRef}, &secret); err != nil {
			return fmt.Errorf("reading model key Secret %q: %w", agentRun.Spec.Model.AuthSecretRef, err)
		}
		if route.Profile.Key(secret.Data) == "" {
			return fmt.Errorf("model key Secret %q has none of the keys provider %q reads: %v", secret.Name, agentRun.Spec.Model.Provider, route.Profile.SecretKeys)
		}
		if modelkey.PinnedTo(&secret, route.Origin) {
			return nil
		}
		if err := modelkey.Pin(&secret, route.Origin); err != nil {
			return err
		}
		return r.Update(ctx, &secret)
	})
}

// applyModelMediation takes the model key out of a mediated run's pod and gives
// the harness the gateway and its token instead. Every pod template passes
// through here, so a path that did not prepare a token fails rather than
// rendering a pod that holds the key.
func (r *AgentRunReconciler) applyModelMediation(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, spec *corev1.PodSpec) error {
	mediated, err := r.runIsMediated(ctx, agentRun)
	if err != nil {
		return err
	}
	if !mediated {
		// Only the controller may say a run is mediated. An adapter relies on this
		// to relax its guard on where a real credential goes, and this run's
		// credential is real.
		for i := range spec.Containers {
			spec.Containers[i].Env = slices.DeleteFunc(spec.Containers[i].Env, func(e corev1.EnvVar) bool { return e.Name == modelMediatedEnv })
		}
		return nil
	}
	route, err := r.mediationRoute(ctx, agentRun)
	if err != nil {
		return err
	}
	secretName := harnessgateway.RunTokenSecretName(agentRun.Name)
	var token corev1.Secret
	if err := r.uncachedReader().Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: secretName}, &token); err != nil {
		return fmt.Errorf("harnessPolicy.modelMediation needs a run model token, which this execution path did not prepare: %w", err)
	}
	if !metav1.IsControlledBy(&token, agentRun) {
		return fmt.Errorf("run model token Secret %s is not owned by this run", secretName)
	}

	harness := &spec.Containers[0]
	profile := route.Profile
	baseURLNames := slices.Concat([]string{"MODEL_BASE_URL"}, profile.Env.BaseURL)
	controllerOwned := []string{"MODEL_BASE_URL", "MODEL_PROVIDER_HEADERS", "NODE_EXTRA_CA_CERTS", modelMediatedEnv}
	removed := slices.Concat(allowedAuthSecretKeys, controllerOwned)
	harness.Env = slices.DeleteFunc(harness.Env, func(e corev1.EnvVar) bool { return slices.Contains(removed, e.Name) })

	// A profile can't overwrite something the controller sets, such as TASK.
	for _, name := range slices.Concat(profile.Env.BaseURL, profile.Env.APIKey) {
		if slices.Contains(controllerOwned, name) || slices.ContainsFunc(harness.Env, func(e corev1.EnvVar) bool { return e.Name == name }) {
			return fmt.Errorf("harness gateway profile %q sets %s, which the controller owns", profile.Name, name)
		}
	}

	url := fmt.Sprintf("%s/runs/%s/%s%s", r.HarnessGateway.URL, agentRun.Namespace, agentRun.Name, profile.ClientBasePath)
	for _, name := range baseURLNames {
		harness.Env = append(harness.Env, corev1.EnvVar{Name: name, Value: url})
	}
	for _, name := range profile.Env.APIKey {
		harness.Env = append(harness.Env, corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  harnessgateway.TokenSecretKey,
			},
		}})
	}
	harness.Env = append(harness.Env, corev1.EnvVar{Name: modelMediatedEnv, Value: "true"})
	harness.Env = append(harness.Env, corev1.EnvVar{Name: "NODE_EXTRA_CA_CERTS", Value: path.Join(harnessGatewayCAMount, harnessgateway.CASecretKey)})
	harness.VolumeMounts = append(harness.VolumeMounts, corev1.VolumeMount{Name: harnessGatewayCAVolume, MountPath: harnessGatewayCAMount, ReadOnly: true})
	// Only the public CA is projected. The token reaches the harness through its
	// env, and no other container sees either.
	mode := int32(0o444)
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: harnessGatewayCAVolume, VolumeSource: corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{
			SecretName:  secretName,
			DefaultMode: &mode,
			Items:       []corev1.KeyToPath{{Key: harnessgateway.CASecretKey, Path: harnessgateway.CASecretKey}},
		},
	}})
	return assertNoModelSecretInPod(agentRun, spec)
}

// assertNoModelSecretInPod is the last check on the promise the feature makes:
// no container in the pod references the run's model Secrets.
func assertNoModelSecretInPod(agentRun *sympoziumv1alpha1.AgentRun, spec *corev1.PodSpec) error {
	secrets := []string{agentRun.Spec.Model.AuthSecretRef, agentRun.Spec.Model.ProviderHeadersSecretRef}
	named := func(name string) bool { return name != "" && slices.Contains(secrets, name) }
	for _, container := range slices.Concat(spec.InitContainers, spec.Containers) {
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && named(env.ValueFrom.SecretKeyRef.Name) {
				return fmt.Errorf("container %s would receive a model Secret on a mediated run", container.Name)
			}
		}
		for _, from := range container.EnvFrom {
			if from.SecretRef != nil && named(from.SecretRef.Name) {
				return fmt.Errorf("container %s would receive a model Secret on a mediated run", container.Name)
			}
		}
	}
	for _, volume := range spec.Volumes {
		if volume.Secret != nil && named(volume.Secret.SecretName) {
			return fmt.Errorf("volume %s would mount a model Secret on a mediated run", volume.Name)
		}
	}
	return nil
}

// cleanupRunModelToken deletes a finished run's token. Finished runs stay around
// up to RunHistoryLimit, and a dead token has no reason to stay with them. The
// gateway already refuses it in a terminal phase, so a failed delete is not
// worth holding up completion.
func (r *AgentRunReconciler) cleanupRunModelToken(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) {
	var secret corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: agentRun.Namespace, Name: harnessgateway.RunTokenSecretName(agentRun.Name)}, &secret)
	if err != nil || !metav1.IsControlledBy(&secret, agentRun) {
		return
	}
	if err := r.Delete(ctx, &secret); err != nil && !errors.IsNotFound(err) {
		logf.FromContext(ctx).Error(err, "failed to delete the run model token", "secret", secret.Name)
	}
}
