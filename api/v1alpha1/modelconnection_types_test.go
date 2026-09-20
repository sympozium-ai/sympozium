package v1alpha1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func gatewayConnectionSpec() ModelConnectionSpec {
	return ModelConnectionSpec{Provider: "openai", Protocol: "openai-chat", Endpoint: "https://model.example/v1/chat/completions", SecretRef: "key", Models: []string{"m"}}
}

func TestModelConnectionWithoutRequestPolicyIsUnchanged(t *testing.T) {
	spec := gatewayConnectionSpec()
	spec.AllowInsecure = true
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	// The serialized form before parameters and maxOutputTokens existed.
	const before = `{"provider":"openai","protocol":"openai-chat","endpoint":"https://model.example/v1/chat/completions","secretRef":"key","models":["m"],"allowInsecure":true}`
	if string(raw) != before {
		t.Fatalf("serialized spec changed:\n got %s\nwant %s", raw, before)
	}
	view, err := spec.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view, spec) {
		t.Fatalf("digest view of a spec without parameters must be the spec itself, got %#v", view)
	}
	if spec.RequestOutputTokens() != DefaultRequestOutputTokens || DefaultRequestOutputTokens != 512 {
		t.Fatalf("default bound %d", spec.RequestOutputTokens())
	}
}

func TestModelConnectionDigestViewBindsParametersAsText(t *testing.T) {
	spec := gatewayConnectionSpec()
	spec.MaxOutputTokens = 2048
	// Key order and whitespace of the stored object do not matter; number text does.
	spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{ "temperature": 0.7, "chat_template_kwargs": {"enable_thinking": false} }`)}
	view, err := spec.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"endpoint":"https://model.example/v1/chat/completions","maxOutputTokens":2048,"models":["m"],"parameters":"{\"chat_template_kwargs\":{\"enable_thinking\":false},\"temperature\":0.7}","protocol":"openai-chat","provider":"openai","secretRef":"key"}`
	if string(raw) != want {
		t.Fatalf("digest view\n got %s\nwant %s", raw, want)
	}
	other := spec
	other.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{"temperature":0.8,"chat_template_kwargs":{"enable_thinking":false}}`)}
	otherView, err := other.DigestView()
	if err != nil {
		t.Fatal(err)
	}
	if otherRaw, _ := json.Marshal(otherView); string(otherRaw) == string(raw) {
		t.Fatal("changed parameters left the digest view unchanged")
	}
}

func TestModelConnectionRequestPolicyValidation(t *testing.T) {
	deep := `{"a":{"b":{"c":{"d":1}}}}`
	for _, tc := range []struct {
		name       string
		parameters string
		tokens     int64
		profile    bool
		refused    string
	}{
		{"omitted", "", 0, false, ""},
		{"fractional and nested parameters", `{"temperature":0.7,"chat_template_kwargs":{"enable_thinking":false}}`, 0, false, ""},
		{"empty object", `{}`, 0, false, ""},
		{"minimum bound", "", 256, false, ""},
		{"maximum bound", "", 4096, false, ""},
		{"below minimum bound", "", 255, false, "256–4096"},
		{"above maximum bound", "", 4097, false, "256–4096"},
		{"negative bound", "", -1, false, "256–4096"},
		{"reserved max_tokens", `{"max_tokens":100000}`, 0, false, "reserved"},
		{"reserved model", `{"model":"other"}`, 0, false, "reserved"},
		{"reserved messages", `{"messages":[]}`, 0, false, "reserved"},
		{"reserved stream", `{"stream":true}`, 0, false, "reserved"},
		{"reserved tools", `{"tools":[]}`, 0, false, "reserved"},
		{"uppercase key", `{"Temperature":1}`, 0, false, "must match"},
		{"too deep", deep, 0, false, "nests deeper"},
		{"null value", `{"seed":null}`, 0, false, "null"},
		{"not an object", `[1]`, 0, false, "JSON object is required"},
		{"null", `null`, 0, false, "JSON object is required"},
		{"too large", `{"a":"` + strings.Repeat("x", 250) + `","b":"` + strings.Repeat("x", 250) + `","c":"` + strings.Repeat("x", 250) + `","d":"` + strings.Repeat("x", 250) + `","e":"` + strings.Repeat("x", 250) + `","f":"` + strings.Repeat("x", 250) + `","g":"` + strings.Repeat("x", 250) + `","h":"` + strings.Repeat("x", 250) + `","i":"` + strings.Repeat("x", 250) + `"}`, 0, false, "exceeds 2048"},
		{"parameters with a host credential profile", `{"seed":1}`, 0, true, "host credential profile"},
		{"bound with a host credential profile", "", 1024, true, "host credential profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := gatewayConnectionSpec()
			if tc.profile {
				spec.SecretRef, spec.CredentialProfile = "", "profile"
			}
			spec.MaxOutputTokens = tc.tokens
			if tc.parameters != "" {
				spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(tc.parameters)}
			}
			err := spec.Validate()
			if tc.refused == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("want refusal naming %q, got %v", tc.refused, err)
			}
			if _, viewErr := spec.DigestView(); tc.parameters != "" && !tc.profile && viewErr == nil {
				t.Fatal("digest view accepted invalid parameters")
			}
		})
	}
}
