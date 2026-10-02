package main

import (
	"strings"
	"testing"
)

func TestProbeImageRequiresCompleteImmutableDigest(t *testing.T) {
	good := "localhost:5009/review@sha256:" + strings.Repeat("a", 64)
	if !validProbeImage(good) {
		t.Fatal("valid immutable reference rejected")
	}
	for _, bad := range []string{"image:latest", "image@sha256:", good + "garbage", " image@sha256:" + strings.Repeat("a", 64), "image@sha256:" + strings.Repeat("g", 64)} {
		if validProbeImage(bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
}
func TestJourneySelection(t *testing.T) {
	for _, good := range []string{"all", "direct", "model", "isolation"} {
		if !validJourney(good) {
			t.Fatal(good)
		}
	}
	for _, bad := range []string{"", "release", "installed", "ALL"} {
		if validJourney(bad) {
			t.Fatal(bad)
		}
	}
}
