package main

import (
	"strings"
	"testing"
)

func TestRedactToken(t *testing.T) {
	tc := &TelegramChannel{BotToken: "123456:ABC-secret-token"}

	// A url.Error-style string embeds the full request URL including the token.
	in := `Get "https://api.telegram.org/bot123456:ABC-secret-token/getUpdates?offset=0": dial tcp: timeout`
	out := tc.redactToken(in)

	if strings.Contains(out, "123456:ABC-secret-token") {
		t.Errorf("redactToken left the bot token in the string: %q", out)
	}
	if !strings.Contains(out, "***REDACTED***") {
		t.Errorf("redactToken did not insert the redaction marker: %q", out)
	}
	if !strings.Contains(out, "dial tcp: timeout") {
		t.Errorf("redactToken removed non-secret content: %q", out)
	}
}

func TestRedactTokenEmpty(t *testing.T) {
	tc := &TelegramChannel{BotToken: ""}
	in := "some error"
	if got := tc.redactToken(in); got != in {
		t.Errorf("redactToken with empty token = %q, want unchanged %q", got, in)
	}
}
