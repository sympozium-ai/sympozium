package eventbus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
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

// setProvisioningKnobs overrides the bootstrap knobs for one test and restores
// them on cleanup.
func setProvisioningKnobs(t *testing.T, attempts int, backoff, opTimeout time.Duration) {
	t.Helper()
	prevAttempts, prevBackoff, prevOpTimeout := streamBootstrapAttempts, streamBootstrapBackoff, jetStreamOpTimeout
	streamBootstrapAttempts, streamBootstrapBackoff, jetStreamOpTimeout = attempts, backoff, opTimeout
	t.Cleanup(func() {
		streamBootstrapAttempts, streamBootstrapBackoff, jetStreamOpTimeout = prevAttempts, prevBackoff, prevOpTimeout
	})
}

// shrinkProvisioningKnobs makes the unavailable-NATS paths fail fast so the
// no-server tests finish in well under a second per attempt.
func shrinkProvisioningKnobs(t *testing.T) {
	t.Helper()
	setProvisioningKnobs(t, 1, 0, 100*time.Millisecond)
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

// Opt-in real-server regression for the lazy paths, in the same shape as
// TestNATSRealServerReconnectAfterStartupContextExpires: this test owns a
// loopback nats-server process and temporary storage; no external endpoint or
// credentials are accepted.
//
// Scenario: the controller starts before NATS exists (node drain, rolling
// upgrade). The bus and a router subscription are created against a refused
// port, NATS comes up afterwards, and events must flow without any restart.
// Then the stream is deleted server-side while the connection stays up, and a
// publish must recreate it rather than fail.
func TestNATSRealServerLazyStartupRecoversWhenServerAppearsLater(t *testing.T) {
	binary := os.Getenv("SYMPOZIUM_NATS_SERVER")
	if binary == "" {
		t.Skip("set SYMPOZIUM_NATS_SERVER to a local nats-server binary")
	}
	// One quick bootstrap attempt so the pre-server construction returns fast,
	// but a realistic per-call timeout once the server is up.
	setProvisioningKnobs(t, 1, 0, 2*time.Second)
	t.Setenv("NATS_USERNAME", "")
	t.Setenv("NATS_PASSWORD", "")

	address := unusedLoopbackAddress(t)
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	storage := t.TempDir()
	var server *exec.Cmd
	stop := func() {
		if server != nil {
			_ = server.Process.Kill()
			_ = server.Wait()
			server = nil
		}
	}
	defer stop()
	start := func() {
		server = exec.Command(binary, "-a", "127.0.0.1", "-p", strconv.Itoa(port), "-js", "-sd", storage)
		server.Stdout = os.Stderr
		server.Stderr = os.Stderr
		if err := server.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("owned NATS process did not listen")
	}

	// 1. Nothing is listening yet. Construction and subscription must both
	//    succeed lazily.
	bus, err := NewNATSEventBus("nats://" + address)
	if err != nil {
		t.Fatalf("lazy construction against a refused port: %v", err)
	}
	defer bus.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	events, err := bus.Subscribe(ctx, TopicAgentRunCompleted)
	if err != nil {
		t.Fatalf("lazy Subscribe against a refused port: %v", err)
	}

	// 2. A publish while NATS is still down must report an error — the lazy
	//    bus defers provisioning, it never pretends a publish succeeded.
	downCtx, cancelDown := context.WithTimeout(ctx, 500*time.Millisecond)
	if err := bus.Publish(downCtx, TopicAgentRunCompleted, &Event{Topic: TopicAgentRunCompleted, Timestamp: time.Now()}); err == nil {
		t.Fatal("publish with NATS down must fail, not silently succeed")
	}
	cancelDown()

	// 3. NATS comes up. The connection reconnects on its own and the lazy
	//    subscription creates its consumer; the consumer is DeliverNew, so keep
	//    publishing fresh proofs until one comes back.
	start()
	deadline := time.Now().Add(10 * time.Second)
	for !bus.conn.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !bus.conn.IsConnected() {
		t.Fatal("connection did not establish after the server appeared")
	}
	awaitRoundTrip := func(prefix string) {
		t.Helper()
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		giveUp := time.After(20 * time.Second)
		for i := 0; ; i++ {
			id := fmt.Sprintf("%s-%d", prefix, i)
			publishCtx, cancelPublish := context.WithTimeout(ctx, 3*time.Second)
			err := bus.Publish(publishCtx, TopicAgentRunCompleted, &Event{
				Topic: TopicAgentRunCompleted, Timestamp: time.Now(), Metadata: map[string]string{"proof": id},
			})
			cancelPublish()
			if err != nil {
				t.Logf("publish %s: %v (retrying)", id, err)
			}
			select {
			case got, ok := <-events:
				if !ok {
					t.Fatal("subscription channel closed")
				}
				t.Logf("received %s", got.Metadata["proof"])
				return
			case <-ticker.C:
			case <-giveUp:
				t.Fatalf("no %s event arrived on the lazily created subscription", prefix)
			}
		}
	}
	awaitRoundTrip("after-start")

	// 4. Delete the stream out from under a live connection. The very next
	//    publish must recreate it and succeed instead of returning an error.
	js, err := jetstream.New(bus.conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := js.DeleteStream(ctx, streamName); err != nil {
		t.Fatalf("deleting stream: %v", err)
	}
	publishCtx, cancelPublish := context.WithTimeout(ctx, 5*time.Second)
	err = bus.Publish(publishCtx, TopicAgentRunCompleted, &Event{
		Topic: TopicAgentRunCompleted, Timestamp: time.Now(), Metadata: map[string]string{"proof": "after-stream-delete"},
	})
	cancelPublish()
	if err != nil {
		t.Fatalf("publish after stream deletion must recreate the stream, got: %v", err)
	}
	if _, err := js.Stream(ctx, streamName); err != nil {
		t.Fatalf("stream was not recreated by Publish: %v", err)
	}

	// 5. The subscriber lost its consumer with the stream; #254's recreate
	//    path brings it back, and events flow again.
	awaitRoundTrip("after-stream-delete")
}
