package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const addedBackendKey = "sk-api-added-0123456789abcdef0123"

func mediationBackendServer(t *testing.T, mediated bool, objects ...client.Object) (*Server, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	installed := `[{"name":"native","provider":"deepseek","protocol":"openai-chat","endpoint":"https://api.deepseek.com/chat/completions","model":"deepseek-chat","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/native"}]`
	configure := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: cellninstall.FleetConfigureDaemonSet, Namespace: "celln-system"}, Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "configure", Env: []corev1.EnvVar{
		{Name: "FLEET_SCOPE", Value: "starter"}, {Name: "FLEET_PRINCIPAL", Value: "sympozium:celln"}, {Name: "FLEET_PACKAGE_HASH", Value: "blake3:" + strings.Repeat("b", 64)}, {Name: "FLEET_BACKENDS", Value: installed},
	}}}}}}}
	objects = append(objects, configure)
	if mediated {
		// The record the chart renders with celln.mediation.enabled; the
		// operator declared no mediateBackends, so only the mediation-only
		// marker can admit an added backend's route.
		objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "celln-system", Name: cellninstall.MediationRecordConfigMap}, Data: map[string]string{"mediation.json": `{"mediateBackends":false,"routes":[]}`}})
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	srv.completeCellnBackend = func(string, cellninstall.FleetFacts, string) {}
	return srv, cl
}

func postBackend(srv *Server, query, body string) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/celln-platform/backends"+query, strings.NewReader(body)))
	return res
}

func keyedHTTPSBackend(name string) string {
	return `{"name":"` + name + `","provider":"openai","model":"gpt-test","endpoint":"https://models.example.com/v1/chat/completions","credential":"` + addedBackendKey + `","skipPreflight":true}`
}

func fleetCredentials(t *testing.T, cl client.Client) map[string][]byte {
	t.Helper()
	var secret corev1.Secret
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "celln-system", Name: cellninstall.FleetModelCredentialSecret}, &secret); apierrors.IsNotFound(err) {
		return map[string][]byte{}
	} else if err != nil {
		t.Fatal(err)
	}
	return secret.Data
}

// With mediation on, a keyed HTTPS backend added through the API is
// mediation-only: its key becomes its starter Agent's own key and the fleet
// gets only the marker, which is what makes InstallPlatform offer it as an
// auth "secret" route with no host-profile route or shared wrappers.
func TestAddedBackendUnderMediationKeepsItsKeyOffTheFleet(t *testing.T) {
	srv, cl := mediationBackendServer(t, true, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}})
	res := postBackend(srv, "?namespace=team-a", keyedHTTPSBackend("cloud"))
	if res.Code != http.StatusAccepted {
		t.Fatalf("add: %d %s", res.Code, res.Body.String())
	}
	var added CellnFleetBackend
	if err := json.Unmarshal(res.Body.Bytes(), &added); err != nil || !added.Mediated || added.StarterAgent != "starter-cloud" || added.StarterNamespace != "team-a" {
		t.Fatalf("add response: %+v %v", added, err)
	}
	if strings.Contains(res.Body.String(), addedBackendKey) {
		t.Fatal("the key was returned")
	}
	fleet := fleetCredentials(t, cl)
	if string(fleet["cloud"]) != cellninstall.MediatedBackendCredential {
		t.Fatalf("fleet credential for the added backend: %q", fleet["cloud"])
	}
	for name, value := range fleet {
		if string(value) == addedBackendKey {
			t.Fatalf("the key reached the fleet Secret under %s", name)
		}
	}
	if only, err := cellninstall.MediatedOnlyBackends(t.Context(), cl); err != nil || !only["cloud"] {
		t.Fatalf("the added backend is not mediation-only: %v %v", only, err)
	}

	names := cellninstall.StarterAgentNamesFor("cloud")
	var secret corev1.Secret
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: names.Secret}, &secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["OPENAI_API_KEY"]) != addedBackendKey || secret.Annotations[modelkey.OwnerAnnotation] != "Agent/"+names.Agent {
		t.Fatalf("starter Secret: %v %v", secret.Annotations, secret.Data)
	}
	var connection sympoziumv1alpha1.ModelConnection
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: names.Connection}, &connection); err != nil || connection.Spec.SecretRef != names.Secret || connection.Spec.Endpoint != "https://models.example.com/v1/chat/completions" {
		t.Fatalf("starter connection: %+v %v", connection.Spec, err)
	}
	var agent sympoziumv1alpha1.Agent
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: names.Agent}, &agent); err != nil || !modelkey.Grants(&agent, "openai", names.Secret) || agent.Spec.RuntimeRef != cellnplatform.WrapperNames("cloud").Runtime {
		t.Fatalf("starter Agent: %+v %v", agent.Spec, err)
	}

	// The list shows it mediated, with its starter Agent.
	list := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/backends", nil))
	var backends []CellnFleetBackend
	if err := json.Unmarshal(list.Body.Bytes(), &backends); err != nil || len(backends) != 2 {
		t.Fatalf("list: %v %s", err, list.Body.String())
	}
	if b := backends[1]; b.Name != "cloud" || !b.Mediated || b.StarterAgent != names.Agent || b.StarterNamespace != "team-a" {
		t.Fatalf("listed: %+v", b)
	}
	if backends[0].Mediated {
		t.Fatalf("the installed fleet-keyed backend is listed mediated: %+v", backends[0])
	}
	// Adding it again is refused before anything is touched.
	if res := postBackend(srv, "?namespace=team-a", keyedHTTPSBackend("cloud")); res.Code != http.StatusConflict {
		t.Fatalf("re-add: %d %s", res.Code, res.Body.String())
	}
	if res := postBackend(srv, "?namespace=Not_A_Namespace", keyedHTTPSBackend("other")); res.Code != http.StatusBadRequest {
		t.Fatalf("bad namespace: %d %s", res.Code, res.Body.String())
	}
}

// A starter Secret another Agent owns is never taken: 409, and nothing is
// published or recorded for the backend.
func TestAddedBackendUnderMediationRefusesAnOwnedSecret(t *testing.T) {
	names := cellninstall.StarterAgentNamesFor("cloud")
	srv, cl := mediationBackendServer(t, true,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "someone"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: names.Secret, Annotations: map[string]string{modelkey.OwnerAnnotation: "Agent/someone"}}, Data: map[string][]byte{"OPENAI_API_KEY": []byte(addedBackendKey)}})
	res := postBackend(srv, "", keyedHTTPSBackend("cloud"))
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "belongs to Agent/someone") {
		t.Fatalf("owned Secret: %d %s", res.Code, res.Body.String())
	}
	if _, ok := fleetCredentials(t, cl)["cloud"]; ok {
		t.Fatal("a refused backend reached the fleet Secret")
	}
	if extra, _, err := cellninstall.ReadExtraBackends(t.Context(), cl); err != nil || len(extra) != 0 {
		t.Fatalf("a refused backend was recorded: %v %v", extra, err)
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: names.Agent}, &sympoziumv1alpha1.Agent{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a starter Agent was created for a refused backend: %v", err)
	}
}

// Without mediation, and under mediation for a backend with no key to protect
// or one a Secret may not travel to, the key (or keyless placeholder) is the
// fleet's as before and no starter Agent is created.
func TestAddedBackendKeepsTheFleetPathWhereMediationDoesNotApply(t *testing.T) {
	plainHTTP := `{"name":"lan","provider":"openai","model":"gpt-test","endpoint":"http://10.0.0.7:8080/v1/chat/completions","allowInsecure":true,"credential":"` + addedBackendKey + `","skipPreflight":true}`
	keyless := `{"name":"local","provider":"llama-server","model":"qwen.gguf","endpoint":"http://10.0.0.8:8080/v1/chat/completions","allowInsecure":true,"skipPreflight":true}`
	for _, tc := range []struct {
		name     string
		mediated bool
		body     string
		backend  string
		fleetKey bool
	}{
		{"mediation off", false, keyedHTTPSBackend("cloud"), "cloud", true},
		{"plain HTTP", true, plainHTTP, "lan", true},
		{"keyless", true, keyless, "local", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, cl := mediationBackendServer(t, tc.mediated, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}})
			res := postBackend(srv, "", tc.body)
			if res.Code != http.StatusAccepted {
				t.Fatalf("add: %d %s", res.Code, res.Body.String())
			}
			var added CellnFleetBackend
			if err := json.Unmarshal(res.Body.Bytes(), &added); err != nil || added.Mediated || added.StarterAgent != "" {
				t.Fatalf("add response: %+v %v", added, err)
			}
			value := string(fleetCredentials(t, cl)[tc.backend])
			if value == "" || value == cellninstall.MediatedBackendCredential || (value == addedBackendKey) != tc.fleetKey {
				t.Fatalf("fleet credential: %q", value)
			}
			var agents sympoziumv1alpha1.AgentList
			if err := cl.List(t.Context(), &agents); err != nil || len(agents.Items) != 0 {
				t.Fatalf("a starter Agent was created: %v %v", agents.Items, err)
			}
		})
	}
}
