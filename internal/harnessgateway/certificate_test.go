package harnessgateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeServingCertificate issues a leaf for 127.0.0.1 from ca and writes it.
func writeServingCertificate(t *testing.T, dir string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, serial int64) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "harness-gateway"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func TestAClientTrustingTheCAConnectsAcrossARenewal(t *testing.T) {
	dir := t.TempDir()
	ca, caKey := newCA(t)
	writeServingCertificate(t, dir, ca, caKey, 2)

	reloader := &CertificateReloader{CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key"), Interval: time.Nanosecond}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }),
		ReadHeaderTimeout: time.Second,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: reloader.GetCertificate},
		ErrorLog:          nil,
	}
	go func() { _ = server.ServeTLS(listener, "", "") }()
	defer func() { _ = server.Close() }()

	roots := x509.NewCertPool()
	roots.AddCert(ca)
	serial := func(transport *http.Transport) int64 {
		t.Helper()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		response, err := client.Get("https://" + listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		return response.TLS.PeerCertificates[0].SerialNumber.Int64()
	}
	trusting := func() *http.Transport {
		return &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, DisableKeepAlives: true}
	}
	if got := serial(trusting()); got != 2 {
		t.Fatalf("serial = %d", got)
	}

	writeServingCertificate(t, dir, ca, caKey, 3)
	if got := serial(trusting()); got != 3 {
		t.Errorf("the renewed certificate was not picked up: serial = %d", got)
	}

	// A broken renewal leaves the good certificate in use.
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := serial(trusting()); got != 3 {
		t.Errorf("serial = %d", got)
	}

	untrusting := &http.Client{Timeout: 5 * time.Second}
	if _, err := untrusting.Get("https://" + listener.Addr().String()); err == nil {
		t.Error("a client without the CA must not connect")
	}
}

func TestAnUnusableCertificateFailsAtStartup(t *testing.T) {
	dir := t.TempDir()
	reloader := &CertificateReloader{CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key")}
	if _, err := reloader.GetCertificate(nil); err == nil {
		t.Error("a missing certificate must fail")
	}
	ca, caKey := newCA(t)
	writeServingCertificate(t, dir, ca, caKey, 2)
	if err := os.Remove(filepath.Join(dir, "tls.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := reloader.GetCertificate(nil); err == nil {
		t.Error("a missing key must fail")
	}
}

// A renewed certificate with the old key must not be served.
func TestAMismatchedRenewalKeepsTheGoodCertificate(t *testing.T) {
	dir := t.TempDir()
	ca, caKey := newCA(t)
	writeServingCertificate(t, dir, ca, caKey, 2)
	reloader := &CertificateReloader{CertFile: filepath.Join(dir, "tls.crt"), KeyFile: filepath.Join(dir, "tls.key"), Interval: time.Nanosecond}
	first, err := reloader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := os.ReadFile(filepath.Join(dir, "tls.key"))
	if err != nil {
		t.Fatal(err)
	}
	writeServingCertificate(t, dir, ca, caKey, 3)
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), oldKey, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	second, err := reloader.GetCertificate(nil)
	if err != nil || second != first {
		t.Errorf("err = %v, certificate changed = %v", err, second != first)
	}
}
