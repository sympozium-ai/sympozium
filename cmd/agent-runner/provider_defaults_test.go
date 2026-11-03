package main

import (
	"net/url"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

// TestHostedDefaultBaseURLsMatchKeyOrigin: the controller decides where a model
// key may go from modelkey.RunOrigin, so every endpoint the runner reaches by
// default must be the origin the controller authorised. A drift would either
// refuse every run of that provider or send a key somewhere unchecked.
func TestHostedDefaultBaseURLsMatchKeyOrigin(t *testing.T) {
	for provider, base := range hostedDefaultBaseURLs {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		got, err := modelkey.RunOrigin(provider, "")
		if err != nil {
			t.Fatalf("RunOrigin(%q): %v", provider, err)
		}
		if want := u.Scheme + "://" + u.Host; got != want {
			t.Errorf("provider %q: runner reaches %s but key origin check expects %s", provider, want, got)
		}
		if got != modelkey.WellKnownOrigin(provider) {
			t.Errorf("provider %q: default origin %s is not its well-known origin %s", provider, got, modelkey.WellKnownOrigin(provider))
		}
	}
}
