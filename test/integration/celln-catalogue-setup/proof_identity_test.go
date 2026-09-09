package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"os"
	"testing"
	"time"
)

// These identities are per-run and private to the proof. The standard httptest
// certificate/key is public source code and must not be used as a network trust
// boundary when exercising controller-Pod to host-service connectivity.
func freshProofCertificate(t *testing.T, host string) tls.Certificate {
	t.Helper()
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		t.Fatal("explicit certificate IP required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	must(t, err)
	now := time.Now()
	lifetime := time.Hour
	if os.Getenv("CELLN_LIVE_INTERACTIVE") == "1" {
		lifetime = 9 * time.Hour
	}
	certificate := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "isolated Celln proof"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(lifetime),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{ip},
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	must(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func freshProofToken(t *testing.T) []byte {
	t.Helper()
	var entropy [32]byte
	_, err := rand.Read(entropy[:])
	must(t, err)
	return []byte(hex.EncodeToString(entropy[:]))
}

func TestProofIdentitiesAreIndependentAndNameBound(t *testing.T) {
	first := freshProofCertificate(t, "10.89.0.1")
	second := freshProofCertificate(t, "10.89.0.1")
	if bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("certificate identity reused")
	}
	cert, err := x509.ParseCertificate(first.Certificate[0])
	must(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "10.89.0.1"})
	must(t, err)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1"}); err == nil {
		t.Fatal("unbound address accepted")
	}
	other, err := x509.ParseCertificate(second.Certificate[0])
	must(t, err)
	if _, err := other.Verify(x509.VerifyOptions{Roots: roots, DNSName: "10.89.0.1"}); err == nil {
		t.Fatal("another proof identity accepted")
	}
	a, b := freshProofToken(t), freshProofToken(t)
	if len(a) != 64 || bytes.Equal(a, b) {
		t.Fatal("invalid or reused credential")
	}
}
