package apiserver

import (
	"context"
	"github.com/go-logr/logr"
	"io/fs"
	"net"
	"net/http"
	"testing"
	"testing/fstest"
	"time"
)

func TestServeContextJoinsHTTPShutdown(t *testing.T) {
	for _, frontend := range []fs.FS{nil, fstest.MapFS{"index.html": {Data: []byte("proof UI")}}} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		listener.Close()
		ctx, cancel := context.WithCancel(context.Background())
		server := NewServer(nil, nil, nil, logr.Discard())
		done := make(chan error, 1)
		go func() { done <- server.ServeContext(ctx, address, nil, frontend) }()
		client := &http.Client{Timeout: time.Second}
		deadline := time.Now().Add(3 * time.Second)
		for {
			response, err := client.Get("http://" + address + "/")
			if err == nil {
				response.Body.Close()
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("HTTP did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("HTTP shutdown was not joined")
		}
		client.CloseIdleConnections()
		if response, err := client.Get("http://" + address + "/"); err == nil {
			response.Body.Close()
			t.Fatal("HTTP listener survived cancellation")
		}
	}
}
