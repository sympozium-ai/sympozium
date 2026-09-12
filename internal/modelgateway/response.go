package modelgateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
)

// validateProviderResponse accepts complete non-streaming message envelopes.
// Missing usage is unknown, never measured zero. Unknown provider extension
// fields are retained, but duplicate keys and malformed accounting are refused.
func validateProviderResponse(protocol string, raw, credential []byte, bearer string) (*int64, error) {
	var object map[string]json.RawMessage
	if cap.StrictDecode(raw, &object) != nil || object == nil {
		return nil, fmt.Errorf("invalid provider object")
	}
	if payload, ok := object["error"]; ok && !bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return nil, fmt.Errorf("provider error envelope")
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return nil, fmt.Errorf("invalid provider JSON")
	}
	for _, secret := range []string{string(credential), bearer} {
		if secret != "" && containsResponseSecret(decoded, secret) {
			return nil, fmt.Errorf("provider echoed credential")
		}
	}
	usageKey := ""
	switch protocol {
	case "openai-chat":
		var choices []struct {
			Message map[string]json.RawMessage `json:"message"`
		}
		if json.Unmarshal(object["choices"], &choices) != nil || len(choices) == 0 {
			return nil, fmt.Errorf("complete choices required")
		}
		for _, choice := range choices {
			var role string
			if json.Unmarshal(choice.Message["role"], &role) != nil || role != "assistant" {
				return nil, fmt.Errorf("assistant message required")
			}
		}
		usageKey = "completion_tokens"
	case "anthropic-messages":
		var kind, role string
		var content []json.RawMessage
		if json.Unmarshal(object["type"], &kind) != nil || kind != "message" || json.Unmarshal(object["role"], &role) != nil || role != "assistant" || json.Unmarshal(object["content"], &content) != nil || content == nil {
			return nil, fmt.Errorf("complete Anthropic message required")
		}
		usageKey = "output_tokens"
	default:
		return nil, fmt.Errorf("unsupported response protocol")
	}
	rawUsage, present := object["usage"]
	if !present || bytes.Equal(bytes.TrimSpace(rawUsage), []byte("null")) {
		return nil, nil
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(rawUsage, &usage) != nil || usage == nil {
		return nil, fmt.Errorf("invalid usage object")
	}
	value, present := usage[usageKey]
	if !present || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, nil
	}
	var observed int64
	if json.Unmarshal(value, &observed) != nil || observed < 0 {
		return nil, fmt.Errorf("invalid output usage")
	}
	return &observed, nil
}

// Also inspect decoded strings/keys, so JSON escaping cannot bypass an exact
// credential-canary check. This is not a claim to detect arbitrary encodings.
func containsResponseSecret(value any, secret string) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, secret)
	case []any:
		for _, item := range v {
			if containsResponseSecret(item, secret) {
				return true
			}
		}
	case map[string]any:
		for key, item := range v {
			if strings.Contains(key, secret) || containsResponseSecret(item, secret) {
				return true
			}
		}
	}
	return false
}
