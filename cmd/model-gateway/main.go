// model-gateway is a separate credential-custody process, never a web-proxy mode.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	cap "github.com/sympozium-ai/sympozium/internal/cellncapability"
	"github.com/sympozium-ai/sympozium/internal/modelbudget"
	"github.com/sympozium-ai/sympozium/internal/modelgateway"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type config struct {
	ClusterID             string   `json:"clusterId"`
	Issuer                string   `json:"issuer"`
	Listen                string   `json:"listen"`
	TLSCertificateFile    string   `json:"tlsCertificateFile"`
	TLSKeyFile            string   `json:"tlsKeyFile"`
	VerificationKeysFile  string   `json:"verificationKeysFile"`
	RegistrationTokenFile string   `json:"registrationTokenFile"`
	DatabaseURLFile       string   `json:"databaseUrlFile"`
	PrivateOrigins        []string `json:"privateOrigins"`
}

func main() {
	path := flag.String("config", "", "operator-controlled JSON configuration file")
	flag.Parse()
	if err := run(*path); err != nil {
		fmt.Fprintln(os.Stderr, "model-gateway: startup or serving failed; check operator configuration and dependency readiness")
		os.Exit(1)
	}
}
func run(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg config
	if err = cap.StrictDecode(raw, &cfg); err != nil {
		return err
	}
	if cfg.ClusterID == "" || cfg.Issuer == "" || cfg.Listen == "" || cfg.TLSCertificateFile == "" || cfg.TLSKeyFile == "" {
		return errors.New("missing gateway configuration")
	}
	keys, err := loadKeys(cfg.VerificationKeysFile)
	if err != nil {
		return err
	}
	verifier, err := cap.NewVerifier(cfg.Issuer, keys, nil)
	if err != nil {
		return err
	}
	registration, err := readSecret(cfg.RegistrationTokenFile)
	if err != nil {
		return err
	}
	database, err := readSecret(cfg.DatabaseURLFile)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	pool, err := pgxpool.New(ctx, database)
	if err != nil {
		return err
	}
	defer pool.Close()
	budgets, err := modelbudget.New(pool)
	if err != nil {
		return err
	}
	authorities, err := modelgateway.NewPostgresAuthorityStore(pool)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	if err = clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err = api.AddToScheme(scheme); err != nil {
		return err
	}
	rest, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	rest.Timeout = 5 * time.Second
	reader, err := client.New(rest, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	private := map[string]bool{}
	for _, origin := range cfg.PrivateOrigins {
		private[origin] = true
	}
	gateway, err := modelgateway.New(modelgateway.Config{ClusterID: cfg.ClusterID, RegistrationToken: cap.NewToken(registration), AllowPrivateOrigins: private}, verifier, reader, budgets, authorities)
	if err != nil {
		return err
	}
	if err = gateway.Ready(ctx); err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.Listen, Handler: gateway.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 3 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	defer signal.Stop(reload)
	go func() {
		for {
			select {
			case <-ctx.Done():
				shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
				defer done()
				_ = server.Shutdown(shutdown)
				return
			case <-reload:
				next, err := loadKeys(cfg.VerificationKeysFile)
				if err == nil {
					err = verifier.ReloadKeys(next)
				}
				if err != nil {
					fmt.Fprintln(os.Stderr, "model-gateway: rejected verification key reload; previous trusted keyset retained")
				}
			}
		}
	}()
	err = server.ListenAndServeTLS(cfg.TLSCertificateFile, cfg.TLSKeyFile)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func readSecret(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return "", errors.New("operator secret file must be bounded and owner-only")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("empty operator secret")
	}
	return value, nil
}
func loadKeys(path string) ([]cap.VerificationKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, errors.New("keyset too large")
	}
	var document struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			X   string `json:"x"`
		} `json:"keys"`
	}
	if err = cap.StrictDecode(raw, &document); err != nil {
		return nil, err
	}
	out := make([]cap.VerificationKey, 0, len(document.Keys))
	for _, k := range document.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" || k.Use != "sig" || k.Alg != "EdDSA" {
			return nil, errors.New("unsupported verification key")
		}
		public, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(public) != ed25519.PublicKeySize {
			return nil, errors.New("invalid verification key")
		}
		out = append(out, cap.VerificationKey{KeyID: k.Kid, PublicKey: public})
	}
	return out, nil
}
