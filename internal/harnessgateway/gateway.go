package harnessgateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/modelgateway"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
)

const (
	runTokenSecretPrefix = "sympozium-run-model-"

	// TokenSecretKey holds the run token in the run token Secret.
	TokenSecretKey = "token"
	// CASecretKey holds the gateway's CA, public, in the same Secret and in the
	// gateway's certificate Secret.
	CASecretKey = "ca.crt"

	// Annotations on the run token Secret. They record what the token was minted
	// for, so a change after minting is refused rather than followed. An edit to
	// the run can't move the token to another key or origin.
	AgentUIDAnnotation         = "sympozium.ai/run-model-agent-uid"
	KeySecretUIDAnnotation     = "sympozium.ai/run-model-key-secret-uid"
	HeadersSecretUIDAnnotation = "sympozium.ai/run-model-headers-secret-uid"
	OriginAnnotation           = "sympozium.ai/run-model-origin"

	// No duration applies to a model call. A harness's own timeouts (RUN_TIMEOUT,
	// LLM_REQUEST_TIMEOUT, spec.timeout) already bound it, and the call ends when
	// the harness hangs up or the run stops allowing model calls. A fixed limit
	// here would cut off a run an operator has deliberately given longer.
	defaultRunWatchInterval = 30 * time.Second
	maxRequestBytes         = 64 << 20
	// A short provider header value is not a secret worth refusing a response
	// over. "true" would match almost any response.
	minEchoSecretLength = 8
	// Tells a harness refused as busy when to try again. A slot frees when a call
	// ends, so there is no better estimate.
	busyRetryAfterSeconds = "5"
)

// Public refusal reasons. A refusal names no credential, route or provider
// detail.
const (
	reasonUnauthorized = "unauthorized"
	reasonPhase        = "run_phase"
	reasonNotMediated  = "not_mediated"
	reasonPath         = "path"
	reasonKey          = "model_key"
	reasonDestination  = "destination"
	reasonUnavailable  = "unavailable"
	reasonProvider     = "provider_unavailable"
	reasonEcho         = "response_refused"
	reasonBusy         = "busy"
	reasonCredentials  = "provider_refused_credentials"
)

// RunTokenSecretName is the Secret holding a run's token. Exported so the
// minter and the gateway cannot drift apart.
func RunTokenSecretName(run string) string {
	return runTokenSecretPrefix + run
}

// ProfileSource provides the current profiles.
type ProfileSource interface {
	Load(ctx context.Context) (*Set, error)
}

// Gateway swaps a run token for the run's model key.
type Gateway struct {
	Reader   client.Reader
	Profiles ProfileSource
	// MaxConcurrent caps calls in flight. Zero means no cap.
	MaxConcurrent int64
	// MaxConcurrentPerRun caps one run's calls in flight. Zero means no cap.
	MaxConcurrentPerRun int64
	// RunWatchInterval is how often a call in flight re-reads its run.
	RunWatchInterval time.Duration

	mu          sync.Mutex
	inFlight    atomic.Int64
	runInFlight map[types.NamespacedName]int64
}

// Handler serves the gateway's routes.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/runs/{namespace}/{run}/{path...}", g.serve)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

var kubernetesName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	namespace, name := r.PathValue("namespace"), r.PathValue("run")
	if !kubernetesName.MatchString(namespace) || !kubernetesName.MatchString(name) {
		refuse(w, http.StatusUnauthorized, reasonUnauthorized, "", "")
		return
	}
	ctx := r.Context()
	key := types.NamespacedName{Namespace: namespace, Name: name}

	profiles, err := g.Profiles.Load(ctx)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, reasonUnavailable, namespace, name)
		return
	}

	var run api.AgentRun
	if err := g.Reader.Get(ctx, key, &run); err != nil {
		// A missing run looks like a wrong token.
		refuse(w, http.StatusUnauthorized, reasonUnauthorized, namespace, name)
		return
	}
	route, routeErr := profiles.Route(run.Spec.Model.Provider, run.Spec.Model.BaseURL)
	var profile *Profile
	if route != nil {
		profile = route.Profile
	}
	token, valid := g.tokenSecret(ctx, r, &run, profile)
	if !valid {
		refuse(w, http.StatusUnauthorized, reasonUnauthorized, namespace, name)
		return
	}

	// Everything below is only said to the holder of this run's token.
	if run.DeletionTimestamp != nil || !phaseAllowsModelCalls(run.Status.Phase) {
		refuse(w, http.StatusForbidden, reasonPhase, namespace, name)
		return
	}
	if run.Spec.Model.ModelRef != "" || run.Spec.Model.AuthSecretRef == "" {
		refuse(w, http.StatusForbidden, reasonNotMediated, namespace, name)
		return
	}
	if routeErr != nil {
		refuse(w, http.StatusForbidden, reasonDestination, namespace, name)
		return
	}
	path := "/" + r.PathValue("path")
	if r.URL.RawPath != "" || !profile.PathAllowed(path) || (r.Method != http.MethodPost && r.Method != http.MethodGet) {
		refuse(w, http.StatusForbidden, reasonPath, namespace, name)
		return
	}

	if route.Origin != token.Annotations[OriginAnnotation] {
		refuse(w, http.StatusForbidden, reasonDestination, namespace, name)
		return
	}
	apiKey, headers, reason, err := g.credentials(ctx, &run, route, token)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, reasonUnavailable, namespace, name)
		return
	}
	if reason != "" {
		refuse(w, http.StatusForbidden, reason, namespace, name)
		return
	}
	release, admitted := g.admit(key)
	if !admitted {
		w.Header().Set("Retry-After", busyRetryAfterSeconds)
		refuse(w, http.StatusServiceUnavailable, reasonBusy, namespace, name)
		return
	}
	defer release()
	g.forward(w, r, route, apiKey, headers, namespace, name)
}

// admit takes a slot for a call, or refuses if the gateway or the run is at its
// limit. The per-run limit stops one run, or a stolen token, from taking every
// slot and refusing all the other runs.
func (g *Gateway) admit(run types.NamespacedName) (release func(), admitted bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.MaxConcurrent > 0 && g.inFlight.Load() >= g.MaxConcurrent {
		return nil, false
	}
	if g.MaxConcurrentPerRun > 0 && g.runInFlight[run] >= g.MaxConcurrentPerRun {
		return nil, false
	}
	if g.runInFlight == nil {
		g.runInFlight = map[types.NamespacedName]int64{}
	}
	g.inFlight.Add(1)
	g.runInFlight[run]++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.inFlight.Add(-1)
		if g.runInFlight[run]--; g.runInFlight[run] == 0 {
			delete(g.runInFlight, run)
		}
	}, true
}

// tokenSecret returns the run's token Secret if the request holds its token. It
// fails closed: a missing Secret, a Secret this run does not control and a wrong
// token are all the same answer.
func (g *Gateway) tokenSecret(ctx context.Context, r *http.Request, run *api.AgentRun, profile *Profile) (*corev1.Secret, bool) {
	var secret corev1.Secret
	if err := g.Reader.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: RunTokenSecretName(run.Name)}, &secret); err != nil {
		return nil, false
	}
	// The Secret's name is predictable. Only one this run controls holds its
	// token, the rule authorizeGateVerdict applies to the gate token.
	if !metav1.IsControlledBy(&secret, run) {
		return nil, false
	}
	expected := secret.Data[TokenSecretKey]
	if len(expected) == 0 {
		return nil, false
	}
	matches := func(presented string) bool {
		return subtle.ConstantTimeCompare([]byte(presented), expected) == 1
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") && matches(strings.TrimPrefix(auth, "Bearer ")) {
		return &secret, true
	}
	if profile == nil {
		return nil, false
	}
	for _, auth := range profile.auths() {
		presented := r.Header.Get(auth.Header)
		if strings.HasPrefix(presented, auth.Prefix) && matches(strings.TrimPrefix(presented, auth.Prefix)) {
			return &secret, true
		}
	}
	return nil, false
}

// phaseAllowsModelCalls accepts Pending as well as Running: reconcilePending
// creates the Job first and writes Running after, so a pod can start while the
// run still says Pending. An empty phase is reconciled as Pending. Everything
// else is refused, so nothing can call the model while a gate judges the run's
// workspace.
func phaseAllowsModelCalls(phase api.AgentRunPhase) bool {
	return phase == "" || phase == api.AgentRunPhasePending || phase == api.AgentRunPhaseRunning
}

// credentials checks again that the Agent still owns the key, and reads the key
// and the run's provider headers. A non-empty reason is a refusal. Like Celln's
// liveRoute, this runs on every call, so revoking a key stops a run mid-way.
func (g *Gateway) credentials(ctx context.Context, run *api.AgentRun, route *Route, token *corev1.Secret) (string, map[string]string, string, error) {
	var agent api.Agent
	if err := g.Reader.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.AgentRef}, &agent); err != nil {
		return "", nil, reasonKey, nil
	}
	if agent.DeletionTimestamp != nil || string(agent.UID) != token.Annotations[AgentUIDAnnotation] {
		return "", nil, reasonKey, nil
	}
	model := run.Spec.Model
	var secret corev1.Secret
	if err := g.Reader.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: model.AuthSecretRef}, &secret); err != nil {
		return "", nil, reasonKey, nil
	}
	if !secretUsable(&secret, &agent, token.Annotations[KeySecretUIDAnnotation]) {
		return "", nil, reasonKey, nil
	}
	// The controller pins the key when it mints the token. A baseURL changed
	// after that goes nowhere.
	if !modelkey.PinnedTo(&secret, route.Origin) {
		return "", nil, reasonDestination, nil
	}
	apiKey := route.Profile.Key(secret.Data)
	if apiKey == "" {
		return "", nil, reasonKey, nil
	}

	headers := map[string]string{}
	for name, value := range model.ProviderHeaders {
		headers[http.CanonicalHeaderKey(name)] = value
	}
	if model.ProviderHeadersSecretRef != "" {
		var headerSecret corev1.Secret
		if err := g.Reader.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: model.ProviderHeadersSecretRef}, &headerSecret); err != nil {
			return "", nil, reasonKey, nil
		}
		if !secretUsable(&headerSecret, &agent, token.Annotations[HeadersSecretUIDAnnotation]) {
			return "", nil, reasonKey, nil
		}
		for name, value := range headerSecret.Data {
			headers[http.CanonicalHeaderKey(name)] = string(value)
		}
	}
	// A provider header can't change where the request goes. The key is set after
	// these in forward, so one can't replace it either.
	for name := range headers {
		if dropRequestHeader(name) {
			delete(headers, name)
		}
	}
	return apiKey, headers, "", nil
}

// secretUsable holds a Secret to what was minted: the same object, not being
// deleted, and still owned by this Agent.
func secretUsable(secret *corev1.Secret, agent *api.Agent, mintedUID string) bool {
	return secret.DeletionTimestamp == nil && string(secret.UID) == mintedUID && modelkey.OwnedBy(secret, agent)
}

// droppedRequestHeaders can carry a credential, or only mean something on the
// inbound hop. Accept-Encoding goes because the echo check must read plain
// bytes: without it the transport negotiates gzip and decompresses itself.
var droppedRequestHeaders = []string{
	"Authorization", "X-Api-Key", "Api-Key", "X-Goog-Api-Key", "Cookie", "Host",
	"Accept-Encoding", "Forwarded", "Via", "Connection", "Keep-Alive", "Te", "Trailer",
	"Transfer-Encoding", "Upgrade", "Content-Length",
}

func dropRequestHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	return slices.Contains(droppedRequestHeaders, canonical) ||
		strings.HasPrefix(canonical, "Proxy-") || strings.HasPrefix(canonical, "X-Forwarded-")
}

func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, route *Route, apiKey string, headers map[string]string, namespace, name string) {
	httpClient, err := modelgateway.ClientForEndpoint(route.Upstream, route.Private, 0)
	if err != nil {
		refuse(w, http.StatusForbidden, reasonDestination, namespace, name)
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go g.watchRun(ctx, cancel, types.NamespacedName{Namespace: namespace, Name: name})
	outbound, err := http.NewRequestWithContext(ctx, r.Method, route.URL("/"+r.PathValue("path"), r.URL.RawQuery), body)
	if err != nil {
		refuse(w, http.StatusForbidden, reasonDestination, namespace, name)
		return
	}
	outbound.ContentLength = r.ContentLength
	if r.ContentLength == 0 {
		outbound.Body = http.NoBody // or the transport sends an empty chunked body
	}
	connectionListed := strings.Split(r.Header.Get("Connection"), ",")
	for header, values := range r.Header {
		listed := slices.ContainsFunc(connectionListed, func(item string) bool {
			return strings.EqualFold(strings.TrimSpace(item), header)
		})
		isAuth := slices.ContainsFunc(route.Profile.auths(), func(auth Auth) bool { return strings.EqualFold(header, auth.Header) })
		if listed || isAuth || dropRequestHeader(header) {
			continue
		}
		outbound.Header[header] = values
	}
	for header, value := range headers {
		outbound.Header.Set(header, value)
	}
	auth := route.Profile.AuthFor(apiKey)
	outbound.Header.Set(auth.Header, auth.Prefix+apiKey)

	response, err := httpClient.Do(outbound)
	if err != nil {
		refuse(w, http.StatusBadGateway, reasonProvider, namespace, name)
		return
	}
	defer func() { _ = response.Body.Close() }()

	secrets := echoForms(apiKey)
	for _, value := range headers {
		if len(value) >= minEchoSecretLength {
			secrets = append(secrets, echoForms(value)...)
		}
	}
	// Provider auth errors are where a key gets echoed, often partly masked, which
	// the exact check can't catch. Celln hides every provider error body; a
	// harness needs the others, such as "prompt is too long", to recover.
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusProxyAuthRequired {
		refuse(w, response.StatusCode, reasonCredentials, namespace, name)
		return
	}
	relayed := http.Header{}
	for header, values := range response.Header {
		if route.responseHeaderAllowed(header) {
			relayed[header] = values
		}
	}
	if headerEchoes(relayed, secrets) {
		refuse(w, http.StatusBadGateway, reasonEcho, namespace, name)
		return
	}
	relay(w, response, relayed, secrets, namespace, name)
}

// watchRun ends a call in flight once its run stops allowing model calls, so a
// stolen token can't hold a stream open past the run. A failed read leaves the
// call alone: an API server blip is not a revocation.
func (g *Gateway) watchRun(ctx context.Context, cancel context.CancelFunc, key types.NamespacedName) {
	interval := g.RunWatchInterval
	if interval <= 0 {
		interval = defaultRunWatchInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		var run api.AgentRun
		err := g.Reader.Get(ctx, key, &run)
		if apierrors.IsNotFound(err) || (err == nil && (run.DeletionTimestamp != nil || !phaseAllowsModelCalls(run.Status.Phase))) {
			logRefusal(http.StatusForbidden, reasonPhase, key.Namespace, key.Name)
			cancel()
			return
		}
	}
}

func relay(w http.ResponseWriter, response *http.Response, relayed http.Header, secrets []string, namespace, name string) {
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Status and headers wait for the first checked bytes, so an echo in the
	// first chunk is still a clean refusal.
	writeHead := func() {
		for header, values := range relayed {
			w.Header()[header] = values
		}
		w.WriteHeader(response.StatusCode)
	}
	echo := newEchoWriter(w, flush, writeHead, secrets)
	_, err := io.Copy(onlyWriter{echo}, response.Body)
	switch {
	case err == nil:
		_ = echo.Close()
	case errors.Is(err, errEcho):
		if !echo.started {
			refuse(w, http.StatusBadGateway, reasonEcho, namespace, name)
			return
		}
		logRefusal(http.StatusBadGateway, reasonEcho, namespace, name)
		// The status has gone out, so the only refusal left is to break the
		// connection, which the client can't mistake for a complete response.
		panic(http.ErrAbortHandler)
	default:
		// The upstream or the client went away. Don't let a cut response look
		// complete, and don't release what was held back unread.
		panic(http.ErrAbortHandler)
	}
}

// onlyWriter hides the ResponseWriter's ReaderFrom, so io.Copy can't skip the
// echo check.
type onlyWriter struct{ io.Writer }

func refuse(w http.ResponseWriter, status int, reason, namespace, name string) {
	logRefusal(status, reason, namespace, name)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"reason": reason})
}

// logRefusal writes one line per refusal, public reason and run only. Names are
// checked as Kubernetes names before they get here.
func logRefusal(status int, reason, namespace, name string) {
	subject := ""
	if name != "" {
		subject = fmt.Sprintf(" run=%s/%s", namespace, name)
	}
	fmt.Fprintf(os.Stderr, "harness-gateway: refused status=%d reason=%s%s\n", status, reason, subject)
}
