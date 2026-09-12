package modelgateway

import "testing"

func TestProviderResponsesPreserveUnknownUsageAndRefuseUnsafeEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, body string
		want                 *int64
		bad                  bool
	}{
		{"unknown", "openai-chat", `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`, nil, false},
		{"zero", "openai-chat", `{"choices":[{"message":{"role":"assistant"}}],"usage":{"completion_tokens":0}}`, new(int64), false},
		{"anthropic-unknown", "anthropic-messages", `{"type":"message","role":"assistant","content":[]}`, nil, false},
		{"no-message", "openai-chat", `{"usage":{"completion_tokens":1}}`, nil, true},
		{"empty", "openai-chat", `{"choices":[]}`, nil, true},
		{"stream", "openai-chat", `{"choices":[{"delta":{"content":"no"}}]}`, nil, true},
		{"bad-role", "anthropic-messages", `{"type":"message","role":"user","content":[]}`, nil, true},
		{"error", "openai-chat", `{"error":{"message":"failure"}}`, nil, true},
		{"negative", "openai-chat", `{"choices":[{"message":{"role":"assistant"}}],"usage":{"completion_tokens":-1}}`, nil, true},
		{"fraction", "openai-chat", `{"choices":[{"message":{"role":"assistant"}}],"usage":{"completion_tokens":1.5}}`, nil, true},
		{"overflow", "openai-chat", `{"choices":[{"message":{"role":"assistant"}}],"usage":{"completion_tokens":9223372036854775808}}`, nil, true},
		{"duplicate", "openai-chat", `{"choices":[{"message":{"role":"assistant"}}],"usage":{"completion_tokens":1,"completion_tokens":0}}`, nil, true},
		{"escaped-credential", "openai-chat", `{"choices":[{"message":{"role":"assistant","content":"\u0063anary-key"}}]}`, nil, true},
		{"escaped-bearer", "openai-chat", `{"choices":[{"message":{"role":"assistant","content":"\u0062roker-bearer"}}]}`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateProviderResponse(tc.protocol, []byte(tc.body), []byte("canary-key"), "broker-bearer")
			if (err != nil) != tc.bad {
				t.Fatalf("got %v want bad=%v", err, tc.bad)
			}
			if !tc.bad && ((got == nil) != (tc.want == nil) || got != nil && *got != *tc.want) {
				t.Fatalf("usage=%v want=%v", got, tc.want)
			}
		})
	}
}
