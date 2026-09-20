package cellnauthority

import (
	"crypto/sha256"
	"fmt"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// The issuer (internal/cellnauthority) and the model gateway
// (internal/modelgateway) each compute route.modelConnectionSpecSha256 and
// must agree. Both packages hold this same table of hand-written canonical
// forms: the first is what a spec hashed to before request policy existed.
func TestConnectionSpecDigestGolden(t *testing.T) {
	base := api.ModelConnectionSpec{Provider: "openai", Protocol: "openai-chat", Endpoint: "https://model.example/v1/chat/completions", SecretRef: "key", Models: []string{"m"}, AllowInsecure: true}
	policy := base
	policy.MaxOutputTokens = 2048
	policy.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{"temperature":0.7,"chat_template_kwargs":{"enable_thinking":false}}`)}
	bound := base
	bound.MaxOutputTokens = 2048
	for _, tc := range []struct {
		name      string
		spec      api.ModelConnectionSpec
		canonical string
	}{
		{"without request policy, as before the fields existed", base,
			`{"allowInsecure":true,"endpoint":"https://model.example/v1/chat/completions","models":["m"],"protocol":"openai-chat","provider":"openai","secretRef":"key"}`},
		{"bound only", bound,
			`{"allowInsecure":true,"endpoint":"https://model.example/v1/chat/completions","maxOutputTokens":2048,"models":["m"],"protocol":"openai-chat","provider":"openai","secretRef":"key"}`},
		{"fractional parameters and bound", policy,
			`{"allowInsecure":true,"endpoint":"https://model.example/v1/chat/completions","maxOutputTokens":2048,"models":["m"],"parameters":"{\"chat_template_kwargs\":{\"enable_thinking\":false},\"temperature\":0.7}","protocol":"openai-chat","provider":"openai","secretRef":"key"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view, err := tc.spec.DigestView()
			if err != nil {
				t.Fatal(err)
			}
			got, err := digestJSON(view)
			if err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(tc.canonical))); got != want {
				t.Fatalf("digest %s, want %s of %s", got, want, tc.canonical)
			}
		})
	}
}
