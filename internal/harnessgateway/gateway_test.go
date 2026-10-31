package harnessgateway

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

const (
	testToken = "run-token-0123456789"
	testKey   = "sk-real/provider-key-0123456789"
)

type staticProfiles struct{ set *Set }

func (s staticProfiles) Load(context.Context) (*Set, error) { return s.set, nil }

// shippedProfiles returns what the chart ships, with extra entries appended.
func shippedProfiles(t *testing.T, privateOrigin, extra string) *Set {
	t.Helper()
	shipped, err := os.ReadFile("../../charts/sympozium/files/harness-gateway/profiles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := os.ReadFile("../../charts/sympozium/files/harness-gateway/allowed.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// The tests' own extra profiles use names an operator would add.
	file := string(allowed) + "insecureOrigins: [\"" + privateOrigin + "\"]\nprofiles:\n" + indent(string(shipped)) + indent(extra)
	file = strings.Replace(file, "allowedEnvNames:\n", "allowedEnvNames:\n  - AZURE_OPENAI_ENDPOINT\n  - AZURE_OPENAI_API_KEY\n", 1)
	set, err := ParseSet([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Rejected) != 0 {
		t.Fatalf("rejected: %v", set.Rejected)
	}
	return set
}

func indent(s string) string {
	if s == "" {
		return ""
	}
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ") + "\n"
}

type fixture struct {
	run    *api.AgentRun
	token  *corev1.Secret
	key    *corev1.Secret
	agent  *api.Agent
	extras []client.Object
}

func newFixture(provider, secretKey, upstream string) *fixture {
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "ns", UID: "agent-uid"}}
	run := &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns", UID: "run-uid"},
		Spec: api.AgentRunSpec{
			AgentRef: "agent",
			Model:    api.ModelSpec{Provider: provider, Model: "m", BaseURL: upstream, AuthSecretRef: "key"},
		},
		Status: api.AgentRunStatus{Phase: api.AgentRunPhaseRunning},
	}
	token := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: RunTokenSecretName("run"), Namespace: "ns",
			Annotations: map[string]string{
				AgentUIDAnnotation:         "agent-uid",
				KeySecretUIDAnnotation:     "key-uid",
				HeadersSecretUIDAnnotation: "headers-uid",
				OriginAnnotation:           upstreamOrigin(upstream),
			},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "sympozium.ai/v1alpha1", Kind: "AgentRun", Name: "run", UID: "run-uid", Controller: ptr.To(true)}},
		},
		Data: map[string][]byte{TokenSecretKey: []byte(testToken)},
	}
	key := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "ns", UID: "key-uid", Annotations: map[string]string{
			modelkey.OwnerAnnotation:  modelkey.Owner(agent),
			modelkey.OriginAnnotation: upstreamOrigin(upstream),
		}},
		Data: map[string][]byte{secretKey: []byte(testKey)},
	}
	return &fixture{run: run, token: token, key: key, agent: agent}
}

func upstreamOrigin(upstream string) string {
	scheme, rest, _ := strings.Cut(upstream, "://")
	host, _, _ := strings.Cut(rest, "/")
	return scheme + "://" + host
}

func (f *fixture) serve(t *testing.T, set *Set) *httptest.Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := append([]client.Object{f.run, f.token, f.key, f.agent}, f.extras...)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(f.run).Build()
	if err := reader.Status().Update(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&Gateway{Reader: reader, Profiles: staticProfiles{set}}).Handler())
	t.Cleanup(server.Close)
	return server
}

func call(t *testing.T, gateway *httptest.Server, path string, header map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, gateway.URL+path, strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range header {
		req.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	return response, string(body)
}

type seen struct {
	path, query string
	header      http.Header
}

func recordingUpstream(t *testing.T, reply string) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.query, got.header = r.URL.Path, r.URL.RawQuery, r.Header.Clone()
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(upstream.Close)
	return upstream, got
}

func TestShippedProfilesSwapTheTokenForTheKey(t *testing.T) {
	for _, tc := range []struct {
		provider, secretKey, path string
		tokenHeader, tokenPrefix  string
		keyHeader, keyPrefix      string
	}{
		{"anthropic", "ANTHROPIC_API_KEY", "/v1/messages", "x-api-key", "", "X-Api-Key", ""},
		{"openai", "OPENAI_API_KEY", "/v1/chat/completions", "Authorization", "Bearer ", "Authorization", "Bearer "},
		{"openai", "API_KEY", "/v1/responses", "Authorization", "Bearer ", "Authorization", "Bearer "},
		{"deepseek", "DEEPSEEK_API_KEY", "/v1/chat/completions", "Authorization", "Bearer ", "Authorization", "Bearer "},
	} {
		t.Run(tc.provider+tc.path, func(t *testing.T) {
			upstream, got := recordingUpstream(t, "ok")
			f := newFixture(tc.provider, tc.secretKey, upstream.URL)
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
			response, body := call(t, gateway, "/runs/ns/run"+tc.path, map[string]string{
				tc.tokenHeader: tc.tokenPrefix + testToken, "anthropic-version": "2023-06-01", "Cookie": "c=1",
			})
			if response.StatusCode != http.StatusOK || body != "ok" {
				t.Fatalf("status=%d body=%q", response.StatusCode, body)
			}
			if got.path != tc.path {
				t.Errorf("path = %q", got.path)
			}
			if want := tc.keyPrefix + testKey; got.header.Get(tc.keyHeader) != want {
				t.Errorf("%s = %q, want the real key", tc.keyHeader, got.header.Get(tc.keyHeader))
			}
			for name, values := range got.header {
				if strings.Contains(strings.Join(values, ","), testToken) {
					t.Errorf("the run token reached the upstream in %s", name)
				}
			}
			if got.header.Get("Cookie") != "" {
				t.Error("Cookie was forwarded")
			}
			if got.header.Get("Anthropic-Version") != "2023-06-01" {
				t.Error("an ordinary header was not forwarded")
			}
		})
	}
}

func bearer() map[string]string { return map[string]string{"Authorization": "Bearer " + testToken} }

func TestPhases(t *testing.T) {
	for phase, want := range map[api.AgentRunPhase]int{
		"":                                http.StatusOK,
		api.AgentRunPhasePending:          http.StatusOK,
		api.AgentRunPhaseRunning:          http.StatusOK,
		api.AgentRunPhaseServing:          http.StatusForbidden,
		api.AgentRunPhasePostRunning:      http.StatusForbidden,
		api.AgentRunPhaseAwaitingDelegate: http.StatusForbidden,
		api.AgentRunPhaseAwaitingGate:     http.StatusForbidden,
		api.AgentRunPhaseSucceeded:        http.StatusForbidden,
		api.AgentRunPhaseFailed:           http.StatusForbidden,
		api.AgentRunPhaseSkipped:          http.StatusForbidden,
	} {
		t.Run(string(phase), func(t *testing.T) {
			upstream, got := recordingUpstream(t, "ok")
			f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
			f.run.Status.Phase = phase
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
			response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer())
			if response.StatusCode != want {
				t.Fatalf("status = %d, want %d", response.StatusCode, want)
			}
			if want != http.StatusOK && got.path != "" {
				t.Error("a refused call reached the upstream")
			}
		})
	}
}

func TestTokenRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*fixture)
		header map[string]string
		path   string
	}{
		"no token":    {header: nil},
		"wrong token": {header: map[string]string{"Authorization": "Bearer nope"}},
		"a run with no token Secret": {
			mutate: func(f *fixture) { f.token.Name = "something-else" },
			header: bearer(),
		},
		"a token Secret the run does not control": {
			mutate: func(f *fixture) { f.token.OwnerReferences = nil },
			header: bearer(),
		},
		"another run's token": {
			mutate: func(f *fixture) {
				f.extras = append(f.extras, &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", UID: "other"}})
			},
			header: bearer(),
			path:   "/runs/ns/other/v1/chat/completions",
		},
		"a missing run": {header: bearer(), path: "/runs/ns/missing/v1/chat/completions"},
		"a bad name":    {header: bearer(), path: "/runs/NS/run/v1/chat/completions"},
	} {
		t.Run(name, func(t *testing.T) {
			upstream, got := recordingUpstream(t, "ok")
			f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
			path := tc.path
			if path == "" {
				path = "/runs/ns/run/v1/chat/completions"
			}
			response, _ := call(t, gateway, path, tc.header)
			if response.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d", response.StatusCode)
			}
			if got.path != "" {
				t.Error("a refused call reached the upstream")
			}
		})
	}
}

func TestPathAllowlist(t *testing.T) {
	for _, tc := range []struct{ provider, secretKey, path string }{
		{"anthropic", "ANTHROPIC_API_KEY", "/v1/chat/completions"},
		{"openai", "OPENAI_API_KEY", "/v1/messages"},
		{"openai", "OPENAI_API_KEY", "/v1/files"},
		{"openai", "OPENAI_API_KEY", "/v1/chat%2Fcompletions"},
	} {
		t.Run(tc.provider+tc.path, func(t *testing.T) {
			upstream, got := recordingUpstream(t, "ok")
			f := newFixture(tc.provider, tc.secretKey, upstream.URL)
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
			response, _ := call(t, gateway, "/runs/ns/run"+tc.path, bearer())
			if response.StatusCode != http.StatusForbidden || got.path != "" {
				t.Errorf("status = %d, upstream saw %q", response.StatusCode, got.path)
			}
		})
	}
}

func TestProfilesAddedAsDataReachTheirProviders(t *testing.T) {
	for _, tc := range []struct {
		name, provider, extra, suffix, path string
		wantPath, wantQuery                 string
		keyHeader, wantKey                  string
	}{
		{
			name: "OpenRouter", provider: "openrouter", suffix: "/api",
			extra: `- name: openrouter
  providers: [openrouter]
  upstream: https://openrouter.ai/api
  secretKeys: [OPENROUTER_API_KEY]
  auth: {type: header, header: Authorization, prefix: "Bearer "}
  paths: [/v1/chat/completions]
  env: {baseURL: [OPENAI_BASE_URL], apiKey: [OPENAI_API_KEY]}
  clientBasePath: /v1
`,
			path: "/v1/chat/completions", wantPath: "/api/v1/chat/completions",
			keyHeader: "Authorization", wantKey: "Bearer " + testKey,
		},
		{
			name: "Azure", provider: "azure-openai",
			extra: `- name: azure
  providers: [azure-openai]
  upstream: https://example.openai.azure.com
  secretKeys: [OPENROUTER_API_KEY]
  auth: {type: header, header: Api-Key}
  paths: [/openai/deployments/*/chat/completions]
  env: {baseURL: [AZURE_OPENAI_ENDPOINT], apiKey: [AZURE_OPENAI_API_KEY]}
`,
			path: "/openai/deployments/gpt/chat/completions?api-version=2024-06-01", wantPath: "/openai/deployments/gpt/chat/completions", wantQuery: "api-version=2024-06-01",
			keyHeader: "Api-Key", wantKey: testKey,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, got := recordingUpstream(t, "ok")
			f := newFixture(tc.provider, "OPENROUTER_API_KEY", upstream.URL+tc.suffix)
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, tc.extra))
			response, body := call(t, gateway, "/runs/ns/run"+tc.path, bearer())
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d body=%s", response.StatusCode, body)
			}
			if got.path != tc.wantPath || got.query != tc.wantQuery {
				t.Errorf("upstream saw %q?%q", got.path, got.query)
			}
			if got.header.Get(tc.keyHeader) != tc.wantKey {
				t.Errorf("%s = %q", tc.keyHeader, got.header.Get(tc.keyHeader))
			}
		})
	}
}

func TestBaseURLWithAndWithoutV1(t *testing.T) {
	for _, suffix := range []string{"", "/v1", "/v1/"} {
		upstream, got := recordingUpstream(t, "ok")
		f := newFixture("openai", "OPENAI_API_KEY", upstream.URL+suffix)
		gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
		if response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer()); response.StatusCode != http.StatusOK {
			t.Fatalf("%q: status = %d", suffix, response.StatusCode)
		}
		if got.path != "/v1/chat/completions" {
			t.Errorf("%q: upstream saw %q", suffix, got.path)
		}
	}
}

func TestPrivateOriginMustBeListed(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, "https://elsewhere.example", ""))
	response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer())
	if response.StatusCode != http.StatusForbidden || got.path != "" {
		t.Fatalf("an unlisted origin: status = %d, upstream saw %q", response.StatusCode, got.path)
	}
	f = newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway = f.serve(t, shippedProfiles(t, upstream.URL, ""))
	if response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer()); response.StatusCode != http.StatusOK {
		t.Fatalf("a listed origin: status = %d", response.StatusCode)
	}
}

func TestAKeyGoesOnlyToItsPinnedOrigin(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	f.key.Annotations[modelkey.OriginAnnotation] = "https://api.openai.com"
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer())
	if response.StatusCode != http.StatusForbidden || got.path != "" {
		t.Fatalf("status = %d, upstream saw %q", response.StatusCode, got.path)
	}
}

func TestAnAgentThatLostTheKeyIsRefused(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	f.key.Annotations[modelkey.OwnerAnnotation] = "Agent/other"
	f.extras = append(f.extras, &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns"}})
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer())
	if response.StatusCode != http.StatusForbidden || got.path != "" {
		t.Fatalf("status = %d, upstream saw %q", response.StatusCode, got.path)
	}
}

func TestARequestCannotNameTheUpstream(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request's choice of upstream was used")
	}))
	defer elsewhere.Close()
	header := bearer()
	header["X-Forwarded-Host"] = elsewhere.Listener.Addr().String()
	header["X-Upstream"] = elsewhere.URL
	response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions?upstream="+elsewhere.URL, header)
	if response.StatusCode != http.StatusOK || got.path == "" {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got.header.Get("X-Forwarded-Host") != "" {
		t.Error("X-Forwarded-Host was forwarded")
	}
}

func TestARedirectIsRefused(t *testing.T) {
	elsewhere, got := recordingUpstream(t, "secret")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer())
	if response.StatusCode != http.StatusBadGateway || got.path != "" {
		t.Fatalf("status = %d, redirect target saw %q", response.StatusCode, got.path)
	}
}

func TestProviderHeaders(t *testing.T) {
	const headerSecretValue = "provider-header-secret-value"
	build := func(reply string) (*httptest.Server, *seen, *fixture) {
		upstream, got := recordingUpstream(t, reply)
		f := newFixture("anthropic", "ANTHROPIC_API_KEY", upstream.URL)
		f.run.Spec.Model.ProviderHeaders = map[string]string{"X-Inline": "inline", "x-api-key": "stolen", "Host": "evil.example", "X-Shared": "from-inline"}
		f.run.Spec.Model.ProviderHeadersSecretRef = "headers"
		f.extras = append(f.extras, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "headers", Namespace: "ns", UID: "headers-uid", Annotations: map[string]string{modelkey.OwnerAnnotation: modelkey.Owner(f.agent)}},
			Data:       map[string][]byte{"X-Secret": []byte(headerSecretValue), "X-Shared": []byte("from-secret")},
		})
		return f.serve(t, shippedProfiles(t, upstream.URL, "")), got, f
	}

	gateway, got, _ := build("ok")
	response, _ := call(t, gateway, "/runs/ns/run/v1/messages", map[string]string{"x-api-key": testToken, "X-Inline": "from-the-model"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got.header.Get("X-Inline") != "inline" || got.header.Get("X-Secret") != headerSecretValue || got.header.Get("X-Shared") != "from-secret" {
		t.Errorf("headers = %v", got.header)
	}
	if got.header.Get("X-Api-Key") != testKey {
		t.Errorf("a provider header replaced the key: %q", got.header.Get("X-Api-Key"))
	}

	gateway, _, _ = build("echo " + headerSecretValue)
	response, body := call(t, gateway, "/runs/ns/run/v1/messages", map[string]string{"x-api-key": testToken})
	if response.StatusCode != http.StatusBadGateway || strings.Contains(body, headerSecretValue) {
		t.Errorf("an echoed header secret: status=%d body=%q", response.StatusCode, body)
	}
}

func TestAnEchoedKeyIsRefused(t *testing.T) {
	half := len(testKey) / 2
	filler := strings.Repeat("x", 4096)
	for name, tc := range map[string]struct {
		handler     http.HandlerFunc
		cleanStatus bool
	}{
		"whole, in the first chunk": {
			handler:     func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{\"k\":\""+testKey+"\"}") },
			cleanStatus: true,
		},
		"split across two chunks, mid-stream": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, filler+testKey[:half])
				w.(http.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
				_, _ = io.WriteString(w, testKey[half:]+"after")
			},
		},
		"in a relayed response header": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Request-Id", testKey)
				_, _ = io.WriteString(w, "ok")
			},
			cleanStatus: true,
		},
		"JSON-escaped": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, strings.ReplaceAll(testKey, "/", `\/`))
			},
			cleanStatus: true,
		},
		"compressed": {
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
					t.Error("expected the transport to negotiate gzip")
				}
				w.Header().Set("Content-Encoding", "gzip")
				zipped := gzip.NewWriter(w)
				_, _ = io.WriteString(zipped, testKey)
				_ = zipped.Close()
			},
			cleanStatus: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(tc.handler)
			defer upstream.Close()
			f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
			req, _ := http.NewRequest(http.MethodPost, gateway.URL+"/runs/ns/run/v1/chat/completions", nil)
			req.Header.Set("Authorization", "Bearer "+testToken)
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				if tc.cleanStatus {
					t.Fatal(err)
				}
				return // The connection broke before any headers: refused.
			}
			defer func() { _ = response.Body.Close() }()
			body, readErr := io.ReadAll(response.Body)
			if strings.Contains(string(body), testKey) || strings.Contains(string(body), testKey[:half+1]) {
				t.Errorf("the key reached the client: %q", body)
			}
			if tc.cleanStatus {
				if response.StatusCode != http.StatusBadGateway {
					t.Errorf("status = %d", response.StatusCode)
				}
			} else if readErr == nil {
				t.Error("a refused stream must break, so it can't look complete")
			}
		})
	}
}

func TestACleanResponseIsNotTruncatedByTheHoldBack(t *testing.T) {
	reply := "short"
	upstream, _ := recordingUpstream(t, reply)
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	if _, body := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer()); body != reply {
		t.Errorf("body = %q", body)
	}
}

func TestStreamingFlushesEachEvent(t *testing.T) {
	release := make(chan struct{})
	first := "event: one\ndata: " + strings.Repeat("a", 200) + "\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, first)
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "event: two\ndata: b\n\n")
	}))
	defer upstream.Close()
	defer close(release)
	f := newFixture("anthropic", "ANTHROPIC_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	req, _ := http.NewRequest(http.MethodPost, gateway.URL+"/runs/ns/run/v1/messages", nil)
	req.Header.Set("x-api-key", testToken)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("the first event did not arrive before the upstream finished: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type = %q", response.Header.Get("Content-Type"))
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "event: one\n" {
		t.Errorf("line = %q, err = %v", line, err)
	}
}

func TestOnlyListedResponseHeadersAreRelayed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Anthropic-Ratelimit-Requests-Remaining", "1")
		w.Header().Set("Set-Cookie", "session=1")
		w.Header().Set("X-Debug", testKey)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"slow down"}`)
	}))
	defer upstream.Close()
	f := newFixture("anthropic", "ANTHROPIC_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	response, body := call(t, gateway, "/runs/ns/run/v1/messages", map[string]string{"x-api-key": testToken})
	if response.StatusCode != http.StatusTooManyRequests || body != `{"error":"slow down"}` {
		t.Fatalf("status=%d body=%q", response.StatusCode, body)
	}
	for header, want := range map[string]string{
		"Retry-After": "7", "Anthropic-Ratelimit-Requests-Remaining": "1", "Content-Type": "application/json",
		"Set-Cookie": "", "X-Debug": "", "Cache-Control": "no-store",
	} {
		if got := response.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestProviderAuthErrorBodiesAreHidden(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusProxyAuthRequired} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":"Incorrect API key provided: sk-re***6789"}`)
		}))
		f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
		gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
		response, body := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer())
		upstream.Close()
		if response.StatusCode != status || strings.Contains(body, "sk-re") {
			t.Errorf("%d: status=%d body=%q", status, response.StatusCode, body)
		}
	}
}

func TestATokenIsBoundToWhatItWasMintedFor(t *testing.T) {
	for name, mutate := range map[string]func(*fixture){
		"a recreated key Secret": func(f *fixture) { f.key.UID = "other" },
		"a recreated Agent":      func(f *fixture) { f.agent.UID = "other" },
		"a token for another origin": func(f *fixture) {
			f.token.Annotations[OriginAnnotation] = "https://api.openai.com"
		},
		"a token with no binding": func(f *fixture) { f.token.Annotations = nil },
		"a key owned by another Agent": func(f *fixture) {
			f.key.Annotations[modelkey.OwnerAnnotation] = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			upstream, got := recordingUpstream(t, "ok")
			f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
			mutate(f)
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
			response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer())
			if response.StatusCode != http.StatusForbidden || got.path != "" {
				t.Errorf("status = %d, upstream saw %q", response.StatusCode, got.path)
			}
		})
	}
}

func TestPlainHTTPNeedsTheInsecureList(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	shipped, err := os.ReadFile("../../charts/sympozium/files/harness-gateway/profiles.yaml")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := os.ReadFile("../../charts/sympozium/files/harness-gateway/allowed.yaml")
	if err != nil {
		t.Fatal(err)
	}
	set, err := ParseSet([]byte(string(allowed) + "privateOrigins: [\"" + upstream.URL + "\"]\nprofiles:\n" + indent(string(shipped))))
	if err != nil {
		t.Fatal(err)
	}
	gateway := f.serve(t, set)
	response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer())
	if response.StatusCode != http.StatusForbidden || got.path != "" {
		t.Errorf("a private listing must not allow a key over plain HTTP: status = %d", response.StatusCode)
	}
}

// The upstream holds the call open. Only the run's phase may end it.
func TestACallEndsWhenItsRunStopsAllowingModelCalls(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("a", 200))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer upstream.CloseClientConnections()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.run, f.token, f.key, f.agent).WithStatusSubresource(f.run).Build()
	ctx := context.Background()
	if err := reader.Status().Update(ctx, f.run); err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer((&Gateway{Reader: reader, Profiles: staticProfiles{shippedProfiles(t, upstream.URL, "")}, RunWatchInterval: 10 * time.Millisecond}).Handler())
	defer gateway.Close()

	req, _ := http.NewRequest(http.MethodPost, gateway.URL+"/runs/ns/run/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(response.Body)
		read <- err
	}()
	select {
	case <-read:
		t.Fatal("the call ended while the run was still Running")
	case <-time.After(100 * time.Millisecond):
	}
	f.run.Status.Phase = api.AgentRunPhaseAwaitingGate
	if err := reader.Status().Update(ctx, f.run); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-read:
		if err == nil {
			t.Error("a call cut short must not look complete")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call outlived its run's phase")
	}
}

func TestMaxConcurrent(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
	}))
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.run, f.token, f.key, f.agent).WithStatusSubresource(f.run).Build()
	if err := reader.Status().Update(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer((&Gateway{Reader: reader, Profiles: staticProfiles{shippedProfiles(t, upstream.URL, "")}, MaxConcurrent: 1}).Handler())
	defer gateway.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest(http.MethodPost, gateway.URL+"/runs/ns/run/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		if response, err := http.DefaultClient.Do(req); err == nil {
			_ = response.Body.Close()
		}
	}()
	<-started
	response, _ := call(t, gateway, "/runs/ns/run/v1/chat/completions", bearer())
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d", response.StatusCode)
	}
	close(release)
	<-done
}

// Every run holds a stream open at the same time. Each call must carry its own
// run's key and get its own run's reply, and no call may wait for another.
func TestConcurrentCallsStreamSideBySide(t *testing.T) {
	const runs = 8
	padding := "event: one\ndata: " + strings.Repeat("a", 200) + "\n\n"
	release := make(chan struct{})
	var mu sync.Mutex
	authByRun := map[string]string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		run := string(body)
		mu.Lock()
		authByRun[run] = r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = io.WriteString(w, padding)
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "event: two\ndata: "+run+"\n\n")
	}))
	defer upstream.Close()

	base := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	objects := []client.Object{base.agent}
	token := func(i int) string { return fmt.Sprintf("run-token-%d-0123456789", i) }
	key := func(i int) string { return fmt.Sprintf("sk-real-key-%d-0123456789", i) }
	for i := range runs {
		name := fmt.Sprintf("run-%d", i)
		run := base.run.DeepCopy()
		run.Name, run.UID = name, types.UID(name+"-uid")
		run.Spec.Model.AuthSecretRef = name + "-key"
		runToken := base.token.DeepCopy()
		runToken.Name = RunTokenSecretName(name)
		runToken.Annotations[KeySecretUIDAnnotation] = name + "-key-uid"
		runToken.OwnerReferences[0].Name, runToken.OwnerReferences[0].UID = name, run.UID
		runToken.Data[TokenSecretKey] = []byte(token(i))
		runKey := base.key.DeepCopy()
		runKey.Name, runKey.UID = name+"-key", types.UID(name+"-key-uid")
		runKey.Data["OPENAI_API_KEY"] = []byte(key(i))
		objects = append(objects, run, runToken, runKey)
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	subject := &Gateway{Reader: reader, Profiles: staticProfiles{shippedProfiles(t, upstream.URL, "")}, MaxConcurrent: runs}
	gateway := httptest.NewServer(subject.Handler())
	defer gateway.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The upstream finishes no stream until every call has its first event. If
	// calls ran one at a time, the first would never get its second event.
	var streaming, finished sync.WaitGroup
	streaming.Add(runs)
	finished.Add(runs)
	for i := range runs {
		go func() {
			defer finished.Done()
			run := fmt.Sprintf("run-%d", i)
			var once sync.Once
			opened := func() { once.Do(streaming.Done) }
			defer opened()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/runs/ns/"+run+"/v1/chat/completions", strings.NewReader(run))
			if err != nil {
				t.Error(err)
				return
			}
			req.Header.Set("Authorization", "Bearer "+token(i))
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("%s: %v", run, err)
				return
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				t.Errorf("%s: status = %d", run, response.StatusCode)
				return
			}
			stream := bufio.NewReader(response.Body)
			if line, err := stream.ReadString('\n'); err != nil || line != "event: one\n" {
				t.Errorf("%s: first line = %q, err = %v", run, line, err)
			}
			opened()
			rest, err := io.ReadAll(stream)
			if err != nil {
				t.Errorf("%s: %v", run, err)
			}
			if want := "event: two\ndata: " + run + "\n\n"; !strings.HasSuffix(string(rest), want) {
				t.Errorf("%s: got another run's reply: %q", run, rest)
			}
		}()
	}
	streaming.Wait()
	close(release)
	finished.Wait()

	gateway.Close() // waits for the handlers, so inFlight has settled
	if got := subject.inFlight.Load(); got != 0 {
		t.Errorf("inFlight = %d", got)
	}
	for i := range runs {
		run := fmt.Sprintf("run-%d", i)
		if got, want := authByRun[run], "Bearer "+key(i); got != want {
			t.Errorf("%s: upstream saw Authorization %q, want %q", run, got, want)
		}
	}
}

// Concurrent callers share one read of the profiles.
func TestLoaderReadsOnceForConcurrentCallers(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "profiles", Namespace: "system"},
		Data:       map[string]string{DataKey: "profiles: []\n"},
	}
	var reads atomic.Int64
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			reads.Add(1)
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	loader := &Loader{Reader: reader, Name: "profiles", Namespace: "system", TTL: time.Minute}
	var callers sync.WaitGroup
	for range 32 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if _, err := loader.Load(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	callers.Wait()
	if got := reads.Load(); got != 1 {
		t.Errorf("reads = %d", got)
	}
}

func TestAHungUpCallerDoesNotFailTheNextCall(t *testing.T) {
	loader := &Loader{Reader: profilesReader(t, func(ctx context.Context) error { return ctx.Err() }), Name: "profiles", Namespace: "system", TTL: time.Minute}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = loader.Load(gone)
	if _, err := loader.Load(context.Background()); err != nil {
		t.Errorf("the next call got %v", err)
	}
}

// A failed read says nothing about the next one. It must not refuse calls for a
// whole TTL.
func TestAFailedProfileReadIsNotCached(t *testing.T) {
	var reads atomic.Int64
	loader := &Loader{Reader: profilesReader(t, func(context.Context) error {
		if reads.Add(1) == 1 {
			return errors.New("api server blip")
		}
		return nil
	}), Name: "profiles", Namespace: "system", TTL: time.Minute}
	if _, err := loader.Load(context.Background()); err == nil {
		t.Fatal("the failed read was not reported")
	}
	if _, err := loader.Load(context.Background()); err != nil {
		t.Errorf("the next call got %v", err)
	}
}

// A caller waiting on someone else's slow read can still hang up.
func TestACallerCanLeaveWhileAReadIsSlow(t *testing.T) {
	release, reading := make(chan struct{}), make(chan struct{})
	var once sync.Once
	loader := &Loader{Reader: profilesReader(t, func(context.Context) error {
		once.Do(func() { close(reading) })
		<-release
		return nil
	}), Name: "profiles", Namespace: "system", TTL: time.Minute}
	defer close(release)
	go func() { _, _ = loader.Load(context.Background()) }()
	<-reading
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	left := make(chan error, 1)
	go func() {
		_, err := loader.Load(ctx)
		left <- err
	}()
	select {
	case err := <-left:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the caller stayed queued behind the read")
	}
}

// A read that never answers must end, or the caller behind it waits forever.
func TestAProfileReadHasATimeLimit(t *testing.T) {
	loader := &Loader{Reader: profilesReader(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}), Name: "profiles", Namespace: "system", TTL: time.Minute, ReadTimeout: 50 * time.Millisecond}
	done := make(chan error, 1)
	go func() {
		_, err := loader.Load(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the stuck read was not reported")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read never ended")
	}
}

// One run holding every slot would refuse every other run.
func TestOneRunCannotTakeEverySlot(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{}, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		started <- struct{}{}
		<-release
	}))
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	otherToken := f.token.DeepCopy()
	otherToken.Name = RunTokenSecretName("other")
	otherToken.OwnerReferences[0].Name, otherToken.OwnerReferences[0].UID = "other", "other-uid"
	otherToken.Data[TokenSecretKey] = []byte("other-token-0123456789")
	other := f.run.DeepCopy()
	other.Name, other.UID = "other", "other-uid"
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.run, f.token, f.key, f.agent, other, otherToken).Build()
	subject := &Gateway{Reader: reader, Profiles: staticProfiles{shippedProfiles(t, upstream.URL, "")}, MaxConcurrent: 2, MaxConcurrentPerRun: 1}
	gateway := httptest.NewServer(subject.Handler())
	defer gateway.Close()

	var held sync.WaitGroup
	hold := func(path, token string) {
		held.Add(1)
		go func() {
			defer held.Done()
			req, _ := http.NewRequest(http.MethodPost, gateway.URL+path, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			if response, err := http.DefaultClient.Do(req); err == nil {
				_ = response.Body.Close()
			}
		}()
	}
	defer func() {
		close(release)
		held.Wait()
	}()
	hold("/runs/ns/run/v1/chat/completions", testToken)
	<-started

	// The run is at its own limit, though the gateway has a slot free.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/runs/ns/run/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the run's second call was not refused: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("second call of the run: status = %d", response.StatusCode)
	}
	if response.Header.Get("Retry-After") == "" {
		t.Error("a busy refusal has no Retry-After")
	}

	// The other run still gets in.
	hold("/runs/ns/other/v1/chat/completions", "other-token-0123456789")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the other run was refused")
	}
}

// profilesReader serves a valid profiles ConfigMap. Each read calls before first.
func profilesReader(t *testing.T, before func(context.Context) error) client.Reader {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "profiles", Namespace: "system"},
		Data:       map[string]string{DataKey: "profiles: []\n"},
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := before(ctx); err != nil {
				return err
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
}
