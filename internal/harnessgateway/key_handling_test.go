package harnessgateway

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

const chatPath = "/runs/ns/run/v1/chat/completions"

func deleting(meta *metav1.ObjectMeta) {
	now := metav1.Now()
	meta.DeletionTimestamp = &now
	meta.Finalizers = []string{"test/hold"}
}

func withHeaderSecret(f *fixture, annotate bool) *corev1.Secret {
	f.run.Spec.Model.ProviderHeadersSecretRef = "headers"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "headers", Namespace: "ns", UID: "headers-uid"},
		Data:       map[string][]byte{"X-Secret": []byte("provider-header-secret")},
	}
	if annotate {
		secret.Annotations = map[string]string{modelkey.OwnerAnnotation: modelkey.Owner(f.agent)}
	}
	f.extras = append(f.extras, secret)
	return secret
}

// Every way the token check can fail gives one answer, so a caller learns nothing
// about which runs exist.
func TestAllTokenFailuresLookTheSame(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	_, wrong := call(t, gateway, chatPath, map[string]string{"Authorization": "Bearer nope"})
	_, missing := call(t, gateway, "/runs/ns/missing/v1/chat/completions", bearer())
	if wrong != missing || !strings.Contains(wrong, reasonUnauthorized) {
		t.Errorf("wrong token = %q, missing run = %q", wrong, missing)
	}
	if got.path != "" {
		t.Error("a refused call reached the upstream")
	}
}

func TestTokenPresentations(t *testing.T) {
	for name, tc := range map[string]struct {
		provider, secretKey, path string
		mutate                    func(*fixture)
		header                    map[string]string
		want                      int
	}{
		"the real key is not a token": {
			"openai", "OPENAI_API_KEY", "/v1/chat/completions", nil,
			map[string]string{"Authorization": "Bearer " + testKey}, http.StatusUnauthorized,
		},
		"an empty token against an empty Secret": {
			"openai", "OPENAI_API_KEY", "/v1/chat/completions",
			func(f *fixture) { f.token.Data[TokenSecretKey] = nil },
			map[string]string{"Authorization": "Bearer "}, http.StatusUnauthorized,
		},
		"no header against an empty Secret": {
			"anthropic", "ANTHROPIC_API_KEY", "/v1/messages",
			func(f *fixture) { f.token.Data[TokenSecretKey] = nil },
			nil, http.StatusUnauthorized,
		},
		"a token in a header this profile doesn't use": {
			"openai", "OPENAI_API_KEY", "/v1/chat/completions", nil,
			map[string]string{"x-api-key": testToken}, http.StatusUnauthorized,
		},
		"a token without the profile's prefix": {
			"openai", "OPENAI_API_KEY", "/v1/chat/completions", nil,
			map[string]string{"Authorization": testToken}, http.StatusUnauthorized,
		},
		"a token prefix alone": {
			"openai", "OPENAI_API_KEY", "/v1/chat/completions", nil,
			map[string]string{"Authorization": "Bearer " + testToken[:4]}, http.StatusUnauthorized,
		},
		"a bearer token on an x-api-key profile": {
			"anthropic", "ANTHROPIC_API_KEY", "/v1/messages", nil,
			map[string]string{"Authorization": "Bearer " + testToken}, http.StatusOK,
		},
	} {
		t.Run(name, func(t *testing.T) {
			upstream, got := recordingUpstream(t, "ok")
			f := newFixture(tc.provider, tc.secretKey, upstream.URL)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
			response, _ := call(t, gateway, "/runs/ns/run"+tc.path, tc.header)
			if response.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.want)
			}
			if tc.want != http.StatusOK && got.path != "" {
				t.Error("a refused call reached the upstream")
			}
		})
	}
}

func TestAnotherRunsRealTokenIsRefused(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	other := &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", UID: "other-uid"},
		Spec:       f.run.Spec,
		Status:     api.AgentRunStatus{Phase: api.AgentRunPhaseRunning},
	}
	otherToken := f.token.DeepCopy()
	otherToken.Name = RunTokenSecretName("other")
	otherToken.OwnerReferences[0].Name, otherToken.OwnerReferences[0].UID = "other", "other-uid"
	otherToken.Data[TokenSecretKey] = []byte("the-other-runs-token")
	f.extras = append(f.extras, other, otherToken)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))

	response, _ := call(t, gateway, chatPath, map[string]string{"Authorization": "Bearer the-other-runs-token"})
	if response.StatusCode != http.StatusUnauthorized || got.path != "" {
		t.Errorf("status = %d, upstream saw %q", response.StatusCode, got.path)
	}
}

func TestSameNamedRunInAnotherNamespaceIsSeparate(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	other := f.run.DeepCopy()
	other.Namespace, other.UID, other.ResourceVersion = "ns2", "ns2-uid", ""
	f.extras = append(f.extras, other)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	response, _ := call(t, gateway, "/runs/ns2/run/v1/chat/completions", bearer())
	if response.StatusCode != http.StatusUnauthorized || got.path != "" {
		t.Errorf("status = %d, upstream saw %q", response.StatusCode, got.path)
	}
}

// A dot-dot path must not reach another run's upstream with this run's token.
func TestPathTraversalDoesNotReachTheUpstream(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	noRedirects := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{
		"/runs/ns/run/v1/chat/completions/../files",
		"/runs/ns/run/../other/v1/chat/completions",
		"/runs/ns/run/v1//chat/completions",
		"/runs/ns/run/v1/chat/completions/",
		"/runs/ns/run",
	} {
		req, err := http.NewRequest(http.MethodPost, gateway.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		response, err := noRedirects.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode < 300 {
			t.Errorf("%s: status = %d", path, response.StatusCode)
		}
		if got.path != "" {
			t.Errorf("%s reached the upstream at %q", path, got.path)
		}
	}
}

func TestRefusalsAfterTheTokenCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*fixture)
		reason string
	}{
		"a run being deleted":    {func(f *fixture) { deleting(&f.run.ObjectMeta) }, reasonPhase},
		"a modelRef run":         {func(f *fixture) { f.run.Spec.Model.ModelRef = "local" }, reasonNotMediated},
		"a run with no key":      {func(f *fixture) { f.run.Spec.Model.AuthSecretRef = "" }, reasonNotMediated},
		"an Agent gone":          {func(f *fixture) { f.run.Spec.AgentRef = "missing" }, reasonKey},
		"an Agent being deleted": {func(f *fixture) { deleting(&f.agent.ObjectMeta) }, reasonKey},
		"a key Secret gone":      {func(f *fixture) { f.run.Spec.Model.AuthSecretRef = "missing" }, reasonKey},
		"a key being deleted":    {func(f *fixture) { deleting(&f.key.ObjectMeta) }, reasonKey},
		"a key with no usable value": {func(f *fixture) {
			f.key.Data = map[string][]byte{"ANTHROPIC_API_KEY": []byte("the-wrong-providers-key")}
		}, reasonKey},
		"an empty key value":         {func(f *fixture) { f.key.Data["OPENAI_API_KEY"] = nil }, reasonKey},
		"an unpinned key":            {func(f *fixture) { delete(f.key.Annotations, modelkey.OriginAnnotation) }, reasonDestination},
		"a provider with no profile": {func(f *fixture) { f.run.Spec.Model.Provider = "unknown" }, reasonDestination},
		"a baseURL edited after minting": {func(f *fixture) {
			f.run.Spec.Model.BaseURL = "https://attacker.example/v1"
		}, reasonDestination},
		"a headers Secret gone":               {func(f *fixture) { f.run.Spec.Model.ProviderHeadersSecretRef = "missing" }, reasonKey},
		"a headers Secret recreated":          {func(f *fixture) { withHeaderSecret(f, true).UID = "other" }, reasonKey},
		"a headers Secret another Agent owns": {func(f *fixture) { withHeaderSecret(f, false) }, reasonKey},
		"a headers Secret being deleted":      {func(f *fixture) { deleting(&withHeaderSecret(f, true).ObjectMeta) }, reasonKey},
	} {
		t.Run(name, func(t *testing.T) {
			upstream, got := recordingUpstream(t, "ok")
			f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
			tc.mutate(f)
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
			response, body := call(t, gateway, chatPath, bearer())
			if response.StatusCode != http.StatusForbidden || !strings.Contains(body, tc.reason) {
				t.Errorf("status = %d body = %q, want 403 %s", response.StatusCode, body, tc.reason)
			}
			if got.path != "" {
				t.Error("a refused call reached the upstream")
			}
		})
	}
}

type failingProfiles struct{}

func (failingProfiles) Load(context.Context) (*Set, error) { return nil, errors.New("no profiles") }

func TestProfilesThatCannotLoadFailClosed(t *testing.T) {
	gateway := httptest.NewServer((&Gateway{Profiles: failingProfiles{}}).Handler())
	defer gateway.Close()
	response, _ := call(t, gateway, chatPath, bearer())
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d", response.StatusCode)
	}
}

func TestInboundCredentialsNeverReachTheUpstream(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("anthropic", "ANTHROPIC_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	response, _ := call(t, gateway, "/runs/ns/run/v1/messages", map[string]string{
		"x-api-key":           testToken,
		"Authorization":       "Bearer model-chosen",
		"Proxy-Authorization": "Basic model-chosen",
		"Api-Key":             "model-chosen",
		"X-Goog-Api-Key":      "model-chosen",
		"Cookie":              "c=model-chosen",
		"Forwarded":           "for=model-chosen",
		"X-Forwarded-For":     "model-chosen",
		"Connection":          "X-Hop-Listed",
		"X-Hop-Listed":        "model-chosen",
		"X-Custom":            "kept",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	for name, values := range got.header {
		joined := strings.Join(values, ",")
		if strings.Contains(joined, "model-chosen") || strings.Contains(joined, testToken) {
			t.Errorf("%s = %q reached the upstream", name, joined)
		}
	}
	if got.header.Get("X-Api-Key") != testKey || got.header.Get("X-Custom") != "kept" {
		t.Errorf("headers = %v", got.header)
	}
}

func TestAProviderHeaderCannotCarryOrRedirectTheKey(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	f.run.Spec.Model.ProviderHeaders = map[string]string{
		"authorization":       "Bearer from-the-spec",
		"Host":                "attacker.example",
		"Proxy-Authorization": "from-the-spec",
		"Connection":          "close",
		"X-Allowed":           "yes",
	}
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	if response, _ := call(t, gateway, chatPath, bearer()); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got.header.Get("Authorization") != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got.header.Get("Authorization"))
	}
	if got.header.Get("Proxy-Authorization") != "" || got.header.Get("X-Allowed") != "yes" {
		t.Errorf("headers = %v", got.header)
	}
}

func TestSecretKeysAreTriedInOrder(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "API_KEY", upstream.URL)
	f.key.Data["API_KEY"] = []byte("the-fallback-value")
	f.key.Data["OPENAI_API_KEY"] = []byte("the-preferred-value")
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	if response, _ := call(t, gateway, chatPath, bearer()); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got.header.Get("Authorization") != "Bearer the-preferred-value" {
		t.Errorf("Authorization = %q", got.header.Get("Authorization"))
	}
}

func TestOnlyPostAndGetAreForwarded(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	for _, method := range []string{http.MethodDelete, http.MethodPut, http.MethodPatch} {
		req, _ := http.NewRequest(method, gateway.URL+chatPath, nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusForbidden || got.path != "" {
			t.Errorf("%s: status = %d", method, response.StatusCode)
		}
	}
}

func TestARequestWithNoBodyIsSentWithNone(t *testing.T) {
	var encoding []string
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		encoding = r.TransferEncoding
	}))
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	req, _ := http.NewRequest(http.MethodPost, gateway.URL+chatPath, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(encoding) != 0 {
		t.Errorf("Transfer-Encoding = %v", encoding)
	}
}

func TestARequestBodyPassesThroughUnchanged(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = string(body)
	}))
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	if response, _ := call(t, gateway, chatPath, bearer()); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if seen != `{"model":"m"}` {
		t.Errorf("body = %q", seen)
	}
}

// The restricted dialer is what stops a public-looking URL reaching the cluster.
func TestAnUnlistedPrivateAddressIsNotDialled(t *testing.T) {
	var reached atomic.Bool
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, "https://elsewhere.example", ""))
	response, body := call(t, gateway, chatPath, bearer())
	if response.StatusCode != http.StatusBadGateway || reached.Load() {
		t.Errorf("status = %d, reached = %v", response.StatusCode, reached.Load())
	}
	if strings.Contains(body, "127.0.0.1") {
		t.Errorf("the refusal names the destination: %q", body)
	}
}

func TestAnUnreachableUpstreamSaysNothing(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	url := upstream.URL
	upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", url)
	gateway := f.serve(t, shippedProfiles(t, url, ""))
	response, body := call(t, gateway, chatPath, bearer())
	if response.StatusCode != http.StatusBadGateway || strings.TrimSpace(body) != `{"reason":"`+reasonProvider+`"}` {
		t.Errorf("status = %d body = %q", response.StatusCode, body)
	}
}

func TestProviderErrorsAreEchoChecked(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusInternalServerError} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":"bad key `+testKey+`"}`)
		}))
		f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
		gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
		response, body := call(t, gateway, chatPath, bearer())
		upstream.Close()
		if strings.Contains(body, testKey) || response.StatusCode != http.StatusBadGateway {
			t.Errorf("%d: status = %d body = %q", status, response.StatusCode, body)
		}
	}
}

func TestAProviderErrorWithoutTheKeyPassesThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"prompt is too long"}`)
	}))
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	response, body := call(t, gateway, chatPath, bearer())
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "prompt is too long") {
		t.Errorf("status = %d body = %q", response.StatusCode, body)
	}
}

// A short header value would refuse almost every response if it counted.
func TestAShortProviderHeaderIsNotAnEcho(t *testing.T) {
	upstream, _ := recordingUpstream(t, "this is true")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	f.run.Spec.Model.ProviderHeaders = map[string]string{"X-Flag": "true"}
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	if response, body := call(t, gateway, chatPath, bearer()); response.StatusCode != http.StatusOK || body != "this is true" {
		t.Errorf("status = %d body = %q", response.StatusCode, body)
	}
}

func TestACompressedResponseIsDecompressedForTheClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		zipped := gzip.NewWriter(w)
		_, _ = io.WriteString(zipped, "clean")
		_ = zipped.Close()
	}))
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	req, _ := http.NewRequest(http.MethodPost, gateway.URL+chatPath, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	// A model-chosen encoding can't make the gateway read bytes it can't check.
	req.Header.Set("Accept-Encoding", "identity, br")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if string(body) != "clean" || response.Header.Get("Content-Encoding") != "" {
		t.Errorf("body = %q, Content-Encoding = %q", body, response.Header.Get("Content-Encoding"))
	}
}

// Logs outlive a run and reach more people than the Secret does. This runs
// without a server so that every refusal is written on this goroutine.
func TestNoCredentialReachesTheLogsOrARefusal(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = stderr }()
	captured := make(chan string)
	go func() {
		raw, _ := io.ReadAll(reader)
		captured <- string(raw)
	}()

	var bodies []string
	serve := func(handler http.HandlerFunc, header map[string]string) {
		upstream := httptest.NewServer(handler)
		defer upstream.Close()
		f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
		scheme := runtime.NewScheme()
		_ = corev1.AddToScheme(scheme)
		_ = api.AddToScheme(scheme)
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.run, f.token, f.key, f.agent).Build()
		handlerUnderTest := (&Gateway{Reader: c, Profiles: staticProfiles{shippedProfiles(t, upstream.URL, "")}}).Handler()
		req := httptest.NewRequest(http.MethodPost, chatPath, strings.NewReader("{}"))
		for name, value := range header {
			req.Header.Set(name, value)
		}
		recorder := httptest.NewRecorder()
		handlerUnderTest.ServeHTTP(recorder, req)
		bodies = append(bodies, recorder.Body.String())
	}
	serve(func(http.ResponseWriter, *http.Request) {}, map[string]string{"Authorization": "Bearer " + testKey})
	serve(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, testKey)
	}, bearer())
	serve(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "bad key "+testKey)
	}, bearer())
	serve(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Request-Id", testKey)
	}, bearer())

	_ = writer.Close()
	logs := <-captured
	if !strings.Contains(logs, "refused") {
		t.Fatalf("expected refusal lines, got %q", logs)
	}
	for _, leak := range []string{testKey, testToken} {
		if strings.Contains(logs, leak) {
			t.Errorf("the logs hold %q: %s", leak, logs)
		}
		for _, body := range bodies {
			if strings.Contains(body, leak) {
				t.Errorf("a refusal holds %q: %s", leak, body)
			}
		}
	}
}

func holdingUpstream(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("a", 200))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	return upstream, release
}

func TestACallEndsWhenItsRunIsDeleted(t *testing.T) {
	upstream, release := holdingUpstream(t)
	defer upstream.Close()
	defer close(release)
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.run, f.token, f.key, f.agent).Build()
	gateway := httptest.NewServer((&Gateway{Reader: c, Profiles: staticProfiles{shippedProfiles(t, upstream.URL, "")}, RunWatchInterval: 10 * time.Millisecond}).Handler())
	defer gateway.Close()
	req, _ := http.NewRequest(http.MethodPost, gateway.URL+chatPath, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if err := c.Delete(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(response.Body)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cut call must not look complete")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call outlived its run")
	}
}

// An API server blip is not a revocation. A stream must survive it.
func TestAFailedRunReadDoesNotEndACall(t *testing.T) {
	upstream, release := holdingUpstream(t)
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	var failing atomic.Bool
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.run, f.token, f.key, f.agent).
		WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, inner client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if failing.Load() {
				return errors.New("apiserver unavailable")
			}
			return inner.Get(ctx, key, obj, opts...)
		}}).Build()
	gateway := httptest.NewServer((&Gateway{Reader: c, Profiles: staticProfiles{shippedProfiles(t, upstream.URL, "")}, RunWatchInterval: 5 * time.Millisecond}).Handler())
	defer gateway.Close()
	req, _ := http.NewRequest(http.MethodPost, gateway.URL+chatPath, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	failing.Store(true)
	time.Sleep(50 * time.Millisecond)
	close(release)
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Errorf("the stream was cut by a failed read: %v", err)
	}
}

func TestAHangingUpClientEndsTheUpstreamCall(t *testing.T) {
	ended := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A server only notices a disconnect once it has read the request body.
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, strings.Repeat("a", 200))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(ended)
	}))
	defer upstream.Close()
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+chatPath, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream call outlived the client")
	}
}

func TestTheConcurrencySlotIsReleased(t *testing.T) {
	upstream, _ := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.run, f.token, f.key, f.agent).Build()
	subject := &Gateway{Reader: c, Profiles: staticProfiles{shippedProfiles(t, upstream.URL, "")}, MaxConcurrent: 1}
	gateway := httptest.NewServer(subject.Handler())
	defer gateway.Close()
	for i := range 3 {
		if response, _ := call(t, gateway, chatPath, bearer()); response.StatusCode != http.StatusOK {
			t.Fatalf("call %d: status = %d", i, response.StatusCode)
		}
	}
	if got := subject.inFlight.Load(); got != 0 {
		t.Errorf("inFlight = %d", got)
	}
}

// A refused call must not spend a slot an unauthenticated caller could exhaust.
func TestARefusedCallTakesNoSlot(t *testing.T) {
	upstream, _ := recordingUpstream(t, "ok")
	f := newFixture("openai", "OPENAI_API_KEY", upstream.URL)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = api.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(f.run, f.token, f.key, f.agent).Build()
	subject := &Gateway{Reader: c, Profiles: staticProfiles{shippedProfiles(t, upstream.URL, "")}, MaxConcurrent: 1}
	gateway := httptest.NewServer(subject.Handler())
	defer gateway.Close()
	call(t, gateway, chatPath, map[string]string{"Authorization": "Bearer nope"})
	if got := subject.inFlight.Load(); got != 0 {
		t.Errorf("inFlight = %d", got)
	}
}

func TestEchoWriterCatchesAKeyAtEverySplit(t *testing.T) {
	const key = "sk-secret-key"
	bodies := map[string]string{
		"at the start":  key + " tail",
		"in the middle": "head " + key + " tail",
		"at the end":    "head " + key,
		"alone":         key,
	}
	for name, body := range bodies {
		for split := 0; split <= len(body); split++ {
			var out strings.Builder
			echo := newEchoWriter(&out, func() {}, func() {}, []string{key})
			_, first := echo.Write([]byte(body[:split]))
			_, second := echo.Write([]byte(body[split:]))
			if !errors.Is(first, errEcho) && !errors.Is(second, errEcho) {
				t.Errorf("%s, split %d: not caught", name, split)
			}
			if strings.ContainsAny(out.String(), "sk") && strings.Contains(out.String(), key[:2]) {
				t.Errorf("%s, split %d: part of the key was released: %q", name, split, out.String())
			}
		}
	}
}

func TestEchoWriterCatchesAKeySentOneByteAtATime(t *testing.T) {
	const key = "sk-secret-key"
	var out strings.Builder
	echo := newEchoWriter(&out, func() {}, func() {}, []string{key})
	var caught bool
	for _, b := range []byte("head " + key + " tail") {
		if _, err := echo.Write([]byte{b}); errors.Is(err, errEcho) {
			caught = true
			break
		}
	}
	if !caught {
		t.Error("not caught")
	}
	if strings.Contains(out.String(), "s") {
		t.Errorf("part of the key was released: %q", out.String())
	}
}

func TestEchoWriterChecksEverySecretWithTheLongestHoldBack(t *testing.T) {
	short, long := "short-secret", "a-much-longer-secret-value"
	for _, secret := range []string{short, long} {
		for split := 1; split < len(secret); split++ {
			var out strings.Builder
			echo := newEchoWriter(&out, func() {}, func() {}, []string{short, long})
			_, first := echo.Write([]byte("x" + secret[:split]))
			_, second := echo.Write([]byte(secret[split:]))
			if first == nil && second == nil {
				t.Errorf("%s, split %d: not caught", secret, split)
			}
		}
	}
}

func TestEchoWriterPassesCleanBytesExactly(t *testing.T) {
	body := strings.Repeat("clean bytes, sk-almost, ", 20)
	for _, size := range []int{1, 3, 7, 64, len(body)} {
		var out strings.Builder
		headers := 0
		echo := newEchoWriter(&out, func() {}, func() { headers++ }, []string{"sk-secret-key"})
		for i := 0; i < len(body); i += size {
			if _, err := echo.Write([]byte(body[i:min(i+size, len(body))])); err != nil {
				t.Fatal(err)
			}
		}
		if err := echo.Close(); err != nil {
			t.Fatal(err)
		}
		if out.String() != body {
			t.Errorf("size %d: output differs", size)
		}
		if headers != 1 {
			t.Errorf("size %d: head written %d times", size, headers)
		}
	}
}

func TestEchoWriterWritesTheHeadForAnEmptyBody(t *testing.T) {
	var head int
	echo := newEchoWriter(io.Discard, func() {}, func() { head++ }, []string{"sk-secret-key"})
	if err := echo.Close(); err != nil || head != 1 {
		t.Errorf("err = %v, head = %d", err, head)
	}
}

func TestEchoFormsCoverJSONEscaping(t *testing.T) {
	forms := echoForms(`key/with<chars>&"quote"`)
	for _, want := range []string{
		`key/with<chars>&"quote"`,
		`key/with<chars>&\"quote\"`,
		`key\/with<chars>&\"quote\"`,
	} {
		found := false
		for _, form := range forms {
			found = found || form == want
		}
		if !found {
			t.Errorf("missing %q in %q", want, forms)
		}
	}
	if got := echoForms("plain-key"); len(got) != 1 || got[0] != "plain-key" {
		t.Errorf("forms = %q", got)
	}
}

func TestLoaderReportsRejectedEntriesAndFailsOnAMalformedFile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "profiles", Namespace: "sys"},
		Data:       map[string]string{DataKey: "profiles:\n" + indent(goodProfile)},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
	var rejected []string
	loader := &Loader{Reader: c, Name: "profiles", Namespace: "sys", TTL: time.Nanosecond, OnRejected: func(s string) { rejected = append(rejected, s) }}
	if _, err := loader.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 1 || strings.Contains(rejected[0], "Bearer") {
		t.Errorf("rejected = %v", rejected)
	}
	cm.Data[DataKey] = "unknownField: true\n"
	if err := c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, err := loader.Load(context.Background()); err == nil {
		t.Error("a malformed file must not load")
	}
}

func TestProfileErrorsAndValidation(t *testing.T) {
	if !strings.Contains((&NoProfileError{Provider: "x"}).Error(), `"x"`) {
		t.Error("the error should name the provider")
	}
	for name, mutate := range map[string]func(string) string{
		"CRLF in the prefix":   func(p string) string { return strings.Replace(p, `"Bearer "`, `"Bearer \r\nX: y"`, 1) },
		"a bad clientBasePath": func(p string) string { return p + "  clientBasePath: ../up\n" },
		"no env": func(p string) string {
			return strings.Replace(p, "env: {baseURL: [GOOD_BASE_URL], apiKey: [GOOD_API_KEY]}", "env: {baseURL: [], apiKey: []}", 1)
		},
		"a dotted segment":  func(p string) string { return strings.Replace(p, "/v1/x", "/v1/.", 1) },
		"a query in a path": func(p string) string { return strings.Replace(p, "/v1/x", `"/v1/x?a=b"`, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			set, err := ParseSet([]byte(allowLists + "profiles:\n" + indent(mutate(goodProfile))))
			if err != nil {
				t.Fatal(err)
			}
			if len(set.Rejected) != 1 {
				t.Errorf("rejected = %v", set.Rejected)
			}
		})
	}
	if _, err := ParseSet([]byte("allowedResponseHeaders: [\"bad header\"]\n")); err == nil {
		t.Error("a malformed response header must fail the file")
	}
}

func TestProfileValuesAreNotLoggedInRejections(t *testing.T) {
	set, err := ParseSet([]byte(allowLists + "profiles:\n" + indent(strings.Replace(goodProfile, "type: header", "type: nope", 1))))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(set.Rejected); !strings.Contains(got, "good") {
		t.Errorf("a rejection should name the profile: %s", got)
	}
}

// A custom auth header isn't on the dropped list, so only setting the key last
// stops a provider header or a client header standing in for it.
func TestNoHeaderCanReplaceTheKeyOnACustomAuthHeader(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	set, err := ParseSet([]byte(`insecureOrigins: ["` + upstream.URL + `"]
allowedEnvNames: [CUSTOM_BASE_URL, CUSTOM_API_KEY]
allowedAuthHeaders: [X-Custom-Key]
profiles:
  - name: custom
    providers: [custom]
    upstream: https://custom.example
    secretKeys: [API_KEY]
    auth: {type: header, header: X-Custom-Key}
    paths: [/v1/x]
    env: {baseURL: [CUSTOM_BASE_URL], apiKey: [CUSTOM_API_KEY]}
`))
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture("custom", "API_KEY", upstream.URL)
	f.run.Spec.Model.ProviderHeaders = map[string]string{"x-custom-key": "from-the-spec"}
	gateway := f.serve(t, set)
	response, _ := call(t, gateway, "/runs/ns/run/v1/x", map[string]string{"X-Custom-Key": testToken})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if values := got.header.Values("X-Custom-Key"); len(values) != 1 || values[0] != testKey {
		t.Errorf("X-Custom-Key = %q, want the key alone", values)
	}
}

func TestTheAuthFollowsTheKindOfKey(t *testing.T) {
	for name, tc := range map[string]struct {
		key, wantHeader, wantValue, absentHeader string
	}{
		"an API key":         {"sk-ant-api03-real", "X-Api-Key", "sk-ant-api03-real", "Authorization"},
		"a subscription key": {"sk-ant-oat01-real", "Authorization", "Bearer sk-ant-oat01-real", "X-Api-Key"},
		"any other key":      {"proxy-key-value", "X-Api-Key", "proxy-key-value", "Authorization"},
		"a wrapped key":      {"sk-ant-oat01-\n re\tal\n", "Authorization", "Bearer sk-ant-oat01-real", "X-Api-Key"},
	} {
		t.Run(name, func(t *testing.T) {
			upstream, got := recordingUpstream(t, "ok")
			f := newFixture("anthropic", "ANTHROPIC_API_KEY", upstream.URL)
			f.key.Data["ANTHROPIC_API_KEY"] = []byte(tc.key)
			gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
			// Claude Code on a subscription sends its token as a bearer token.
			response, _ := call(t, gateway, "/runs/ns/run/v1/messages", bearer())
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
			if got.header.Get(tc.wantHeader) != tc.wantValue {
				t.Errorf("%s = %q, want %q", tc.wantHeader, got.header.Get(tc.wantHeader), tc.wantValue)
			}
			if got.header.Get(tc.absentHeader) != "" {
				t.Errorf("%s = %q, want it absent", tc.absentHeader, got.header.Get(tc.absentHeader))
			}
		})
	}
}

// A shaped token is still only a token.
func TestAFakeKeyWithARealPrefixAuthenticatesOnlyByItsValue(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	f := newFixture("anthropic", "ANTHROPIC_API_KEY", upstream.URL)
	f.key.Data["ANTHROPIC_API_KEY"] = []byte("sk-ant-oat01-real")
	f.token.Data[TokenSecretKey] = []byte("sk-ant-oat01-" + testToken)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	for _, presented := range []string{"sk-ant-oat01-", "sk-ant-oat01-wrong", testToken, "sk-ant-oat01-real"} {
		response, _ := call(t, gateway, "/runs/ns/run/v1/messages", map[string]string{"Authorization": "Bearer " + presented})
		if response.StatusCode != http.StatusUnauthorized || got.path != "" {
			t.Errorf("%q: status = %d", presented, response.StatusCode)
		}
	}
	response, _ := call(t, gateway, "/runs/ns/run/v1/messages", map[string]string{"x-api-key": "sk-ant-oat01-" + testToken})
	if response.StatusCode != http.StatusOK {
		t.Errorf("the token in x-api-key: status = %d", response.StatusCode)
	}
	if strings.Contains(strings.Join(got.header.Values("Authorization"), ""), testToken) {
		t.Error("the token reached the upstream")
	}
}

// A variant's custom header must not carry the token upstream.
func TestAVariantsInboundHeaderIsNotForwarded(t *testing.T) {
	upstream, got := recordingUpstream(t, "ok")
	set, err := ParseSet([]byte(`insecureOrigins: ["` + upstream.URL + `"]
allowedEnvNames: [CUSTOM_BASE_URL, CUSTOM_API_KEY]
allowedAuthHeaders: [X-Base-Key, X-Variant-Key]
profiles:
  - name: custom
    providers: [custom]
    upstream: https://custom.example
    secretKeys: [API_KEY]
    auth: {type: header, header: X-Base-Key}
    credentials:
      - keyPrefix: never-
        auth: {type: header, header: X-Variant-Key}
    paths: [/v1/x]
    env: {baseURL: [CUSTOM_BASE_URL], apiKey: [CUSTOM_API_KEY]}
`))
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture("custom", "API_KEY", upstream.URL)
	gateway := f.serve(t, set)
	response, _ := call(t, gateway, "/runs/ns/run/v1/x", map[string]string{"X-Variant-Key": testToken})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got.header.Get("X-Variant-Key") != "" || got.header.Get("X-Base-Key") != testKey {
		t.Errorf("headers = %v", got.header)
	}
}

func TestTokenFor(t *testing.T) {
	profile := &Profile{Credentials: []Credential{
		{KeyPrefix: "sk-a", FakeKeyPattern: "fake-a-{token}"},
		{KeyPrefix: "sk-", FakeKeyPattern: "fake-{token}-end"},
		{KeyPrefix: "plain-"},
	}}
	for key, want := range map[string]string{
		"sk-a-real": "fake-a-RANDOM",
		"sk-b-real": "fake-RANDOM-end",
		"plain-key": "RANDOM",
		"other":     "RANDOM",
		"":          "RANDOM",
	} {
		got := profile.TokenFor(key, "RANDOM")
		if got != want {
			t.Errorf("%q = %q, want %q", key, got, want)
		}
		if key != "" && strings.Contains(got, key) {
			t.Errorf("%q: the token holds the key", key)
		}
	}
}

func TestCredentialVariantValidation(t *testing.T) {
	variant := func(fields string) string {
		return goodProfile + "  credentials:\n    - " + fields + "\n"
	}
	for name, tc := range map[string]struct {
		profile string
		ok      bool
	}{
		"valid":              {variant(`{keyPrefix: sk-, fakeKeyPattern: "sk-{token}"}`), true},
		"a suffix":           {variant(`{keyPrefix: sk-, fakeKeyPattern: "{token}.x"}`), true},
		"no prefix":          {variant(`{fakeKeyPattern: "sk-{token}"}`), false},
		"no placeholder":     {variant(`{keyPrefix: sk-, fakeKeyPattern: "sk-static"}`), false},
		"two placeholders":   {variant(`{keyPrefix: sk-, fakeKeyPattern: "{token}{token}"}`), false},
		"a newline":          {variant(`{keyPrefix: sk-, fakeKeyPattern: "sk-\nX: y{token}"}`), false},
		"a space":            {variant(`{keyPrefix: sk-, fakeKeyPattern: "sk {token}"}`), false},
		"an unlisted header": {variant(`{keyPrefix: sk-, auth: {type: header, header: X-Other}}`), false},
		"an unknown type":    {variant(`{keyPrefix: sk-, auth: {type: sigv4, header: Authorization}}`), false},
		"an unknown field":   {variant(`{keyPrefix: sk-, future: true}`), false},
	} {
		t.Run(name, func(t *testing.T) {
			set, err := ParseSet([]byte(allowLists + "profiles:\n" + indent(tc.profile)))
			if err != nil {
				t.Fatal(err)
			}
			if got := len(set.Rejected) == 0; got != tc.ok {
				t.Errorf("rejected = %v", set.Rejected)
			}
		})
	}
}

// What Claude Code's gateway compatibility guide says a gateway must carry:
// https://code.claude.com/docs/en/llm-gateway-protocol
func TestClaudeCodesGatewayRequirements(t *testing.T) {
	var query string
	var seen http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, seen = r.URL.RawQuery, r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Retry-After", "7")
		w.Header().Set("X-Should-Retry", "true")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "allowed")
	}))
	defer upstream.Close()
	f := newFixture("anthropic", "ANTHROPIC_API_KEY", upstream.URL)
	gateway := f.serve(t, shippedProfiles(t, upstream.URL, ""))
	beta := "oauth-2025-04-20,some-future-capability"
	response, _ := call(t, gateway, "/runs/ns/run/v1/messages?beta=true", map[string]string{
		"Authorization": "Bearer " + testToken, "Anthropic-Beta": beta, "Anthropic-Version": "2023-06-01",
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if query != "beta=true" || seen.Get("Anthropic-Beta") != beta || seen.Get("Anthropic-Version") != "2023-06-01" {
		t.Errorf("query = %q, headers = %v", query, seen)
	}
	for header, want := range map[string]string{
		"Content-Type": "text/event-stream", "Retry-After": "7", "X-Should-Retry": "true", "Anthropic-Ratelimit-Unified-Status": "allowed",
	} {
		if got := response.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}
