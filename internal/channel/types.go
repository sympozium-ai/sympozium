// Package channel provides base types and interfaces for Sympozium channel implementations.
// Each channel type (Telegram, WhatsApp, Discord, Slack) runs as its own pod
// and uses this framework to connect to the event bus.
package channel

import (
	"context"

	"github.com/alexsjones/sympozium/internal/eventbus"
)

// InboundMessage represents a message received from an external channel.
type InboundMessage struct {
	Channel      string            `json:"channel"`
	InstanceName string            `json:"instanceName"`
	SenderID     string            `json:"senderId"`
	SenderName   string            `json:"senderName,omitempty"`
	ChatID       string            `json:"chatId"`
	ThreadID     string            `json:"threadId,omitempty"`
	Text         string            `json:"text"`
	Attachments  []Attachment      `json:"attachments,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// OutboundMessage represents a message to send to an external channel.
type OutboundMessage struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chatId"`
	ThreadID string `json:"threadId,omitempty"`
	Text     string `json:"text"`
	Format   string `json:"format,omitempty"` // plain, markdown, html
	ReplyTo  string `json:"replyTo,omitempty"`
}

// Attachment represents a file or media attachment.
type Attachment struct {
	Type     string `json:"type"` // image, file, audio, video
	URL      string `json:"url,omitempty"`
	Filename string `json:"filename,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// HealthStatus represents the connection health of a channel.
type HealthStatus struct {
	Channel   string `json:"channel"`
	Connected bool   `json:"connected"`
	Message   string `json:"message,omitempty"`
}

// BaseChannel provides common functionality for channel implementations.
// Embed this in each channel to get event bus publishing/subscribing.
type BaseChannel struct {
	ChannelType  string
	InstanceName string
	EventBus     eventbus.EventBus
}

// PublishInbound publishes an inbound message to the event bus.
func (bc *BaseChannel) PublishInbound(ctx context.Context, msg InboundMessage) error {
	msg.Channel = bc.ChannelType
	msg.InstanceName = bc.InstanceName

	event, err := eventbus.NewEvent(eventbus.TopicChannelMessageRecv, map[string]string{
		"channel":      bc.ChannelType,
		"instanceName": bc.InstanceName,
	}, msg)
	if err != nil {
		return err
	}

	return bc.EventBus.Publish(ctx, eventbus.TopicChannelMessageRecv, event)
}

// PublishHealth publishes a health update to the event bus.
func (bc *BaseChannel) PublishHealth(ctx context.Context, status HealthStatus) error {
	status.Channel = bc.ChannelType

	event, err := eventbus.NewEvent(eventbus.TopicChannelHealthUpdate, map[string]string{
		"channel":      bc.ChannelType,
		"instanceName": bc.InstanceName,
	}, status)
	if err != nil {
		return err
	}

	return bc.EventBus.Publish(ctx, eventbus.TopicChannelHealthUpdate, event)
}

// SubscribeOutbound subscribes to outbound messages destined for this channel.
func (bc *BaseChannel) SubscribeOutbound(ctx context.Context) (<-chan *eventbus.Event, error) {
	return bc.EventBus.Subscribe(ctx, eventbus.TopicChannelMessageSend)
}
