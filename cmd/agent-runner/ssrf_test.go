package main

import (
	"context"
	"strings"
	"testing"
)

// TestSSRFGuardedDialContext_BlocksInternal verifies the fetch_url dial guard
// rejects loopback/private/link-local targets (IP literals resolve without
// network I/O, so no real connection is attempted).
func TestSSRFGuardedDialContext_BlocksInternal(t *testing.T) {
	t.Setenv("SYMPOZIUM_FETCH_URL_ALLOW_PRIVATE", "")
	dial := ssrfGuardedDialContext()

	blocked := []string{
		"127.0.0.1:80",       // loopback v4
		"[::1]:80",           // loopback v6
		"10.0.0.5:6443",      // private (kube-apiserver-ish)
		"192.168.1.1:80",     // private
		"169.254.169.254:80", // link-local (cloud metadata)
		"0.0.0.0:80",         // unspecified
	}
	for _, addr := range blocked {
		_, err := dial(context.Background(), "tcp", addr)
		if err == nil {
			t.Errorf("dial(%q) succeeded, want rejection", addr)
			continue
		}
		if !strings.Contains(err.Error(), "disallowed") {
			t.Errorf("dial(%q) error = %q, want a 'disallowed address' rejection", addr, err.Error())
		}
	}
}

// TestSSRFGuardedDialContext_OptOut verifies the escape hatch returns a plain
// dialer (no address filtering) when explicitly enabled.
func TestSSRFGuardedDialContext_OptOut(t *testing.T) {
	t.Setenv("SYMPOZIUM_FETCH_URL_ALLOW_PRIVATE", "true")
	dial := ssrfGuardedDialContext()

	// With the guard disabled, dialing loopback should not be rejected on
	// address grounds; it will fail only if nothing is listening. Use a port
	// that is almost certainly closed and assert the error, if any, is not our
	// "disallowed" rejection.
	_, err := dial(context.Background(), "tcp", "127.0.0.1:9")
	if err != nil && strings.Contains(err.Error(), "disallowed") {
		t.Errorf("opt-out dialer still rejected loopback as disallowed: %v", err)
	}
}
