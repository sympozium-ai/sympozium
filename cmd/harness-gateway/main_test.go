package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// A rollout must let a call in flight finish, and the process must stay up for it.
func TestServeDrainsACallInFlight(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "finished")
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() {
		served <- serve(ctx, server, 30*time.Second, func() error { return server.Serve(listener) })
	}()
	body := make(chan string, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			body <- err.Error()
			return
		}
		defer func() { _ = response.Body.Close() }()
		raw, _ := io.ReadAll(response.Body)
		body <- string(raw)
	}()
	<-started
	cancel()
	select {
	case err := <-served:
		close(release)
		t.Fatalf("serve returned with a call in flight: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if got := <-body; got != "finished" {
		t.Errorf("body = %q", got)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the drain")
	}
}

func TestServeReportsAListenFailure(t *testing.T) {
	if err := serve(context.Background(), &http.Server{}, time.Second, func() error { return io.ErrClosedPipe }); err != io.ErrClosedPipe {
		t.Errorf("err = %v", err)
	}
}
