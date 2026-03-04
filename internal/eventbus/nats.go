package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	streamName    = "sympozium"
	consumerGroup = "sympozium-workers"
)

// NATSEventBus implements EventBus using NATS JetStream.
type NATSEventBus struct {
	conn   *nats.Conn
	js     jetstream.JetStream
	stream jetstream.Stream
}

// NewNATSEventBus creates a new NATS JetStream event bus.
func NewNATSEventBus(url string) (*NATSEventBus, error) {
	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(10),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to NATS: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("creating JetStream context: %w", err)
	}

	// Retry stream creation — NATS may not be fully ready yet.
	var stream jetstream.Stream
	for attempt := 0; attempt < 10; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		stream, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:      streamName,
			Subjects:  []string{"sympozium.>"},
			Retention: jetstream.LimitsPolicy,
			MaxAge:    24 * time.Hour,
			Storage:   jetstream.FileStorage,
			Replicas:  1,
		})
		cancel()
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("creating JetStream stream after retries: %w", err)
	}

	return &NATSEventBus{
		conn:   nc,
		js:     js,
		stream: stream,
	}, nil
}

// Publish sends an event to the NATS JetStream stream.
// Trace context from ctx is automatically injected into NATS message headers.
func (n *NATSEventBus) Publish(ctx context.Context, topic string, event *Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshalling event: %w", err)
	}

	subject := topicToSubject(topic)
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header:  nats.Header{},
	}
	InjectTraceContext(ctx, msg.Header)

	_, err = n.js.PublishMsg(ctx, msg)
	if err != nil {
		return fmt.Errorf("publishing to %s: %w", subject, err)
	}

	return nil
}

// Subscribe returns a channel that receives events for the given topic.
func (n *NATSEventBus) Subscribe(ctx context.Context, topic string) (<-chan *Event, error) {
	subject := topicToSubject(topic)

	consumer, err := n.stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("creating consumer for %s: %w", subject, err)
	}

	ch := make(chan *Event, 64)

	go func() {
		defer close(ch)
		for {
			msgs, err := consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}

			for msg := range msgs.Messages() {
				var event Event
				if err := json.Unmarshal(msg.Data(), &event); err != nil {
					msg.Nak()
					continue
				}

				// Extract trace context from NATS message headers so consumers
				// can continue the distributed trace started by the publisher.
				event.Ctx = ExtractTraceContext(ctx, msg.Headers())

				select {
				case ch <- &event:
					msg.Ack()
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// Close shuts down the NATS connection.
func (n *NATSEventBus) Close() error {
	n.conn.Close()
	return nil
}

// topicToSubject converts a dotted topic (e.g. "agent.run.completed")
// to a NATS subject under the sympozium namespace (e.g. "sympozium.agent.run.completed").
func topicToSubject(topic string) string {
	return "sympozium." + topic
}
