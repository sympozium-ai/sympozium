package cellninstall

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func mediationStore(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	namespaces := []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sympozium-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fleetNamespace}},
	}
	return extraStore(t, append(namespaces, objects...)...)
}

func mediationOptions() MediationOptions {
	gateway, receiver := DefaultMediationHosts("sympozium", "sympozium-system")
	return MediationOptions{ClusterID: "evaluation", SystemNamespace: "sympozium-system", GatewayHosts: gateway, ReceiverHosts: append(receiver, "127.0.0.1")}
}

func TestMediationTrustIsWhatEveryComponentAccepts(t *testing.T) {
	ctx := context.Background()
	store := mediationStore(t)
	trust, err := PrepareMediationTrust(ctx, store, mediationOptions())
	if err != nil || !trust.Created || !strings.HasPrefix(trust.KeyID, "mediation-") {
		t.Fatalf("bootstrap: %+v %v", trust, err)
	}
	var controller, gateway, node corev1.Secret
	var systemTrust, nodeTrust corev1.ConfigMap
	for key, object := range map[types.NamespacedName]client.Object{
		{Namespace: "sympozium-system", Name: MediationControllerSecret}: &controller,
		{Namespace: "sympozium-system", Name: MediationGatewaySecret}:    &gateway,
		{Namespace: fleetNamespace, Name: MediationNodeSecret}:           &node,
		{Namespace: "sympozium-system", Name: MediationTrustConfigMap}:   &systemTrust,
		{Namespace: fleetNamespace, Name: MediationTrustConfigMap}:       &nodeTrust,
	} {
		if err := store.Get(ctx, key, object); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}

	// The controller's loader: one PKCS#8 Ed25519 PRIVATE KEY block, accepted
	// by the capability issuer under the fixed issuer name.
	block, rest := pem.Decode(controller.Data["issuer.key"])
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		t.Fatal("issuer.key is not one PKCS#8 block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	private, ok := parsed.(ed25519.PrivateKey)
	if err != nil || !ok {
		t.Fatalf("issuer key: %v", err)
	}
	if _, err := cap.NewIssuer(MediationIssuer, cap.SigningKey{KeyID: trust.KeyID, PrivateKey: private}, nil); err != nil {
		t.Fatalf("capability issuer refused the key: %v", err)
	}

	// The gateway's and Celln's keyset shape: exactly these members.
	var document struct {
		Keys []map[string]string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(systemTrust.Data["jwks.json"]), &document); err != nil || len(document.Keys) != 1 {
		t.Fatalf("jwks: %v %v", document, err)
	}
	key := document.Keys[0]
	want := map[string]string{"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": trust.KeyID, "x": base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey))}
	if len(key) != len(want) {
		t.Fatalf("jwks carries unexpected (possibly private) members: %v", key)
	}
	for name, value := range want {
		if key[name] != value {
			t.Fatalf("jwks %s = %q, want %q", name, key[name], value)
		}
	}
	public, _ := base64.RawURLEncoding.DecodeString(key["x"])
	if _, err := cap.NewVerifier(MediationIssuer, []cap.VerificationKey{{KeyID: key["kid"], PublicKey: public}}, nil); err != nil {
		t.Fatalf("capability verifier refused the keyset: %v", err)
	}
	if nodeTrust.Data["jwks.json"] != systemTrust.Data["jwks.json"] || nodeTrust.Data["ca.crt"] != systemTrust.Data["ca.crt"] {
		t.Fatal("the two namespaces received different public trust")
	}

	// Tokens: shared pairwise, independent of each other, long enough for Celln.
	if string(controller.Data["receiver-token"]) != string(node.Data["operator-token"]) || string(controller.Data["gateway-token"]) != string(gateway.Data["registration-token"]) {
		t.Fatal("token pairs differ")
	}
	if string(controller.Data["receiver-token"]) == string(controller.Data["gateway-token"]) || len(node.Data["operator-token"]) < 24 {
		t.Fatal("tokens are not independent bearer credentials")
	}
	// Custody split: no signing key off the controller, no gateway token on nodes.
	for name, secret := range map[string]corev1.Secret{"gateway": gateway, "node": node} {
		for k := range secret.Data {
			if k == "issuer.key" || (name == "node" && k == "registration-token") {
				t.Fatalf("%s Secret holds %s", name, k)
			}
		}
	}
	for _, public := range []string{systemTrust.Data["jwks.json"], systemTrust.Data["ca.crt"]} {
		if strings.Contains(public, "PRIVATE") || strings.Contains(public, `"d"`) {
			t.Fatal("private material in the public ConfigMap")
		}
	}

	// TLS: a server using the receiver pair is trusted through ca.crt alone,
	// under a bootstrapped name, by a client configured like the controller's.
	pair, err := tls.X509KeyPair(node.Data["tls.crt"], node.Data["tls.key"])
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	defer server.Close()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(systemTrust.Data["ca.crt"])) {
		t.Fatal("ca.crt holds no certificate")
	}
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}}
	response, err := httpClient.Get(server.URL)
	if err != nil {
		t.Fatalf("receiver certificate not trusted through ca.crt: %v", err)
	}
	response.Body.Close()
	gatewayBlock, _ := pem.Decode(gateway.Data["tls.crt"])
	gatewayCert, err := x509.ParseCertificate(gatewayBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gatewayCert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "sympozium-model-gateway.sympozium-system.svc"}); err != nil {
		t.Fatalf("gateway certificate: %v", err)
	}
	if _, err := gatewayCert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "celln-scoped-receiver.celln-system.svc"}); err == nil {
		t.Fatal("gateway certificate also answers for the receiver")
	}

	// The printed values carry names and the public key id only.
	values := trust.Values()
	for _, secret := range []string{string(controller.Data["receiver-token"]), string(controller.Data["gateway-token"]), "PRIVATE KEY"} {
		if strings.Contains(values, secret) {
			t.Fatal("values output carries a credential")
		}
	}
	if !strings.Contains(values, trust.KeyID) || !strings.Contains(values, "clusterId: \"evaluation\"") {
		t.Fatalf("values output incomplete:\n%s", values)
	}

	// A second run verifies and keeps the installation.
	again, err := PrepareMediationTrust(ctx, store, mediationOptions())
	if err != nil || again.Created || again.KeyID != trust.KeyID {
		t.Fatalf("rerun replaced or lost trust: %+v %v", again, err)
	}
	var after corev1.Secret
	if err := store.Get(ctx, types.NamespacedName{Namespace: "sympozium-system", Name: MediationControllerSecret}, &after); err != nil || string(after.Data["issuer.key"]) != string(controller.Data["issuer.key"]) {
		t.Fatal("rerun rotated the signing key")
	}
}

func TestMediationTrustIsNeverRepairedByReplacement(t *testing.T) {
	ctx := context.Background()
	store := mediationStore(t)
	if _, err := PrepareMediationTrust(ctx, store, mediationOptions()); err != nil {
		t.Fatal(err)
	}
	var node corev1.Secret
	key := types.NamespacedName{Namespace: fleetNamespace, Name: MediationNodeSecret}
	if err := store.Get(ctx, key, &node); err != nil {
		t.Fatal(err)
	}
	node.Data["operator-token"] = []byte("another-operator-token-0123456789")
	if err := store.Update(ctx, &node); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareMediationTrust(ctx, store, mediationOptions()); err == nil || !strings.Contains(err.Error(), "operator-token") || strings.Contains(err.Error(), "another-operator-token") {
		t.Fatalf("diverged tokens accepted or leaked: %v", err)
	}
	if err := store.Delete(ctx, &node); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareMediationTrust(ctx, store, mediationOptions()); err == nil || !strings.Contains(err.Error(), "partially present (4 of 5") {
		t.Fatalf("partial trust completed: %v", err)
	}
}

func TestMediationTrustRefusesIncompleteInput(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*MediationOptions){
		"cluster id":       func(o *MediationOptions) { o.ClusterID = "" },
		"spaced id":        func(o *MediationOptions) { o.ClusterID = "a b" },
		"namespace":        func(o *MediationOptions) { o.SystemNamespace = fleetNamespace },
		"gateway hosts":    func(o *MediationOptions) { o.GatewayHosts = nil },
		"receiver as URL":  func(o *MediationOptions) { o.ReceiverHosts = []string{"https://node:9443"} },
		"absent namespace": func(o *MediationOptions) { o.SystemNamespace = "elsewhere" },
	} {
		o := mediationOptions()
		mutate(&o)
		if _, err := PrepareMediationTrust(ctx, mediationStore(t), o); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}
