package main

import "testing"

func TestServeTargetLineNamesContextServerAndNode(t *testing.T) {
	kubectl := func(args ...string) string {
		switch args[0] + " " + args[1] {
		case "config current-context":
			return "kind-dev"
		case "config view":
			return "https://127.0.0.1:6443"
		case "get nodes":
			return "dev-control-plane"
		}
		return ""
	}
	want := "Serving the console for context kind-dev (https://127.0.0.1:6443), node dev-control-plane"
	if got := serveTargetLine(kubectl); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestServeTargetLineToleratesMissingValues(t *testing.T) {
	got := serveTargetLine(func(...string) string { return "" })
	if got != "Serving the console for context unknown (unknown), node unknown" {
		t.Fatalf("got %q", got)
	}
}
