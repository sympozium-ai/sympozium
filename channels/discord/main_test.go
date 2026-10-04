package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/go-logr/logr"

	"github.com/sympozium-ai/sympozium/internal/channel"
)

// recordingTransport captures every request discordgo makes and answers with
// a minimal message object, so no request leaves the test.
type recordingTransport struct {
	requests []*http.Request
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.requests = append(rt.requests, req)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"1"}`)),
		Request:    req,
	}, nil
}

func newTestChannel(t *testing.T) (*DiscordChannel, *recordingTransport) {
	t.Helper()
	dg, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	rt := &recordingTransport{}
	dg.Client = &http.Client{Transport: rt}
	return &DiscordChannel{session: dg, log: logr.Discard()}, rt
}

func TestSendMessagePostsToChannelMessages(t *testing.T) {
	dc, rt := newTestChannel(t)

	err := dc.sendMessage(channel.OutboundMessage{Channel: "discord", ChatID: "123456789012345678", Text: "hello"})
	if err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if len(rt.requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(rt.requests))
	}
	req := rt.requests[0]
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	if want := "/api/v9/channels/123456789012345678/messages"; req.URL.Path != want {
		t.Errorf("path = %s, want %s", req.URL.Path, want)
	}
}

func TestSendMessageRejectsEmptyChatID(t *testing.T) {
	dc, rt := newTestChannel(t)

	err := dc.sendMessage(channel.OutboundMessage{Channel: "discord", Text: "hello"})
	if !errors.Is(err, errEmptyChatID) {
		t.Fatalf("err = %v, want errEmptyChatID", err)
	}
	if len(rt.requests) != 0 {
		t.Errorf("got %d requests to %s, want none", len(rt.requests), rt.requests[0].URL.Path)
	}
}
