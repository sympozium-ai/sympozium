package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

// sign produces a valid Slack v0 signature for the given secret/timestamp/body.
func sign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySlackSignature(t *testing.T) {
	const secret = "8f742231b10e8888abcd99yyyzzz85a5"
	body := []byte(`{"type":"event_callback","event":{"type":"message","user":"U1","text":"hi"}}`)
	now := strconv.FormatInt(time.Now().Unix(), 10)
	valid := sign(secret, now, body)

	tests := []struct {
		name      string
		secret    string
		ts        string
		signature string
		body      []byte
		want      bool
	}{
		{"valid", secret, now, valid, body, true},
		{"wrong secret rejected", "different-secret", now, valid, body, false},
		{"tampered body rejected", secret, now, valid, []byte(`{"evil":true}`), false},
		{"empty signing secret rejected", "", now, valid, body, false},
		{"missing signature rejected", secret, now, "", body, false},
		{"missing timestamp rejected", secret, "", valid, body, false},
		{"non-numeric timestamp rejected", secret, "not-a-number", valid, body, false},
		{
			"stale timestamp rejected (replay)",
			secret,
			strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10),
			sign(secret, strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10), body),
			body,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := verifySlackSignature(tt.secret, tt.ts, tt.signature, tt.body)
			if got != tt.want {
				t.Errorf("verifySlackSignature() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestVerifySlackSignature_FutureSkew confirms a slightly-future timestamp
// (clock skew within the window) is still accepted.
func TestVerifySlackSignature_FutureSkew(t *testing.T) {
	const secret = "abc123"
	body := []byte("challenge")
	future := strconv.FormatInt(time.Now().Add(2*time.Minute).Unix(), 10)
	if !verifySlackSignature(secret, future, sign(secret, future, body), body) {
		t.Error("expected near-future timestamp within window to be accepted")
	}
	// A signature valid in content but ~4 minutes out should still pass (< 5 min).
	edge := strconv.FormatInt(time.Now().Add(-4*time.Minute).Unix(), 10)
	if !verifySlackSignature(secret, edge, sign(secret, edge, body), body) {
		t.Error("expected 4-minute-old timestamp to be within window")
	}
}
