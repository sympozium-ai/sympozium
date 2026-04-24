package controller

import (
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildInstance_ChannelAccessControlPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		packAC         map[string]*sympoziumv1alpha1.ChannelAccessControl
		personaAC      map[string]*sympoziumv1alpha1.ChannelAccessControl
		wantAllowChats []string
	}{
		{
			name: "persona override wins over ensemble-level",
			packAC: map[string]*sympoziumv1alpha1.ChannelAccessControl{
				"discord": {AllowedChats: []string{"ensemble-channel"}},
			},
			personaAC: map[string]*sympoziumv1alpha1.ChannelAccessControl{
				"discord": {AllowedChats: []string{"persona-channel"}},
			},
			wantAllowChats: []string{"persona-channel"},
		},
		{
			name: "fallback to ensemble-level when persona has none",
			packAC: map[string]*sympoziumv1alpha1.ChannelAccessControl{
				"discord": {AllowedChats: []string{"ensemble-channel"}},
			},
			personaAC:      nil,
			wantAllowChats: []string{"ensemble-channel"},
		},
		{
			name:           "no access control at either level",
			packAC:         nil,
			personaAC:      nil,
			wantAllowChats: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &EnsembleReconciler{}
			pack := &sympoziumv1alpha1.Ensemble{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pack",
					Namespace: "default",
				},
				Spec: sympoziumv1alpha1.EnsembleSpec{
					ChannelConfigs:       map[string]string{"discord": "my-discord-secret"},
					ChannelAccessControl: tt.packAC,
				},
			}
			persona := &sympoziumv1alpha1.PersonaSpec{
				Name:                 "tech-lead",
				SystemPrompt:         "You are a tech lead.",
				Channels:             []string{"discord"},
				ChannelAccessControl: tt.personaAC,
			}

			inst := r.buildInstance(pack, persona, "test-pack-tech-lead", "")

			if len(inst.Spec.Channels) != 1 {
				t.Fatalf("expected 1 channel, got %d", len(inst.Spec.Channels))
			}
			ch := inst.Spec.Channels[0]
			if ch.Type != "discord" {
				t.Fatalf("expected channel type discord, got %s", ch.Type)
			}

			if tt.wantAllowChats == nil {
				if ch.AccessControl != nil {
					t.Errorf("expected nil AccessControl, got %+v", ch.AccessControl)
				}
				return
			}

			if ch.AccessControl == nil {
				t.Fatal("expected non-nil AccessControl")
			}
			if len(ch.AccessControl.AllowedChats) != len(tt.wantAllowChats) {
				t.Fatalf("AllowedChats = %v, want %v", ch.AccessControl.AllowedChats, tt.wantAllowChats)
			}
			for i, want := range tt.wantAllowChats {
				if ch.AccessControl.AllowedChats[i] != want {
					t.Errorf("AllowedChats[%d] = %q, want %q", i, ch.AccessControl.AllowedChats[i], want)
				}
			}
		})
	}
}
