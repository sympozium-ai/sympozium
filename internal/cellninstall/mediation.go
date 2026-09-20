package cellninstall

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Names shared between the chart's celln.mediation defaults and the bootstrap.
const (
	MediationControllerSecret = "celln-mediation-controller"
	MediationGatewaySecret    = "celln-mediation-gateway"
	MediationNodeSecret       = "celln-mediation-node"
	MediationTrustConfigMap   = "celln-mediation-trust"
	// MediationIssuer is fixed by the authorisation credential contract.
	MediationIssuer = "sympozium-control-plane"
)

// MediationOptions names where the mediated path's trust is published and the
// host names its two TLS endpoints are reached by.
type MediationOptions struct {
	ClusterID       string
	SystemNamespace string
	// GatewayHosts and ReceiverHosts become the certificates' subject
	// alternative names; they must contain the host of the chart's gateway
	// origin and of celln.mediation.receiver.url.
	GatewayHosts  []string
	ReceiverHosts []string
	Validity      time.Duration
	Now           func() time.Time
}

// MediationTrust is the credential-free result an operator feeds to the chart.
type MediationTrust struct {
	ClusterID string
	KeyID     string
	Created   bool
}

// Values renders the chart values matching the published trust. It carries no
// credential: only names and the public key id.
func (t MediationTrust) Values() string {
	return fmt.Sprintf("celln:\n  mediation:\n    enabled: true\n    clusterId: %q\n    issuer:\n      name: %s\n      keyId: %q\n    controllerSecret: %s\n    gatewaySecret: %s\n    nodeSecret: %s\n    trustConfigMap: %s\n",
		t.ClusterID, MediationIssuer, t.KeyID, MediationControllerSecret, MediationGatewaySecret, MediationNodeSecret, MediationTrustConfigMap)
}

// DefaultMediationHosts are the in-cluster names of the chart's gateway
// Service (for a release whose full name is fullname) and receiver Service.
func DefaultMediationHosts(fullname, systemNamespace string) (gateway, receiver []string) {
	gw := fmt.Sprintf("%s-model-gateway.%s.svc", fullname, systemNamespace)
	rc := "celln-scoped-receiver." + fleetNamespace + ".svc"
	return []string{gw, gw + ".cluster.local"}, []string{rc, rc + ".cluster.local"}
}

// PrepareMediationTrust mints, once, everything the mediated path shares: the
// issuer's Ed25519 signing key and its public JWKS, the two transport tokens,
// and a private CA with one server certificate each for the model gateway and
// the scoped receiver. Private material goes only into Secrets, split so that
// the gateway never holds the signing key and nodes never hold the gateway
// token; the JWKS and CA certificate go into a ConfigMap in both namespaces.
// The CA's private key is discarded after signing. An existing installation
// is verified for consistency and never replaced: rotate by deleting all five
// objects deliberately and bootstrapping again.
func PrepareMediationTrust(ctx context.Context, store client.Client, o MediationOptions) (MediationTrust, error) {
	if o.ClusterID == "" || len(o.ClusterID) > 253 || strings.ContainsAny(o.ClusterID, " \t\r\n") {
		return MediationTrust{}, fmt.Errorf("a cluster id of at most 253 characters without whitespace is required")
	}
	if o.SystemNamespace == "" || o.SystemNamespace == fleetNamespace {
		return MediationTrust{}, fmt.Errorf("the control-plane namespace is required and must differ from %s", fleetNamespace)
	}
	if len(o.GatewayHosts) == 0 || len(o.ReceiverHosts) == 0 {
		return MediationTrust{}, fmt.Errorf("gateway and receiver host names are required for their certificates")
	}
	for _, host := range append(append([]string{}, o.GatewayHosts...), o.ReceiverHosts...) {
		if host == "" || (strings.ContainsAny(host, " \t\r\n/:@") && net.ParseIP(host) == nil) {
			return MediationTrust{}, fmt.Errorf("invalid certificate host %q: a DNS name or IP address without scheme or port", host)
		}
	}
	if o.Validity <= 0 {
		o.Validity = 365 * 24 * time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	for _, namespace := range []string{o.SystemNamespace, fleetNamespace} {
		var ns corev1.Namespace
		if err := store.Get(ctx, types.NamespacedName{Name: namespace}, &ns); err != nil {
			return MediationTrust{}, fmt.Errorf("namespace %s: %w; install the Celln fleet first (sympozium install --celln-fleet)", namespace, err)
		}
	}
	objects := mediationObjects(o.SystemNamespace)
	found := 0
	for _, object := range objects {
		err := store.Get(ctx, client.ObjectKeyFromObject(object), object)
		switch {
		case err == nil:
			found++
		case !apierrors.IsNotFound(err):
			return MediationTrust{}, err
		}
	}
	switch found {
	case len(objects):
		keyID, err := verifyMediationTrust(objects)
		if err != nil {
			return MediationTrust{}, fmt.Errorf("existing mediation trust is inconsistent (%w); never repaired by replacement: delete all of it deliberately and bootstrap again", err)
		}
		return MediationTrust{ClusterID: o.ClusterID, KeyID: keyID}, nil
	case 0:
	default:
		return MediationTrust{}, fmt.Errorf("mediation trust is partially present (%d of %d objects); never completed by replacement: delete what exists deliberately and bootstrap again", found, len(objects))
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return MediationTrust{}, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return MediationTrust{}, err
	}
	issuerKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return MediationTrust{}, err
	}
	keyID := fmt.Sprintf("mediation-%s-%x", o.Now().UTC().Format("2006-01-02"), suffix)
	jwks, err := mediationJWKS(keyID, public)
	if err != nil {
		return MediationTrust{}, err
	}
	receiverToken, err := mediationToken()
	if err != nil {
		return MediationTrust{}, err
	}
	gatewayToken, err := mediationToken()
	if err != nil {
		return MediationTrust{}, err
	}
	ca, gatewayCert, gatewayKey, receiverCert, receiverKey, err := mediationCertificates(o)
	if err != nil {
		return MediationTrust{}, err
	}
	labels := fleetLabels()
	trust := map[string]string{"jwks.json": string(jwks), "ca.crt": string(ca)}
	for _, object := range []client.Object{
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: MediationTrustConfigMap, Namespace: o.SystemNamespace, Labels: labels}, Data: trust},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: MediationTrustConfigMap, Namespace: fleetNamespace, Labels: labels}, Data: trust},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: MediationGatewaySecret, Namespace: o.SystemNamespace, Labels: labels}, Data: map[string][]byte{"tls.crt": gatewayCert, "tls.key": gatewayKey, "registration-token": []byte(gatewayToken)}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: MediationNodeSecret, Namespace: fleetNamespace, Labels: labels}, Data: map[string][]byte{"tls.crt": receiverCert, "tls.key": receiverKey, "operator-token": []byte(receiverToken)}},
		// Last: the controller, sole holder of the signing key, cannot start
		// scoped dispatch against trust that was only partly published.
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: MediationControllerSecret, Namespace: o.SystemNamespace, Labels: labels}, Data: map[string][]byte{"issuer.key": issuerKey, "receiver-token": []byte(receiverToken), "gateway-token": []byte(gatewayToken)}},
	} {
		if err := store.Create(ctx, object); err != nil {
			return MediationTrust{}, fmt.Errorf("publish %s/%s: %w; partial mediation trust retained, never replaced", object.GetNamespace(), object.GetName(), err)
		}
	}
	return MediationTrust{ClusterID: o.ClusterID, KeyID: keyID, Created: true}, nil
}

func mediationObjects(systemNamespace string) []client.Object {
	return []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: MediationControllerSecret, Namespace: systemNamespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: MediationGatewaySecret, Namespace: systemNamespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: MediationNodeSecret, Namespace: fleetNamespace}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: MediationTrustConfigMap, Namespace: systemNamespace}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: MediationTrustConfigMap, Namespace: fleetNamespace}},
	}
}

// verifyMediationTrust proves the five objects still describe one installation
// and returns the signing key's id. It reports which relation broke, never a value.
func verifyMediationTrust(objects []client.Object) (string, error) {
	controller, gateway, node := objects[0].(*corev1.Secret), objects[1].(*corev1.Secret), objects[2].(*corev1.Secret)
	systemTrust, nodeTrust := objects[3].(*corev1.ConfigMap), objects[4].(*corev1.ConfigMap)
	if len(controller.Data["receiver-token"]) < 24 || !bytes.Equal(controller.Data["receiver-token"], node.Data["operator-token"]) {
		return "", fmt.Errorf("the controller's receiver-token and the node's operator-token differ")
	}
	if len(controller.Data["gateway-token"]) < 24 || !bytes.Equal(controller.Data["gateway-token"], gateway.Data["registration-token"]) {
		return "", fmt.Errorf("the controller's gateway-token and the gateway's registration-token differ")
	}
	if bytes.Equal(controller.Data["receiver-token"], controller.Data["gateway-token"]) {
		return "", fmt.Errorf("the receiver and gateway tokens must be independent")
	}
	if systemTrust.Data["jwks.json"] == "" || systemTrust.Data["jwks.json"] != nodeTrust.Data["jwks.json"] || systemTrust.Data["ca.crt"] == "" || systemTrust.Data["ca.crt"] != nodeTrust.Data["ca.crt"] {
		return "", fmt.Errorf("the two trust ConfigMaps differ")
	}
	block, rest := pem.Decode(controller.Data["issuer.key"])
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return "", fmt.Errorf("issuer.key is not one PKCS#8 PRIVATE KEY block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	private, ok := parsed.(ed25519.PrivateKey)
	if err != nil || !ok {
		return "", fmt.Errorf("issuer.key is not an Ed25519 key")
	}
	want := base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey))
	var document struct {
		Keys []struct {
			Kid string `json:"kid"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(systemTrust.Data["jwks.json"]), &document); err != nil {
		return "", fmt.Errorf("jwks.json is not a JSON keyset")
	}
	keyID := ""
	for _, key := range document.Keys {
		if key.X == want && key.Kid != "" {
			keyID = key.Kid
		}
	}
	if keyID == "" {
		return "", fmt.Errorf("jwks.json does not carry the issuer key's public half")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(systemTrust.Data["ca.crt"])) {
		return "", fmt.Errorf("ca.crt holds no certificate")
	}
	for name, secret := range map[string]*corev1.Secret{"gateway": gateway, "receiver": node} {
		block, _ := pem.Decode(secret.Data["tls.crt"])
		if block == nil {
			return "", fmt.Errorf("the %s certificate is missing", name)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("the %s certificate is invalid", name)
		}
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return "", fmt.Errorf("the %s certificate is expired or not signed by ca.crt", name)
		}
	}
	return keyID, nil
}

// mediationJWKS is the public keyset in exactly the shape cmd/model-gateway
// and Celln's verifier accept: OKP/Ed25519/sig/EdDSA members only.
func mediationJWKS(keyID string, public ed25519.PublicKey) ([]byte, error) {
	return json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": keyID, "x": base64.RawURLEncoding.EncodeToString(public)}}})
}

func mediationToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func mediationCertificates(o MediationOptions) (ca, gatewayCert, gatewayKey, receiverCert, receiverKey []byte, err error) {
	now := o.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return
	}
	serial := func() *big.Int {
		n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
		return n
	}
	caTemplate := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "sympozium celln mediation CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(o.Validity),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return
	}
	ca = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	leaf := func(name string, hosts []string) (cert, key []byte, err error) {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(o.Validity),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
		for _, host := range hosts {
			if ip := net.ParseIP(host); ip != nil {
				template.IPAddresses = append(template.IPAddresses, ip)
			} else {
				template.DNSNames = append(template.DNSNames, host)
			}
		}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
		if err != nil {
			return nil, nil, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
		if err != nil {
			return nil, nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
	}
	if gatewayCert, gatewayKey, err = leaf("sympozium model gateway", o.GatewayHosts); err != nil {
		return
	}
	receiverCert, receiverKey, err = leaf("celln scoped receiver", o.ReceiverHosts)
	return
}
