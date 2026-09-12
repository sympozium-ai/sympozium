package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	streamName    = "sympozium"
	consumerGroup = "sympozium-workers"

	// reconnectBackoff is how long a Subscribe loop waits before (re)creating its
	// consumer — after a fetch error (the consumer or stream was lost because
	// NATS was restarted/recreated) or while NATS is still unreachable on a lazy
	// bus.
	reconnectBackoff = 2 * time.Second

	// consumerInactiveThreshold is how long the server keeps a subscription's
	// ephemeral consumer alive with no client activity. The server default is a
	// few seconds, so any fetch gap longer than that — a rolling NATS restart, a
	// network blip, a handler that blocks on a full channel — reaps the consumer,
	// and the DeliverNew recreate in Subscribe then silently drops every event
	// published in between. A long threshold keeps the consumer and its cursor,
	// so the backlog is delivered when fetching resumes. Consumers orphaned by a
	// recreate are still reclaimed by the server once the threshold elapses.
	consumerInactiveThreshold = time.Hour
)

// Provisioning knobs. Variables rather than constants so tests can shrink
// them; the defaults are the production values.
var (
	// streamBootstrapAttempts and streamBootstrapBackoff bound how long a
	// constructor waits for the stream to become creatable before it either
	// fails (bounded mode) or hands back a bus that provisions lazily.
	streamBootstrapAttempts = 10
	streamBootstrapBackoff  = 2 * time.Second

	// jetStreamOpTimeout bounds each stream/consumer management call.
	jetStreamOpTimeout = 5 * time.Second
)

// NATSEventBus implements EventBus using NATS JetStream.
type NATSEventBus struct {
	conn *nats.Conn
	js   jetstream.JetStream

	// lazy marks a bus built by NewNATSEventBus: NATS being unreachable at
	// startup or at Subscribe time is treated as transient and self-heals,
	// rather than failing the caller.
	lazy bool
}

// NewNATSEventBus creates a NATS JetStream event bus for a long-lived process
// such as the controller. It does not fail because NATS is merely unreachable:
// the connection reconnects indefinitely, the stream is provisioned on first
// use if it cannot be created at startup, and Subscribe hands back a live
// channel whose consumer is created once NATS answers. A process whose NATS
// peer is recreated alongside it (node drain, rolling upgrade) therefore comes
// up with routing intact instead of running with the bus permanently
// disabled. Configuration errors — a malformed URL, bad options — are still
// returned.
func NewNATSEventBus(url string) (*NATSEventBus, error) {
	return newNATSEventBus(context.Background(), url, true)
}

// NewNATSEventBusWithContext bounds initial stream provisioning and fails fast
// when NATS is not usable within ctx. Cancellation closes the connection,
// including its background reconnect loop. A successfully returned bus retains
// the existing lifetime/reconnect policy after ctx expires, and its Subscribe
// keeps fail-fast semantics, which suits per-request subscribers.
func NewNATSEventBusWithContext(ctx context.Context, url string) (*NATSEventBus, error) {
	return newNATSEventBus(ctx, url, false)
}

func newNATSEventBus(ctx context.Context, url string, lazy bool) (*NATSEventBus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := &NATSEventBus{lazy: lazy}
	initialized := make(chan struct{})

	// MaxReconnects(-1) keeps the client reconnecting indefinitely. A bounded
	// limit means a NATS pod recreation that takes longer than
	// limit*ReconnectWait permanently kills the connection, silently breaking
	// every subscription until the process restarts.
	opts := append(connectOptions(),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Printf("eventbus: disconnected from NATS: %v", err)
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			// The first reconnect can race constructor initialization.
			<-initialized
			if n.js == nil {
				return
			}
			log.Printf("eventbus: reconnected to NATS at %s", c.ConnectedUrl())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := n.ensureStream(ctx); err != nil {
				log.Printf("eventbus: failed to ensure stream after reconnect: %v", err)
			}
		}),
		nats.ClosedHandler(func(_ *nats.Conn) { log.Printf("eventbus: NATS connection closed") }),
	)
	connectTimeout := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < connectTimeout {
		connectTimeout = time.Until(deadline)
		if connectTimeout <= 0 {
			close(initialized)
			return nil, context.DeadlineExceeded
		}
	}
	opts = append(opts, nats.Timeout(connectTimeout))
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		close(initialized)
		return nil, fmt.Errorf("connecting to NATS: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		close(initialized)
		nc.Close()
		return nil, fmt.Errorf("creating JetStream context: %w", err)
	}

	n.conn = nc
	n.js = js
	close(initialized)

	// Retry stream creation — NATS may not be fully ready yet.
	var lastErr error
	for attempt := 0; attempt < streamBootstrapAttempts; attempt++ {
		if _, lastErr = n.ensureStream(ctx); lastErr == nil {
			break
		}
		if attempt == streamBootstrapAttempts-1 {
			break
		}
		timer := time.NewTimer(streamBootstrapBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			nc.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if lastErr != nil {
		if !lazy {
			nc.Close()
			return nil, fmt.Errorf("creating JetStream stream after retries: %w", lastErr)
		}
		// Lazy bus: keep the connection (it is reconnecting in the background)
		// and let Publish/Subscribe provision the stream once NATS answers.
		// Failing here would leave the caller without an event bus for the
		// life of the process over what is usually a startup-ordering race.
		log.Printf("eventbus: JetStream stream not provisioned at startup (%v); it will be created on first use once NATS is reachable", lastErr)
	}

	return n, nil
}

// Publish sends an event to the NATS JetStream stream.
// Trace context from ctx is automatically injected into NATS message headers.
//
// If the stream is missing server-side — NATS was recreated with ephemeral
// storage, the reconnect handler has not finished re-ensuring it, or this bus
// was built lazily before NATS was reachable — Publish recreates the stream
// and retries once instead of failing.
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

	if _, err := n.js.PublishMsg(ctx, msg); err != nil {
		if !isStreamGoneErr(err) {
			return fmt.Errorf("publishing to %s: %w", subject, err)
		}
		log.Printf("eventbus: publish to %s found no stream (%v); recreating and retrying", subject, err)
		if _, serr := n.ensureStream(ctx); serr != nil {
			return fmt.Errorf("publishing to %s: stream missing (%v) and recreate failed: %w", subject, err, serr)
		}
		if _, rerr := n.js.PublishMsg(ctx, msg); rerr != nil {
			return fmt.Errorf("publishing to %s after recreating stream: %w", subject, rerr)
		}
	}

	return nil
}

// isStreamGoneErr reports whether err means the server has no stream for us
// to publish to or consume from, so the caller should recreate it and retry.
// ErrNoResponders is included because a JetStream that has restarted without
// our stream answers requests with no responders rather than a typed
// not-found error. A missing consumer is deliberately not classified here:
// the stream is fine in that case and only the consumer needs recreating.
func isStreamGoneErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, jetstream.ErrStreamNotFound) ||
		errors.Is(err, jetstream.ErrNoStreamResponse) ||
		errors.Is(err, nats.ErrNoResponders)
}

// Subscribe returns a channel that receives events for the given topic.
//
// The subscription is resilient to NATS restarts/recreations: if a fetch fails
// because the consumer or stream no longer exists, the loop recreates them
// (re-creating the stream first if needed) and resumes, so the subscription
// recovers without requiring the process to restart.
//
// On a bus built with NewNATSEventBus, NATS being unreachable at Subscribe
// time is handled the same way: the channel is returned live and the consumer
// is created once NATS answers. A bus built with NewNATSEventBusWithContext
// returns the error instead, so per-request subscribers fail fast.
func (n *NATSEventBus) Subscribe(ctx context.Context, topic string) (<-chan *Event, error) {
	subject := topicToSubject(topic)

	consumer, err := n.createConsumer(ctx, subject)
	if err != nil {
		if !n.lazy {
			return nil, fmt.Errorf("creating consumer for %s: %w", subject, err)
		}
		// A long-lived subscriber (the routers) returning this error from Start
		// takes the whole controller-runtime manager down over a transient
		// outage the fetch loop below would have ridden out. Hand back a live
		// channel and let the loop create the consumer instead.
		log.Printf("eventbus: consumer for %s not created yet (%v); will keep trying", subject, err)
		consumer = nil
	}

	ch := make(chan *Event, 64)

	go func() {
		defer close(ch)
		for {
			if ctx.Err() != nil {
				return
			}

			// Lazy start: NATS was unreachable at Subscribe time. Keep trying
			// to create the consumer, backing off between attempts.
			if consumer == nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(reconnectBackoff):
				}
				created, cerr := n.createConsumer(ctx, subject)
				if cerr != nil {
					continue
				}
				log.Printf("eventbus: created consumer for %s", subject)
				consumer = created
				continue
			}

			msgs, err := consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
			if err == nil {
				for msg := range msgs.Messages() {
					var event Event
					if uerr := json.Unmarshal(msg.Data(), &event); uerr != nil {
						// Terminate (not Nak) an unparseable message. The consumer
						// has no MaxDeliver/AckWait backoff, so Nak would redeliver
						// the same poison message immediately and live-lock this
						// subscription forever. Term drops it and advances.
						msg.Term()
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
				// A fetch that simply timed out with no messages reports no
				// error here; only a real consumer/stream problem does.
				err = msgs.Error()
				if err == nil {
					continue
				}
			}

			// Fetch (or batch) error. This usually means the consumer or the
			// stream was lost because NATS was restarted/recreated. Back off,
			// then recreate the consumer so the subscription self-heals instead
			// of spinning forever on a dead consumer.
			select {
			case <-ctx.Done():
				return
			case <-time.After(reconnectBackoff):
			}

			newConsumer, rerr := n.createConsumer(ctx, subject)
			if rerr != nil {
				log.Printf("eventbus: failed to recreate consumer for %s: %v (after %v)", subject, rerr, err)
				continue
			}
			log.Printf("eventbus: recreated consumer for %s after error: %v", subject, err)
			consumer = newConsumer
		}
	}()

	return ch, nil
}

// Close shuts down the NATS connection.
func (n *NATSEventBus) Close() error {
	n.conn.Close()
	return nil
}

// ensureStream creates or updates the sympozium stream and returns a handle to
// it. It is safe to call repeatedly — after a NATS recreate the stream no longer
// exists, and calling this recreates it.
func (n *NATSEventBus) ensureStream(ctx context.Context) (jetstream.Stream, error) {
	cctx, cancel := context.WithTimeout(ctx, jetStreamOpTimeout)
	defer cancel()
	return n.js.CreateOrUpdateStream(cctx, streamConfig())
}

// createConsumer (re)creates a pull consumer for the given subject, ensuring the
// stream exists first so it works even after NATS has been recreated.
func (n *NATSEventBus) createConsumer(ctx context.Context, subject string) (jetstream.Consumer, error) {
	stream, err := n.ensureStream(ctx)
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithTimeout(ctx, jetStreamOpTimeout)
	defer cancel()
	return stream.CreateOrUpdateConsumer(cctx, consumerConfig(subject))
}

// consumerConfig returns the ephemeral pull-consumer configuration Subscribe
// uses for a subject. See consumerInactiveThreshold for why the threshold is
// set explicitly.
func consumerConfig(subject string) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		FilterSubject:     subject,
		AckPolicy:         jetstream.AckExplicitPolicy,
		DeliverPolicy:     jetstream.DeliverNewPolicy,
		InactiveThreshold: consumerInactiveThreshold,
	}
}

// streamConfig returns the JetStream stream configuration for Sympozium events.
func streamConfig() jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:      streamName,
		Subjects:  []string{"sympozium.>"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    24 * time.Hour,
		Storage:   jetstream.FileStorage,
		Replicas:  1,
	}
}

// topicToSubject converts a dotted topic (e.g. "agent.run.completed")
// to a NATS subject under the sympozium namespace (e.g. "sympozium.agent.run.completed").
func topicToSubject(topic string) string {
	return "sympozium." + topic
}
