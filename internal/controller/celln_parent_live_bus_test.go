package controller_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// Own an authenticated loopback JetStream process; never use a shared bus or
// send synthetic model-output events to make the UI look active.
func liveParentBus(t *testing.T) eventbus.EventBus {
	t.Helper()
	binary := os.Getenv("CELLN_INTEROP_NATS_BINARY")
	if binary == "" {
		return nil
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("absolute owned NATS binary required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	t.Setenv("CELLN_INTEROP_EVENT_BUS_URL", "nats://"+address)
	listener.Close()
	username, password := "parent-proof", rand.Text()
	t.Setenv("NATS_USERNAME", username)
	t.Setenv("NATS_PASSWORD", password)
	dir := t.TempDir()
	raw, err := json.Marshal(map[string]any{"listen": address, "authorization": map[string]any{"users": []map[string]string{{"user": username, "password": password}}}, "jetstream": map[string]string{"store_dir": filepath.Join(dir, "state")}})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "nats.json")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	process := exec.Command(binary, "--config", config)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned event bus did not listen")
		}
		time.Sleep(20 * time.Millisecond)
	}
	startup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bus, err := eventbus.NewNATSEventBusWithContext(startup, "nats://"+address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}
