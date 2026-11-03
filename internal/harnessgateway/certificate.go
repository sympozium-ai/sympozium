package harnessgateway

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"os"
	"sync"
	"time"
)

// CertificateReloader serves the mounted certificate and picks up a renewal
// with no restart. A renewal that cannot be loaded leaves the old one in use.
type CertificateReloader struct {
	CertFile, KeyFile string
	// Interval is how often the files are read again.
	Interval time.Duration

	mu        sync.Mutex
	cert      *tls.Certificate
	certPEM   []byte
	keyPEM    []byte
	checkedAt time.Time
}

// GetCertificate implements tls.Config.GetCertificate.
func (c *CertificateReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	interval := c.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if c.cert != nil && time.Since(c.checkedAt) < interval {
		return c.cert, nil
	}
	c.checkedAt = time.Now()
	err := c.reload()
	if c.cert == nil {
		return nil, fmt.Errorf("loading the gateway certificate: %w", err)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-gateway: keeping the current certificate: %v\n", err)
	}
	return c.cert, nil
}

func (c *CertificateReloader) reload() error {
	certPEM, err := os.ReadFile(c.CertFile)
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(c.KeyFile)
	if err != nil {
		return err
	}
	if c.cert != nil && bytes.Equal(certPEM, c.certPEM) && bytes.Equal(keyPEM, c.keyPEM) {
		return nil
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	c.cert, c.certPEM, c.keyPEM = &cert, certPEM, keyPEM
	return nil
}
