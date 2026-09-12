package modelgateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestModelRequestCanonicalBounds(t *testing.T) {
	for _, depth := range []int{64, 65} {
		raw := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		_, _, err := requestDigest([]byte(raw))
		if (err != nil) != (depth > 64) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
	if _, _, err := requestDigest([]byte(strings.Repeat(" ", 262145))); err == nil {
		t.Fatal("oversized model body accepted")
	}
}

func TestSharedModelRequestCanonicalVectors(t *testing.T) {
	raw, err := os.ReadFile("../../test/fixtures/celln-model-requests/v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		APIVersion string `json:"apiVersion"`
		Vectors    []struct {
			Name      string  `json:"name"`
			Request   string  `json:"request"`
			Canonical *string `json:"canonical"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.APIVersion != "celln.sympozium.ai/model-request-conformance-v1" || len(fixture.Vectors) != 11 {
		t.Fatal("unexpected fixture version/count")
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			digest, canonical, err := requestDigest([]byte(vector.Request))
			if vector.Canonical == nil {
				if err == nil {
					t.Fatal("out-of-profile request accepted")
				}
				return
			}
			if err != nil || string(canonical) != *vector.Canonical {
				t.Fatalf("canonical mismatch: %q %v", canonical, err)
			}
			h := sha256.Sum256([]byte(*vector.Canonical))
			if digest != "sha256:"+hex.EncodeToString(h[:]) {
				t.Fatal("canonical digest mismatch")
			}
		})
	}
}
