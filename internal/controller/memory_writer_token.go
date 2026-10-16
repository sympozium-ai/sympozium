package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Memory writer tokens authenticate writes (POST /store, /update, /forget) to
// a memory server. Each memory server Deployment — "<agent>-memory" for an
// agent's private memory, "<ensemble>-shared-memory" for shared workflow
// memory — has its own random token in a Secret named "<deployment>-writer-token".
//
// The token is injected with a SecretKeyRef into exactly two places: the
// memory-server container, and the agent-runner ("agent") container of a run
// pod that writes to that server. It never goes into skill sidecars, where
// execute_command runs model-chosen commands, so a prompt-injected model
// cannot write to or forget from memory by calling the server directly. The
// agent-runner sets source_agent itself, so the server can trust it.
//
// The writer token is separate from MEMORY_ADMIN_TOKEN, which only the
// memory-server container ever holds; the memory server refuses to start if
// the two are equal.
const (
	memoryWriterTokenKey = "token"

	// memoryWriterTokenEnvName is the env var on the memory-server container
	// and on the agent container for private memory.
	memoryWriterTokenEnvName = "MEMORY_WRITER_TOKEN"
	// workflowMemoryWriterTokenEnvName is the env var on the agent container
	// for shared workflow memory. Read-only personas do not get it.
	workflowMemoryWriterTokenEnvName = "WORKFLOW_MEMORY_WRITER_TOKEN"
)

// memoryWriterSecretName returns the writer-token Secret name for the memory
// server Deployment deployName.
func memoryWriterSecretName(deployName string) string {
	return deployName + "-writer-token"
}

// memoryWriterTokenEnv returns an env var named envName that reads the writer
// token for the memory server deployName. The reference is required, not
// optional: a pod that must hold the token does not start without it, so a
// memory server can never come up accepting unauthenticated writes.
func memoryWriterTokenEnv(envName, deployName string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: envName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: memoryWriterSecretName(deployName)},
				Key:                  memoryWriterTokenKey,
			},
		},
	}
}

// ensureMemoryWriterSecret creates the writer-token Secret for the memory
// server deployName when it does not exist, with owner (the Agent or Ensemble)
// as its controller so it is deleted with it. An existing Secret is left
// alone; to rotate the token, delete the Secret and restart the memory pod.
func ensureMemoryWriterSecret(ctx context.Context, c client.Client, scheme *runtime.Scheme, owner client.Object, deployName string, labels map[string]string) error {
	name := memoryWriterSecretName(deployName)
	var existing corev1.Secret
	err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: owner.GetNamespace()}, &existing)
	if err == nil {
		return nil
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("get memory writer token secret: %w", err)
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate memory writer token: %w", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: owner.GetNamespace(),
			Labels:    labels,
		},
		Data: map[string][]byte{memoryWriterTokenKey: []byte(hex.EncodeToString(raw))},
	}
	if err := controllerutil.SetControllerReference(owner, secret, scheme); err != nil {
		return err
	}
	if err := c.Create(ctx, secret); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create memory writer token secret: %w", err)
	}
	return nil
}

// memoryWriterToken reads the writer token for the memory server deployName,
// for the controller's own writes to it.
func memoryWriterToken(ctx context.Context, c client.Client, namespace, deployName string) (string, error) {
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: memoryWriterSecretName(deployName), Namespace: namespace}, &secret); err != nil {
		return "", err
	}
	token := string(secret.Data[memoryWriterTokenKey])
	if token == "" {
		return "", fmt.Errorf("memory writer token secret %s has no %q key", secret.Name, memoryWriterTokenKey)
	}
	return token, nil
}

// deleteMemoryWriterSecret removes the writer-token Secret for deployName.
func deleteMemoryWriterSecret(ctx context.Context, c client.Client, namespace, deployName string) error {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: memoryWriterSecretName(deployName), Namespace: namespace}}
	if err := c.Delete(ctx, secret); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete memory writer token secret: %w", err)
	}
	return nil
}
