package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/channel"
)

func TestSendMessage_AttributionPayload(t *testing.T) {
	var capturedBody []byte
	var capturedURL string
	sc := newTestSlackChannel(func(req *http.Request) (*http.Response, error) {
		capturedURL = req.URL.String()
		capturedBody, _ = io.ReadAll(req.Body)
		return jsonResponse(`{"ok":true}`), nil
	})

	err := sc.sendMessage(context.Background(), channel.OutboundMessage{
		Channel:   "slack",
		ChatID:    "C123",
		Text:      "hi",
		Username:  "agent-alpha",
		IconEmoji: ":robot_face:",
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
	if payload["channel"] != "C123" {
		t.Errorf("channel = %v", payload["channel"])
	}
	if payload["text"] != "hi" {
		t.Errorf("text = %v", payload["text"])
	}
	if payload["username"] != "agent-alpha" {
		t.Errorf("username = %v", payload["username"])
	}
	if payload["icon_emoji"] != ":robot_face:" {
		t.Errorf("icon_emoji = %v", payload["icon_emoji"])
	}
}

func TestSendMessage_IconURLTakesPrecedence(t *testing.T) {
	var capturedBody []byte
	sc := newTestSlackChannel(func(req *http.Request) (*http.Response, error) {
		capturedBody, _ = io.ReadAll(req.Body)
		return jsonResponse(`{"ok":true}`), nil
	})

	err := sc.sendMessage(context.Background(), channel.OutboundMessage{
		Channel:   "slack",
		ChatID:    "C123",
		Text:      "hi",
		Username:  "a",
		IconURL:   "https://x/i.png",
		IconEmoji: ":robot_face:",
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["icon_url"] != "https://x/i.png" {
		t.Errorf("icon_url = %v", payload["icon_url"])
	}
	if _, ok := payload["icon_emoji"]; ok {
		t.Errorf("icon_emoji present: %v", payload["icon_emoji"])
	}
}

func TestSendMessage_NoAttributionKeysWhenEmpty(t *testing.T) {
	var capturedBody []byte
	sc := newTestSlackChannel(func(req *http.Request) (*http.Response, error) {
		capturedBody, _ = io.ReadAll(req.Body)
		return jsonResponse(`{"ok":true}`), nil
	})

	err := sc.sendMessage(context.Background(), channel.OutboundMessage{
		Channel: "slack",
		ChatID:  "C123",
		Text:    "hi",
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["channel"] != "C123" {
		t.Errorf("channel = %v", payload["channel"])
	}
	if payload["text"] != "hi" {
		t.Errorf("text = %v", payload["text"])
	}
	for _, key := range []string{"username", "icon_url", "icon_emoji"} {
		if _, ok := payload[key]; ok {
			t.Errorf("%s present: %v", key, payload[key])
		}
	}
}

func TestSendMessage_ThreadRoutingUnchangedWithAttribution(t *testing.T) {
	var capturedBody []byte
	sc := newTestSlackChannel(func(req *http.Request) (*http.Response, error) {
		capturedBody, _ = io.ReadAll(req.Body)
		return jsonResponse(`{"ok":true}`), nil
	})

	err := sc.sendMessage(context.Background(), channel.OutboundMessage{
		Channel:  "slack",
		ChatID:   "C123",
		Text:     "hi",
		ThreadID: "1700000000.000100",
		Username: "agent-alpha",
	})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload["thread_ts"] != "1700000000.000100" {
		t.Errorf("thread_ts = %v", payload["thread_ts"])
	}
	if payload["username"] != "agent-alpha" {
		t.Errorf("username = %v", payload["username"])
	}
}
