package harnessgateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const allowLists = `allowedEnvNames: [GOOD_BASE_URL, GOOD_API_KEY, OTHER_BASE_URL, OTHER_API_KEY]
allowedAuthHeaders: [authorization]
`

const goodProfile = `- name: good
  providers: [good]
  upstream: https://good.example
  secretKeys: [API_KEY]
  auth: {type: header, header: Authorization, prefix: "Bearer "}
  paths: [/v1/x]
  env: {baseURL: [GOOD_BASE_URL], apiKey: [GOOD_API_KEY]}
`

func TestParseSetRejectsWhatItMust(t *testing.T) {
	for name, tc := range map[string]struct{ from, to string }{
		"plain http upstream":      {"https://good.example", "http://good.example"},
		"upstream with userinfo":   {"https://good.example", "https://u:p@good.example"},
		"PATH":                     {"GOOD_BASE_URL", "PATH"},
		"LD_PRELOAD":               {"GOOD_API_KEY", "LD_PRELOAD"},
		"HTTPS_PROXY":              {"GOOD_API_KEY", "HTTPS_PROXY"},
		"a Host auth header":       {"header: Authorization", "header: Host"},
		"a Connection auth header": {"header: Authorization", "header: Connection"},
		"an unlisted env name":     {"GOOD_API_KEY", "GOOD_TOKEN"},
		"an unlisted auth header":  {"header: Authorization", "header: X-Key"},
		"an unknown auth.type":     {"type: header", "type: sigv4"},
		"an unknown field":         {"name: good", "name: good\n  future: true"},
		"a path with dot-dot":      {"/v1/x", "/v1/../x"},
		"a partial wildcard":       {"/v1/x", "/v1/x*"},
		"no paths":                 {"paths: [/v1/x]", "paths: []"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := strings.Replace(goodProfile, tc.from, tc.to, 1)
			if bad == goodProfile {
				t.Fatal("the test did not change the profile")
			}
			other := strings.ReplaceAll(goodProfile, "good", "other")
			set, err := ParseSet([]byte(allowLists + "profiles:\n" + indent(bad) + indent(other)))
			if err != nil {
				t.Fatal(err)
			}
			if len(set.Rejected) != 1 {
				t.Errorf("Rejected = %v", set.Rejected)
			}
			if _, err := set.Route("good", ""); err == nil {
				t.Error("a rejected profile's provider must fail closed")
			}
			if _, err := set.Route("other", ""); err != nil {
				t.Errorf("a valid entry was lost: %v", err)
			}
		})
	}
}

func TestNothingIsAllowedUntilListed(t *testing.T) {
	set, err := ParseSet([]byte("profiles:\n" + indent(goodProfile)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Route("good", ""); err == nil {
		t.Error("a profile must be refused when the allowlists are empty")
	}
}

func TestAnOperatorCanListANewName(t *testing.T) {
	extended := strings.Replace(goodProfile, "header: Authorization", "header: X-Key", 1)
	extended = strings.Replace(extended, "GOOD_API_KEY", "NEW_API_KEY", 1)
	file := "allowedEnvNames: [GOOD_BASE_URL, NEW_API_KEY]\nallowedAuthHeaders: [x-key]\nprofiles:\n" + indent(extended)
	set, err := ParseSet([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Route("good", ""); err != nil {
		t.Errorf("%v, rejected: %v", err, set.Rejected)
	}
}

func TestAMalformedAllowlistEntryFailsTheFile(t *testing.T) {
	for _, file := range []string{"allowedEnvNames: [\"not a name\"]\n", "allowedAuthHeaders: [\"bad header\"]\n"} {
		if _, err := ParseSet([]byte(file)); err == nil {
			t.Errorf("%q should fail", file)
		}
	}
}

func TestRoute(t *testing.T) {
	set, err := ParseSet([]byte(allowLists + "insecureOrigins: [\"http://llama.local:8080/\"]\nprofiles:\n" + indent(goodProfile)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Route("missing", ""); err == nil {
		t.Error("a provider with no profile must be refused")
	}
	route, err := set.Route("GOOD", "")
	if err != nil || route.Upstream != "https://good.example" || route.Origin != "https://good.example" || route.Private {
		t.Errorf("route = %+v, err = %v", route, err)
	}
	if _, err := set.Route("good", "http://elsewhere.example"); err == nil {
		t.Error("an unlisted plain http baseURL must be refused")
	}
	route, err = set.Route("good", "http://llama.local:8080/v1")
	if err != nil || !route.Private || route.Origin != "http://llama.local:8080" {
		t.Errorf("route = %+v, err = %v", route, err)
	}
}

func TestPathAllowedWildcardMatchesOneSegment(t *testing.T) {
	profile := &Profile{Paths: []string{"/model/*/converse-stream"}}
	for path, want := range map[string]bool{
		"/model/m/converse-stream":   true,
		"/model/a/b/converse-stream": false,
		"/model//converse-stream":    false,
		"/model/m/converse":          false,
	} {
		if got := profile.PathAllowed(path); got != want {
			t.Errorf("%s = %v", path, got)
		}
	}
}

func TestLoaderPicksUpANewProfileWithNoRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "profiles", Namespace: "sys"},
		Data:       map[string]string{DataKey: "profiles: []\n"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
	loader := &Loader{Reader: c, Name: "profiles", Namespace: "sys", TTL: 10 * time.Millisecond}
	ctx := context.Background()
	set, err := loader.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Route("good", ""); err == nil {
		t.Fatal("the profile should not exist yet")
	}
	cm.Data[DataKey] = allowLists + "profiles:\n" + indent(goodProfile)
	if err := c.Update(ctx, cm); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	set, err = loader.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Route("good", ""); err != nil {
		t.Errorf("the new profile did not take effect: %v", err)
	}
}

func TestLoaderFailsClosedWithoutTheConfigMap(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	loader := &Loader{Reader: fake.NewClientBuilder().WithScheme(scheme).Build(), Name: "profiles", Namespace: "sys"}
	if _, err := loader.Load(context.Background()); err == nil {
		t.Error("a missing ConfigMap must be an error")
	}
}

// A broken override must not leave the entry it meant to replace. That entry
// would send the operator's key somewhere the operator chose to stop using.
func TestARejectedOverrideLeavesItsProviderWithNoProfile(t *testing.T) {
	shipped := strings.Replace(goodProfile, "name: good", "name: shipped", 1)
	provider := "good"
	for name, override := range map[string]string{
		"an invalid auth type": strings.Replace(goodProfile, "type: header", "type: nope", 1),
		"a typo in providers":  strings.Replace(goodProfile, "providers:", "provider:", 1),
	} {
		t.Run(name, func(t *testing.T) {
			named := strings.Replace(override, "name: good", "name: shipped", 1)
			set, err := ParseSet([]byte(allowLists + "profiles:\n" + indent(shipped) + indent(named)))
			if err != nil {
				t.Fatal(err)
			}
			if len(set.Rejected) != 1 {
				t.Fatalf("rejected = %v", set.Rejected)
			}
			var noProfile *NoProfileError
			if _, err := set.Route(provider, ""); !errors.As(err, &noProfile) {
				t.Errorf("Route err = %v, want NoProfileError", err)
			}
		})
	}
	t.Run("a later valid entry still applies", func(t *testing.T) {
		broken := strings.Replace(goodProfile, "type: header", "type: nope", 1)
		set, err := ParseSet([]byte(allowLists + "profiles:\n" + indent(shipped) + indent(broken) + indent(shipped)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := set.Route(provider, ""); err != nil {
			t.Errorf("Route: %v", err)
		}
	})
	t.Run("an entry that names no target fails the whole file", func(t *testing.T) {
		if _, err := ParseSet([]byte(allowLists + "profiles:\n" + indent(shipped) + "  - upstream: https://proxy.example\n")); err == nil {
			t.Error("no way to tell which provider this entry meant, so no profile can be trusted")
		}
		if _, err := ParseSet([]byte(allowLists + "profiles:\n" + indent(shipped) + "  - providers: 7\n")); err == nil {
			t.Error("providers that can't be read must fail the file")
		}
	})
}

func TestPathAllowedRefusesWhatAWildcardMustNotMatch(t *testing.T) {
	profile := &Profile{Paths: []string{"/openai/deployments/*/chat"}}
	if !profile.PathAllowed("/openai/deployments/gpt-4.1/chat") {
		t.Error("an ordinary segment must match")
	}
	for _, segment := range []string{"a?b", "a#b", "a%b", ".", "..", "a?", "?"} {
		if profile.PathAllowed("/openai/deployments/" + segment + "/chat") {
			t.Errorf("%q matched", segment)
		}
	}
}

func TestRouteURLEscapesThePath(t *testing.T) {
	route := &Route{Upstream: "https://example.test/api/v1"}
	for _, tc := range []struct{ path, query, want string }{
		{"/v1/chat", "", "https://example.test/api/v1/chat"},
		{"/v1/chat", "api-version=1", "https://example.test/api/v1/chat?api-version=1"},
		{"/x/a?b/c", "", "https://example.test/api/v1/x/a%3Fb/c"},
		{"/x/a#b", "", "https://example.test/api/v1/x/a%23b"},
	} {
		if got := route.URL(tc.path, tc.query); got != tc.want {
			t.Errorf("URL(%q, %q) = %q, want %q", tc.path, tc.query, got, tc.want)
		}
	}
}
