package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A backend's model parameters are accepted on POST, validated with the
// fleet's rules, probed, stored for the nodes and shown on GET.
func TestCellnFleetBackendsCarryModelParameters(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	installed := `[{"name":"native","provider":"deepseek","protocol":"openai-chat","endpoint":"https://api.deepseek.com/chat/completions","model":"deepseek-chat","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/native"},` +
		`{"name":"tuned","provider":"openai","protocol":"openai-chat","endpoint":"https://api.openai.com/v1/chat/completions","model":"gpt-4o","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/tuned","parameters":{"temperature":0.2}}]`
	configure := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: cellninstall.FleetConfigureDaemonSet, Namespace: "celln-system"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "configure", Env: []corev1.EnvVar{
		{Name: "FLEET_SCOPE", Value: "starter"}, {Name: "FLEET_PRINCIPAL", Value: "sympozium:celln"}, {Name: "FLEET_PACKAGE_HASH", Value: "blake3:" + strings.Repeat("b", 64)}, {Name: "FLEET_BACKENDS", Value: installed},
	}}}}}}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configure).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	srv.completeCellnBackend = func(string, cellninstall.FleetFacts, string) {}

	var probe map[string]any
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe = nil
		_ = json.NewDecoder(r.Body).Decode(&probe)
		if _, refused := probe["unknown_knob"]; refused {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"unknown_knob is not supported"}`))
		}
	}))
	defer model.Close()
	post := func(body string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/celln-platform/backends", strings.NewReader(body)))
		return res
	}
	request := func(name, parameters string) string {
		return `{"name":"` + name + `","provider":"llama-server","model":"qwen.gguf","endpoint":"` + model.URL + `/v1/chat/completions","allowInsecure":true,"parameters":` + parameters + `}`
	}

	// Invalid parameters: 400 with the precise rule, nothing probed or stored.
	for parameters, refusal := range map[string]string{
		`{"max_tokens":4096}`:                      `model parameters: "max_tokens" is reserved (Celln sets it on every request)`,
		`{"Temperature":1}`:                        `model parameters: key "Temperature" must match ^[a-z][a-z0-9_]{0,63}$`,
		`{"a":{"b":{"c":{"d":1}}}}`:                `model parameters: a.b.c nests deeper than 3 levels`,
		`{"stop":[1,2,3,4,5,6,7,8,9]}`:             `model parameters: stop holds 9 items, at most 8`,
		`{"a":null}`:                               `model parameters: a is null`,
		`{"a":"` + strings.Repeat("x", 257) + `"}`: `model parameters: a must be a string of at most 256 bytes without NUL`,
	} {
		probe = nil
		res := post(request("bad", parameters))
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), refusal) || probe != nil {
			t.Fatalf("%s: status %d body %q probe %v", parameters, res.Code, res.Body.String(), probe)
		}
	}
	if res := post(`{"name":"bad","provider":"llama-server","parameters":[1]}`); res.Code != http.StatusBadRequest {
		t.Fatalf("non-object parameters: %d", res.Code)
	}
	// A parameter the provider refuses is reported by the probe.
	if res := post(request("bad", `{"unknown_knob":true}`)); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "backend probe failed") || !strings.Contains(res.Body.String(), `{"unknown_knob":true}`) {
		t.Fatalf("refused parameter: %d %s", res.Code, res.Body.String())
	}
	if extra, _, err := cellninstall.ReadExtraBackends(t.Context(), cl); err != nil || len(extra) != 0 {
		t.Fatalf("a refused backend was stored: %v %v", extra, err)
	}

	thinkingOff := map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}
	res := post(request("local", `{"chat_template_kwargs":{"enable_thinking":false}}`))
	if res.Code != http.StatusAccepted {
		t.Fatalf("add: %d %s", res.Code, res.Body.String())
	}
	var added CellnFleetBackend
	if err := json.Unmarshal(res.Body.Bytes(), &added); err != nil || !reflect.DeepEqual(added.Parameters, thinkingOff) || added.Source != "added" {
		t.Fatalf("add response: %+v %v", added, err)
	}
	if !reflect.DeepEqual(probe["chat_template_kwargs"], map[string]any{"enable_thinking": false}) || probe["max_tokens"] != float64(1) {
		t.Fatalf("probe body: %v", probe)
	}
	// What the node script reads carries them; a backend without carries no key.
	if res := post(`{"name":"plain","provider":"llama-server","model":"qwen.gguf","endpoint":"` + model.URL + `/v1/chat/completions","allowInsecure":true}`); res.Code != http.StatusAccepted {
		t.Fatalf("add plain: %d %s", res.Code, res.Body.String())
	}
	var cm corev1.ConfigMap
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "celln-system", Name: cellninstall.FleetExtraBackendsConfigMap}, &cm); err != nil {
		t.Fatal(err)
	}
	if stored := cm.Data["backends.json"]; strings.Count(stored, `"parameters"`) != 1 || !strings.Contains(stored, `"parameters":{"chat_template_kwargs":{"enable_thinking":false}}`) {
		t.Fatalf("stored list: %s", stored)
	}
	// The name exists now: parameters cannot be changed by adding it again.
	if res := post(request("local", `{"temperature":1}`)); res.Code != http.StatusConflict {
		t.Fatalf("re-add: %d %s", res.Code, res.Body.String())
	}

	list := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/backends", nil))
	var backends []CellnFleetBackend
	if err := json.Unmarshal(list.Body.Bytes(), &backends); err != nil || len(backends) != 4 {
		t.Fatalf("list: %v %s", err, list.Body.String())
	}
	got := map[string]map[string]any{}
	for _, b := range backends {
		got[b.Name] = b.Parameters
	}
	if got["native"] != nil || got["plain"] != nil || !reflect.DeepEqual(got["tuned"], map[string]any{"temperature": 0.2}) || !reflect.DeepEqual(got["local"], thinkingOff) {
		t.Fatalf("listed parameters: %v", got)
	}
	if strings.Count(list.Body.String(), `"parameters"`) != 2 {
		t.Fatalf("backends without parameters list the key: %s", list.Body.String())
	}
}

// A backend's output cap per request is accepted on POST within Celln's range,
// stored for the nodes only when it is not the default, shown on GET, kept out
// of the probe, and answered with a warning when the scope's ceilings pay for
// fewer of its turns than the usual session.
func TestCellnFleetBackendsCarryMaxOutputTokens(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	installed := `[{"name":"native","provider":"deepseek","protocol":"openai-chat","endpoint":"https://api.deepseek.com/chat/completions","model":"deepseek-chat","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/native"},` +
		`{"name":"reasoner","provider":"openai","protocol":"openai-chat","endpoint":"https://api.openai.com/v1/chat/completions","model":"o4","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/reasoner","maxOutputTokens":2048}]`
	configure := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: cellninstall.FleetConfigureDaemonSet, Namespace: "celln-system"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "configure", Env: []corev1.EnvVar{
		{Name: "FLEET_SCOPE", Value: "starter"}, {Name: "FLEET_PRINCIPAL", Value: "sympozium:celln"}, {Name: "FLEET_PACKAGE_HASH", Value: "blake3:" + strings.Repeat("b", 64)}, {Name: "FLEET_BACKENDS", Value: installed},
	}}}}}}}
	policy := func(tokens int64) *sympoziumv1alpha1.CellnExecutionPolicy {
		return &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-starter"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{
			Ceilings: sympoziumv1alpha1.CellnExecutionPolicyCeilings{MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: tokens, MaxParentLeaseSeconds: 86400, MaxTurnSeconds: 60}}}
	}
	var probe map[string]any
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe = nil
		_ = json.NewDecoder(r.Body).Decode(&probe)
	}))
	defer model.Close()
	request := func(name, tokens string) string {
		return `{"name":"` + name + `","provider":"llama-server","model":"qwen.gguf","endpoint":"` + model.URL + `/v1/chat/completions","allowInsecure":true,"maxOutputTokens":` + tokens + `}`
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configure, policy(786432)).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	var mu sync.Mutex
	hints := map[string]string{}
	srv.completeCellnBackend = func(name string, _ cellninstall.FleetFacts, hint string) {
		mu.Lock()
		defer mu.Unlock()
		hints[name] = hint
	}
	post := func(body string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/celln-platform/backends", strings.NewReader(body)))
		return res
	}
	add := func(name, tokens string) CellnFleetBackend {
		t.Helper()
		res := post(request(name, tokens))
		if res.Code != http.StatusAccepted {
			t.Fatalf("add %s: %d %s", name, res.Code, res.Body.String())
		}
		var added CellnFleetBackend
		if err := json.Unmarshal(res.Body.Bytes(), &added); err != nil {
			t.Fatal(err)
		}
		return added
	}

	// Out of range: 400 with the rule, nothing probed or stored.
	for _, tokens := range []string{"255", "4097", "-1"} {
		probe = nil
		if res := post(request("bad", tokens)); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "max output tokens per request must be 256–4096 (omit it for the default 512)") || probe != nil {
			t.Fatalf("%s: %d %q probe %v", tokens, res.Code, res.Body.String(), probe)
		}
	}
	if res := post(request("bad", `"2048"`)); res.Code != http.StatusBadRequest {
		t.Fatalf("a string was accepted: %d", res.Code)
	}

	// The default, stated: nothing is stored or returned, as if it were absent.
	if added := add("stated", "512"); added.MaxOutputTokens != 0 || added.Warning != "" {
		t.Fatalf("default: %+v", added)
	}
	// 2048 per request: a turn reserves 12288 and the ceilings still pay for 64.
	probe = nil
	if added := add("thinker", "2048"); added.MaxOutputTokens != 2048 || added.Warning != "" {
		t.Fatalf("2048: %+v", added)
	}
	// The probe stays a one-token request and never carries the cap.
	if len(probe) != 3 || probe["max_tokens"] != float64(1) {
		t.Fatalf("probe body: %v", probe)
	}
	// 4096 per request: a turn reserves 24576 and the ceilings pay for 32.
	deep := add("deep", "4096")
	for _, want := range []string{"backend deep reserves 24576 output tokens (6 requests × 4096)", "pay for 32 such turns, fewer than the usual 64"} {
		if deep.MaxOutputTokens != 4096 || !strings.Contains(deep.Warning, want) {
			t.Fatalf("warning lacks %q: %+v", want, deep)
		}
	}
	// The background completion knows which Celln the backend needs.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		done, stated, deepHint := len(hints) == 3, hints["stated"], hints["deep"]
		mu.Unlock()
		if done {
			if stated != "" || !strings.Contains(deepHint, "newer than v0.5.23") {
				t.Fatalf("hints: %q %q", stated, deepHint)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completions did not run")
		}
	}

	var cm corev1.ConfigMap
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "celln-system", Name: cellninstall.FleetExtraBackendsConfigMap}, &cm); err != nil {
		t.Fatal(err)
	}
	if stored := cm.Data["backends.json"]; strings.Count(stored, `"maxOutputTokens"`) != 2 || !strings.Contains(stored, `"maxOutputTokens":2048`) || !strings.Contains(stored, `"maxOutputTokens":4096`) {
		t.Fatalf("stored list: %s", stored)
	}
	list := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/backends", nil))
	var backends []CellnFleetBackend
	if err := json.Unmarshal(list.Body.Bytes(), &backends); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, b := range backends {
		got[b.Name] = b.MaxOutputTokens
	}
	if want := map[string]int64{"native": 0, "reasoner": 2048, "stated": 0, "thinker": 2048, "deep": 4096}; !reflect.DeepEqual(got, want) || strings.Count(list.Body.String(), `"maxOutputTokens"`) != 3 {
		t.Fatalf("listed: %v %s", got, list.Body.String())
	}

	// Ceilings that cannot pay for one turn of the backend: refused up front,
	// as every node would refuse it.
	narrow := NewServer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(configure, policy(6144)).Build(), nil, nil, logr.Discard())
	narrow.completeCellnBackend = func(string, cellninstall.FleetFacts, string) {}
	res := httptest.NewRecorder()
	narrow.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/celln-platform/backends", strings.NewReader(request("deep", "2048"))))
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "one turn reserve 12288 output tokens") || !strings.Contains(res.Body.String(), "choose at most 1024") {
		t.Fatalf("narrow ceilings: %d %s", res.Code, res.Body.String())
	}
}
