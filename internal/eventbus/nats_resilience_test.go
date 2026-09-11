package eventbus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestIsStreamGoneErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"stream not found", jetstream.ErrStreamNotFound, true},
		{"no stream response", jetstream.ErrNoStreamResponse, true},
		{"no responders", nats.ErrNoResponders, true},
		{"wrapped stream not found", fmt.Errorf("publish: %w", jetstream.ErrStreamNotFound), true},
		{"consumer not found is a consumer problem, not a stream problem", jetstream.ErrConsumerNotFound, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"unrelated", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStreamGoneErr(tc.err); got != tc.want {
				t.Fatalf("isStreamGoneErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestConsumerConfigSurvivesFetchGaps(t *testing.T) {
	cfg := consumerConfig("sympozium.agent.run.completed")
	// The server-side default is a few seconds; a consumer reaped during a
	// NATS restart loses every event published until Subscribe recreates it.
	if cfg.InactiveThreshold < 10*time.Minute {
		t.Fatalf("InactiveThreshold = %s, want at least 10m so a restart does not reap the consumer", cfg.InactiveThreshold)
	}
	if cfg.FilterSubject != "sympozium.agent.run.completed" {
		t.Fatalf("FilterSubject = %q", cfg.FilterSubject)
	}
	if cfg.AckPolicy != jetstream.AckExplicitPolicy {
		t.Fatalf("AckPolicy = %v, want explicit", cfg.AckPolicy)
	}
	if cfg.DeliverPolicy != jetstream.DeliverNewPolicy {
		t.Fatalf("DeliverPolicy = %v, want new", cfg.DeliverPolicy)
	}
}

// shrinkProvisioningKnobs makes the unavailable-NATS paths fail fast so the
// tests below finish in well under a second per attempt. Restored on cleanup.
func shrinkProvisioningKnobs(t *testing.T) {
	t.Helper()
	attempts, backoff, opTimeout := streamBootstrapAttempts, streamBootstrapBackoff, jetStreamOpTimeout
	streamBootstrapAttempts, streamBootstrapBackoff, jetStreamOpTimeout = 1, 0, 100*time.Millisecond
	t.Cleanup(func() {
		streamBootstrapAttempts, streamBootstrapBackoff, jetStreamOpTimeout = attempts, backoff, opTimeout
	})
}

// unusedLoopbackAddress reserves a loopback port and releases it, so a dial
// there is refused immediately — the shape of "NATS is not up yet".
func unusedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func TestLazyStartupReturnsUsableBusWhenNATSUnavailable(t *testing.T) {
	shrinkProvisioningKnobs(t)
	t.Setenv("NATS_USERNAME", "")
	t.Setenv("NATS_PASSWORD", "")
	address := unusedLoopbackAddress(t)

	start := time.Now()
	bus, err := NewNATSEventBus("nats://" + address)
	if err != nil || bus == nil {
		t.Fatalf("NewNATSEventBus must hand back a lazily provisioned bus when NATS is down: bus=%v err=%v", bus, err)
	}
	defer bus.Close()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("lazy startup took %s; bootstrap did not respect the shrunken knobs", elapsed)
	}

	// Subscribe must not fail either: a router's Start returning an error here
	// would take the whole manager down over a startup-ordering race. It gets
	// a live channel that closes once the subscription context ends.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	events, err := bus.Subscribe(ctx, TopicAgentRunCompleted)
	if err != nil || events == nil {
		t.Fatalf("lazy Subscribe: events=%v err=%v", events, err)
	}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("received an event from an unreachable NATS")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription channel did not close after its context ended")
	}
}

func TestBoundedStartupStillFailsWhenNATSUnavailable(t *testing.T) {
	shrinkProvisioningKnobs(t)
	t.Setenv("NATS_USERNAME", "")
	t.Setenv("NATS_PASSWORD", "")
	address := unusedLoopbackAddress(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	bus, err := NewNATSEventBusWithContext(ctx, "nats://"+address)
	if bus != nil || err == nil {
		t.Fatalf("bounded startup must keep failing fast: bus=%v err=%v", bus, err)
	}
}
