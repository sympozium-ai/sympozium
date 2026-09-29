package v1alpha1

import "testing"

func TestInheritModelTuning(t *testing.T) {
	maxTokens := int32(12000)
	agent := AgentConfig{Thinking: "high", MaxTokens: &maxTokens, Temperature: "0.2"}

	t.Run("fills unset fields", func(t *testing.T) {
		var m ModelSpec
		agent.InheritModelTuning(&m)
		if m.Thinking != "high" || m.MaxTokens == nil || *m.MaxTokens != 12000 || m.Temperature != "0.2" {
			t.Fatalf("got thinking %q maxTokens %v temperature %q", m.Thinking, m.MaxTokens, m.Temperature)
		}
		if m.MaxTokens == agent.MaxTokens {
			t.Fatal("MaxTokens aliases the Agent's pointer; want a copy")
		}
	})

	t.Run("explicit values win", func(t *testing.T) {
		own := int32(10)
		m := ModelSpec{Thinking: "off", MaxTokens: &own, Temperature: "1"}
		agent.InheritModelTuning(&m)
		if m.Thinking != "off" || *m.MaxTokens != 10 || m.Temperature != "1" {
			t.Fatalf("got thinking %q maxTokens %d temperature %q", m.Thinking, *m.MaxTokens, m.Temperature)
		}
	})

	t.Run("unset agent leaves run unset", func(t *testing.T) {
		var m ModelSpec
		AgentConfig{}.InheritModelTuning(&m)
		if m.Thinking != "" || m.MaxTokens != nil || m.Temperature != "" {
			t.Fatalf("got thinking %q maxTokens %v temperature %q", m.Thinking, m.MaxTokens, m.Temperature)
		}
	})

	t.Run("nil model is a no-op", func(t *testing.T) {
		agent.InheritModelTuning(nil)
	})
}
