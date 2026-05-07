package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// channelStateConfigMapName returns the ConfigMap name used to persist
// per-chat mute state for an Agent.
func channelStateConfigMapName(instanceName string) string {
	return instanceName + "-channel-state"
}

// channelMuteKey returns the data key used inside the state ConfigMap to
// represent a single (channel, chat) combination.
//
// The chat ID is hashed because:
//   - ConfigMap data keys are restricted to [-._a-zA-Z0-9]+, so raw chat IDs
//     containing characters like ':' (Matrix), '@' (WhatsApp JIDs), or '/'
//     would be rejected by the API server.
//   - It eliminates any chance of collision across channel types whose IDs
//     happen to share a separator character.
//
// Format: "<channelType>.<sha256[:8]>". 64 bits is ample for the small set of
// chats one Agent ever talks to and keeps the key short and human-greppable.
func channelMuteKey(channelType, chatID string) string {
	sum := sha256.Sum256([]byte(chatID))
	return channelType + "." + hex.EncodeToString(sum[:8])
}

const channelMuteValue = "muted"

// channelTriggerSpec returns the ChannelTriggerSpec for the named channel
// type on the given Agent, or nil if none is configured.
func channelTriggerSpec(inst *sympoziumv1alpha1.Agent, channelType string) *sympoziumv1alpha1.ChannelTriggerSpec {
	if inst == nil {
		return nil
	}
	for i := range inst.Spec.Channels {
		if inst.Spec.Channels[i].Type == channelType {
			return inst.Spec.Channels[i].Triggers
		}
	}
	return nil
}

// triggerDecision describes what the router should do with an inbound
// message after evaluating ChannelTriggerSpec.
type triggerDecision int

const (
	// triggerProcess: deliver the message to the agent.
	triggerProcess triggerDecision = iota
	// triggerDrop: silently drop the message (chat is muted and the
	// message did not match any start keyword).
	triggerDrop
	// triggerResume: chat just unmuted; consume the message without
	// creating an AgentRun.
	triggerResume
	// triggerStop: chat just muted; consume the message without
	// creating an AgentRun.
	triggerStop
)

// matchesAny reports whether text contains any of the keywords as a
// case-insensitive substring. Empty/whitespace-only keywords are ignored.
func matchesAny(text string, keywords []string) bool {
	if len(keywords) == 0 || text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// evaluateTrigger applies start/stop keyword rules given the current
// muted state of the chat. The returned decision fully determines the
// new mute state (Stop ⇒ muted, Resume ⇒ unmuted, others ⇒ unchanged),
// so callers do not need a separate state value.
func evaluateTrigger(spec *sympoziumv1alpha1.ChannelTriggerSpec, text string, muted bool) triggerDecision {
	if spec == nil {
		return triggerProcess
	}
	if muted {
		// Only StartKeywords are evaluated while muted; everything
		// else is dropped silently.
		if matchesAny(text, spec.StartKeywords) {
			return triggerResume
		}
		return triggerDrop
	}
	// Active chat: a stop keyword mutes; otherwise process normally.
	if matchesAny(text, spec.StopKeywords) {
		return triggerStop
	}
	return triggerProcess
}

// muteStore reads/writes per-chat mute state for an Agent backed by a
// single ConfigMap per Agent. All operations are best-effort: failures
// are surfaced to the caller but never panic.
type muteStore struct {
	c        client.Client
	owner    *sympoziumv1alpha1.Agent
	instance string
}

// newMuteStore returns a store scoped to a single Agent. The owner is
// used both to derive the namespace/name of the backing ConfigMap and to
// stamp an ownerReference on it, so the ConfigMap is garbage-collected
// when the Agent is deleted.
func newMuteStore(c client.Client, owner *sympoziumv1alpha1.Agent) *muteStore {
	return &muteStore{c: c, owner: owner, instance: owner.Name}
}

// IsMuted reports whether the (channel, chat) tuple is currently muted.
// A missing ConfigMap is treated as "not muted".
func (m *muteStore) IsMuted(ctx context.Context, channelType, chatID string) (bool, error) {
	cm := &corev1.ConfigMap{}
	err := m.c.Get(ctx, types.NamespacedName{
		Namespace: m.owner.Namespace,
		Name:      channelStateConfigMapName(m.instance),
	}, cm)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return cm.Data[channelMuteKey(channelType, chatID)] == channelMuteValue, nil
}

// SetMuted sets or clears the mute flag for (channel, chat). It creates
// the backing ConfigMap on demand and removes empty entries to keep
// the map tidy.
func (m *muteStore) SetMuted(ctx context.Context, channelType, chatID string, muted bool) error {
	name := channelStateConfigMapName(m.instance)
	key := channelMuteKey(channelType, chatID)

	cm := &corev1.ConfigMap{}
	err := m.c.Get(ctx, types.NamespacedName{Namespace: m.owner.Namespace, Name: name}, cm)
	switch {
	case errors.IsNotFound(err):
		if !muted {
			return nil // nothing to clear
		}
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: m.owner.Namespace,
				Labels: map[string]string{
					"sympozium.ai/component": "channel-state",
					"sympozium.ai/instance":  m.instance,
				},
			},
			Data: map[string]string{key: channelMuteValue},
		}
		if err := controllerutil.SetControllerReference(m.owner, cm, m.c.Scheme()); err != nil {
			return fmt.Errorf("set owner reference on channel state configmap: %w", err)
		}
		if err := m.c.Create(ctx, cm); err != nil {
			return fmt.Errorf("create channel state configmap: %w", err)
		}
		return nil
	case err != nil:
		return err
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	current := cm.Data[key] == channelMuteValue
	if current == muted {
		return nil // no change
	}
	if muted {
		cm.Data[key] = channelMuteValue
	} else {
		delete(cm.Data, key)
	}
	if err := m.c.Update(ctx, cm); err != nil {
		return fmt.Errorf("update channel state configmap: %w", err)
	}
	return nil
}
