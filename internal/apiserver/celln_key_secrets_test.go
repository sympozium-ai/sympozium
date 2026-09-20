package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const keySecretValue = "sk-ant-must-never-leave-the-cluster"

func keySecretServer(t *testing.T, objects ...client.Object) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	return NewServer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), nil, nil, logr.Discard())
}

func opaque(namespace, name string, labels map[string]string, data map[string]string) *corev1.Secret {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: labels}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{}}
	for k, v := range data {
		secret.Data[k] = []byte(v)
	}
	return secret
}

// The picker learns the names of Secrets holding the asked model key in the
// asked namespace, and nothing else: no value, no other key, no other Secret.
func TestListCellnKeySecretsDisclosesNamesOnly(t *testing.T) {
	token := opaque("team-a", "sa-token", nil, map[string]string{"ANTHROPIC_API_KEY": keySecretValue})
	token.Type = corev1.SecretTypeServiceAccountToken
	srv := keySecretServer(t,
		opaque("team-a", "mine", nil, map[string]string{"ANTHROPIC_API_KEY": keySecretValue, "OTHER_PRIVATE_KEY_NAME": "x"}),
		opaque("team-a", "console-made", map[string]string{"sympozium.ai/model-connection": "a-connection"}, map[string]string{"ANTHROPIC_API_KEY": keySecretValue}),
		opaque("team-a", "openai-only", nil, map[string]string{"OPENAI_API_KEY": keySecretValue}),
		opaque("team-a", "empty", nil, map[string]string{"ANTHROPIC_API_KEY": ""}),
		opaque("team-a", "database", nil, map[string]string{"password": keySecretValue}),
		opaque("team-a", "local", map[string]string{"sympozium.ai/credential-kind": "harness-local-compatibility"}, map[string]string{"ANTHROPIC_API_KEY": "local-no-key"}),
		token,
		opaque("team-b", "elsewhere", nil, map[string]string{"ANTHROPIC_API_KEY": keySecretValue}),
	)
	get := func(query string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/key-secrets?"+query, nil))
		return res
	}
	res := get("namespace=team-a&key=ANTHROPIC_API_KEY")
	if res.Code != http.StatusOK {
		t.Fatalf("list: %d %s", res.Code, res.Body.String())
	}
	var got []CellnKeySecret
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []CellnKeySecret{{Name: "console-made", Key: "ANTHROPIC_API_KEY", Managed: true}, {Name: "mine", Key: "ANTHROPIC_API_KEY"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("listed %+v, want %+v", got, want)
	}
	for _, leaked := range []string{keySecretValue, "OTHER_PRIVATE_KEY_NAME", "database", "password", "elsewhere", "sa-token", "local"} {
		if strings.Contains(res.Body.String(), leaked) {
			t.Fatalf("response discloses %q: %s", leaked, res.Body.String())
		}
	}
	if res := get("namespace=team-a&key=OPENAI_API_KEY"); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"openai-only"`) || strings.Contains(res.Body.String(), `"mine"`) {
		t.Fatalf("openai listing: %d %s", res.Code, res.Body.String())
	}
	// The key is not free text: the endpoint cannot probe for other key names.
	for _, query := range []string{"namespace=team-a&key=password", "namespace=team-a"} {
		if res := get(query); res.Code != http.StatusBadRequest || strings.Contains(res.Body.String(), "database") {
			t.Fatalf("%s: %d %s", query, res.Code, res.Body.String())
		}
	}
	if res := get("namespace=team-c&key=ANTHROPIC_API_KEY"); res.Code != http.StatusOK || strings.TrimSpace(res.Body.String()) != "[]" {
		t.Fatalf("empty namespace: %d %s", res.Code, res.Body.String())
	}
}

// Creating a native Celln Agent on its own Secret-backed connection grants
// that Secret in authRefs, named by the connection and never by the request,
// and refuses a Secret that is missing or lacks the protocol's fixed key.
func TestCreateCellnAgentGrantsItsConnectionSecret(t *testing.T) {
	connection := func(name, secret string) *api.ModelConnection {
		return &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: name}, Spec: api.ModelConnectionSpec{Provider: "anthropic", Protocol: "anthropic-messages", Endpoint: "https://api.anthropic.com/v1/messages", SecretRef: secret, Models: []string{"claude-sonnet-5"}}}
	}
	srv := keySecretServer(t,
		opaque("team-a", "my-anthropic-key", nil, map[string]string{"ANTHROPIC_API_KEY": keySecretValue}),
		opaque("team-a", "wrong-key-name", nil, map[string]string{"OPENAI_API_KEY": keySecretValue}),
		connection("mine-connection", "my-anthropic-key"), connection("wrong-connection", "wrong-key-name"), connection("missing-connection", "no-such-secret"),
	)
	post := func(name, connectionRef, extra string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		body := `{"name":"` + name + `","model":"claude-sonnet-5","runtimeRef":"celln-native"` + extra + `,"execution":{"backend":"celln","executionLifecycle":"enduring","modelConnectionRef":"` + connectionRef + `","model":"claude-sonnet-5","cellnSelection":{"runtimeRef":"celln-native","toolRefs":[]},"enduring":{"leaseSeconds":600,"maxTurns":2,"maxModelRequests":12,"maxOutputTokens":6144}}}`
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/agents?namespace=team-a", strings.NewReader(body)))
		return res
	}
	res := post("mine", "mine-connection", "")
	if res.Code >= 300 {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), keySecretValue) {
		t.Fatal("the key reached the response")
	}
	var agent api.Agent
	if err := srv.client.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "mine"}, &agent); err != nil {
		t.Fatal(err)
	}
	if len(agent.Spec.AuthRefs) != 1 || agent.Spec.AuthRefs[0] != (api.SecretRef{Provider: "anthropic", Secret: "my-anthropic-key"}) || agent.Spec.Execution.ModelConnectionRef != "mine-connection" || agent.Spec.RuntimeRef != "celln-native" {
		t.Fatalf("agent does not own its key: %+v", agent.Spec)
	}
	var secrets corev1.SecretList
	_ = srv.client.List(context.Background(), &secrets, client.InNamespace("team-a"))
	if len(secrets.Items) != 2 {
		t.Fatalf("agent creation wrote a Secret: %d", len(secrets.Items))
	}
	if res := post("wrong", "wrong-connection", ""); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "ANTHROPIC_API_KEY") {
		t.Fatalf("wrong key name accepted: %d %s", res.Code, res.Body.String())
	}
	if res := post("missing", "missing-connection", ""); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "not found") {
		t.Fatalf("missing Secret accepted: %d %s", res.Code, res.Body.String())
	}
	// The request cannot name a Secret of its own beside the connection.
	if res := post("borrow", "mine-connection", `,"secretName":"wrong-key-name"`); res.Code != http.StatusBadRequest {
		t.Fatalf("request-named Secret accepted: %d %s", res.Code, res.Body.String())
	}
}

// Replacing an Agent's key with another Secret moves its authRefs grant with
// the connection, and a Secret without the fixed key is refused.
func TestPatchCellnAgentFollowsItsConnectionSecret(t *testing.T) {
	connection := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "mine-connection"}, Spec: api.ModelConnectionSpec{Provider: "anthropic", Protocol: "anthropic-messages", Endpoint: "https://api.anthropic.com/v1/messages", SecretRef: "second-key", Models: []string{"claude-sonnet-5"}}}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "mine"}, Spec: api.AgentSpec{RuntimeRef: "celln-native", AuthRefs: []api.SecretRef{{Provider: "anthropic", Secret: "first-key"}}}}
	srv := keySecretServer(t, agent, connection,
		opaque("team-a", "second-key", nil, map[string]string{"ANTHROPIC_API_KEY": keySecretValue}),
		opaque("team-a", "no-key", nil, map[string]string{"SOMETHING_ELSE": "x"}),
	)
	patch := func() *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		body := `{"execution":{"backend":"celln","executionLifecycle":"enduring","modelConnectionRef":"mine-connection","model":"claude-sonnet-5","cellnSelection":{"runtimeRef":"celln-native","toolRefs":[]},"enduring":{"leaseSeconds":600,"maxTurns":2,"maxModelRequests":12,"maxOutputTokens":6144}}}`
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/api/v1/agents/mine?namespace=team-a", strings.NewReader(body)))
		return res
	}
	if res := patch(); res.Code != http.StatusOK || strings.Contains(res.Body.String(), keySecretValue) {
		t.Fatalf("patch: %d %s", res.Code, res.Body.String())
	}
	var got api.Agent
	if err := srv.client.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "mine"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.AuthRefs) != 1 || got.Spec.AuthRefs[0] != (api.SecretRef{Provider: "anthropic", Secret: "second-key"}) {
		t.Fatalf("grant did not follow the connection: %+v", got.Spec.AuthRefs)
	}
	connection.Spec.SecretRef = "no-key"
	if err := srv.client.Update(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if res := patch(); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "ANTHROPIC_API_KEY") {
		t.Fatalf("Secret without the key accepted: %d %s", res.Code, res.Body.String())
	}
}

// What the console's "Create a new key" sends: the key lands in a Secret of the
// Agent's namespace under the name the protocol fixes, the connection carries
// this Agent's parameters and output-token bound, and the answer names the
// Secret without ever returning the key.
func TestCreateOwnKeyConnectionFixesTheSecretKeyByProtocol(t *testing.T) {
	for protocol, key := range map[string]string{"anthropic-messages": "ANTHROPIC_API_KEY", "openai-chat": "OPENAI_API_KEY"} {
		srv := keySecretServer(t)
		res := httptest.NewRecorder()
		body := `{"name":"mine-connection","apiKey":"` + keySecretValue + `","spec":{"provider":"my-provider","protocol":"` + protocol + `","endpoint":"https://llm.example.com/v1/messages","models":["m"],"parameters":{"temperature":0.2},"maxOutputTokens":2048}}`
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/model-connections?namespace=team-a", strings.NewReader(body)))
		if res.Code != http.StatusCreated || strings.Contains(res.Body.String(), keySecretValue) || !strings.Contains(res.Body.String(), `"secretRef":"mine-connection-my-provider-key"`) {
			t.Fatalf("%s: %d %s", protocol, res.Code, res.Body.String())
		}
		var secret corev1.Secret
		if err := srv.client.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "mine-connection-my-provider-key"}, &secret); err != nil {
			t.Fatal(err)
		}
		// The fake client keeps StringData; a real API server folds it into Data.
		if len(secret.StringData)+len(secret.Data) != 1 || secret.StringData[key]+string(secret.Data[key]) != keySecretValue {
			t.Fatalf("%s: Secret keys data=%v stringData=%v, want only %s", protocol, secret.Data, secret.StringData, key)
		}
		var connection api.ModelConnection
		if err := srv.client.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "mine-connection"}, &connection); err != nil {
			t.Fatal(err)
		}
		if connection.Spec.MaxOutputTokens != 2048 || connection.Spec.Parameters == nil || connection.Spec.CredentialProfile != "" {
			t.Fatalf("%s: connection %+v", protocol, connection.Spec)
		}
	}
}

func TestCreateKeylessCellnAgentDoesNotCreateCompatibilitySecret(t *testing.T) {
	connection := &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "local"}, Spec: api.ModelConnectionSpec{Provider: "llama-server", Protocol: "openai-chat", Endpoint: "http://framework:8080/v1/chat/completions", AllowInsecure: true, Models: []string{"local"}}}
	srv := keySecretServer(t, connection)
	res := httptest.NewRecorder()
	body := `{"name":"local-agent","provider":"llama-server","model":"local","runtimeRef":"celln-native","execution":{"backend":"celln","executionLifecycle":"enduring","modelConnectionRef":"local","model":"local","cellnSelection":{"runtimeRef":"celln-native","toolRefs":[]},"enduring":{"leaseSeconds":600,"maxTurns":2,"maxModelRequests":12,"maxOutputTokens":6144}}}`
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/agents?namespace=team-a", strings.NewReader(body)))
	if res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	var secrets corev1.SecretList
	if err := srv.client.List(context.Background(), &secrets); err != nil || len(secrets.Items) != 0 {
		t.Fatalf("keyless agent created Secrets: %d %v", len(secrets.Items), err)
	}
}
