package main

import (
	"net"
	"testing"
)

func TestCheckOTelEndpoint_Unreachable(t *testing.T) {
	// Port 1 is almost certainly not listening.
	if checkOTelEndpoint("127.0.0.1:1") {
		t.Error("expected unreachable endpoint to return false")
	}
}

func TestCheckOTelEndpoint_Reachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	if !checkOTelEndpoint(ln.Addr().String()) {
		t.Error("expected reachable endpoint to return true")
	}
}

func TestCheckOTelEndpoint_StripScheme(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	if !checkOTelEndpoint("http://" + ln.Addr().String()) {
		t.Error("expected reachable endpoint with http:// prefix to return true")
	}
}
