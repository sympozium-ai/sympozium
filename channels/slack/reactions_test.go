package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-logr/logr"

	"github.com/sympozium-ai/sympozium/internal/channel"
)

// roundTripFunc lets a test substitute the http.Client transport.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func newTestSlackChannel(rt roundTripFunc) *SlackChannel {
	return &SlackChannel{
		BotToken: "xoxb-test",
		log:      logr.Discard(),
		client:   &http.Client{Transport: rt},
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestAddReaction_PostsExpectedPayload(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	sc := newTestSlackChannel(func(req *http.Request) (*http.Response, error) {
		captured = req
		buf, _ := io.ReadAll(req.Body)
		capturedBody = buf
		return jsonResponse(`{"ok":true}`), nil
	})

	err := sc.addReaction(context.Background(), channel.OutboundMessage{
		Channel:         "slack",
		ChatID:          "C123",
		Reaction:        ":robot_face:",
		TargetMessageID: "1700000000.000100",
	})
	if err != nil {
		t.Fatalf("addReaction: %v", err)
	}
	if captured.URL.String() != "https://slack.com/api/reactions.add" {
		t.Errorf("URL = %s", captured.URL)
	}
	if got := captured.Header.Get("Authorization"); got != "Bearer xoxb-test" {
		t.Errorf("Authorization = %q", got)
	}
	if got := captured.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["channel"] != "C123" {
		t.Errorf("channel = %v", payload["channel"])
	}
	if payload["timestamp"] != "1700000000.000100" {
		t.Errorf("timestamp = %v", payload["timestamp"])
	}
	if payload["name"] != "robot_face" { // colons stripped
		t.Errorf("name = %v (want colons stripped)", payload["name"])
	}
}

func TestAddReaction_NoOpWhenIncomplete(t *testing.T) {
	called := false
	sc := newTestSlackChannel(func(*http.Request) (*http.Response, error) {
		called = true
		return jsonResponse(`{"ok":true}`), nil
	})

	cases := []channel.OutboundMessage{
		{Channel: "slack", ChatID: "C", Reaction: "eyes"},       // missing ts
		{Channel: "slack", ChatID: "C", TargetMessageID: "1.0"}, // missing reaction
		{Channel: "slack", ChatID: "C"},                         // both missing
	}
	for i, msg := range cases {
		if err := sc.addReaction(context.Background(), msg); err != nil {
			t.Errorf("case %d: unexpected error: %v", i, err)
		}
	}
	if called {
		t.Error("HTTP transport should not be invoked for incomplete messages")
	}
}

func TestAddReaction_AlreadyReactedIsSuccess(t *testing.T) {
	sc := newTestSlackChannel(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"ok":false,"error":"already_reacted"}`), nil
	})
	err := sc.addReaction(context.Background(), channel.OutboundMessage{
		Channel: "slack", ChatID: "C", Reaction: "eyes", TargetMessageID: "1.0",
	})
	if err != nil {
		t.Errorf("already_reacted should be treated as success, got %v", err)
	}
}

func TestAddReaction_SlackErrorBubblesUp(t *testing.T) {
	sc := newTestSlackChannel(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"ok":false,"error":"invalid_auth"}`), nil
	})
	err := sc.addReaction(context.Background(), channel.OutboundMessage{
		Channel: "slack", ChatID: "C", Reaction: "eyes", TargetMessageID: "1.0",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid_auth") {
		t.Errorf("want error containing invalid_auth, got %v", err)
	}
}

func TestAddReaction_HTTPErrorBubblesUp(t *testing.T) {
	sc := newTestSlackChannel(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})
	err := sc.addReaction(context.Background(), channel.OutboundMessage{
		Channel: "slack", ChatID: "C", Reaction: "eyes", TargetMessageID: "1.0",
	})
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Errorf("want network error, got %v", err)
	}
}

func TestAddReaction_NonJSONResponseFails(t *testing.T) {
	sc := newTestSlackChannel(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader([]byte("<html>oops</html>"))),
		}, nil
	})
	err := sc.addReaction(context.Background(), channel.OutboundMessage{
		Channel: "slack", ChatID: "C", Reaction: "eyes", TargetMessageID: "1.0",
	})
	if err == nil {
		t.Error("expected error for non-JSON response")
	}
}

func TestAddReaction_Non2xxFails(t *testing.T) {
	sc := newTestSlackChannel(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("upstream down")),
		}, nil
	})
	err := sc.addReaction(context.Background(), channel.OutboundMessage{
		Channel: "slack", ChatID: "C", Reaction: "eyes", TargetMessageID: "1.0",
	})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("want 503 error, got %v", err)
	}
}

func TestSendMessage_PostsExpectedPayload(t *testing.T) {
	var capturedBody []byte
	var capturedURL string
	sc := newTestSlackChannel(func(req *http.Request) (*http.Response, error) {
		capturedURL = req.URL.String()
		capturedBody, _ = io.ReadAll(req.Body)
		return jsonResponse(`{"ok":true}`), nil
	})
	err := sc.sendMessage(context.Background(), channel.OutboundMessage{
		Channel: "slack", ChatID: "C123", Text: "hello", ThreadID: "1700.0001",
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if capturedURL != "https://slack.com/api/chat.postMessage" {
		t.Errorf("URL = %s", capturedURL)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["channel"] != "C123" || payload["text"] != "hello" || payload["thread_ts"] != "1700.0001" {
		t.Errorf("payload = %+v", payload)
	}
}
