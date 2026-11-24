package substrate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Settings is how an operator points Sympozium at an Agent Substrate install.
// Every component reads the same SUBSTRATE_* variables, so the controller
// that binds a run to an actor and the gateway that checks the actor's JWT
// cannot disagree about issuer or audience.
type Settings struct {
	// Control API (controller only).
	APIEndpoint   string
	APIServerName string
	TokenFile     string
	RunnerImage   string
	// TurnListen is where the controller serves the turn endpoint; TurnHost
	// and TurnPort are how actors address it. GatewayHost and GatewayPort are
	// how actors address the model gateway.
	TurnListen  string
	TurnHost    string
	TurnPort    int32
	GatewayHost string
	GatewayPort int32

	// Actor JWT verification (controller and gateway).
	Issuer         string
	JWKSURL        string
	JWKSServerName string
	Audience       string
	// CAFile verifies Substrate's serving certificates: the Control API and
	// the issuer's key set.
	CAFile string

	// AllowPlaintextGatewayHop is the operator's acknowledgement that
	// Substrate's egress gateway reaches the model gateway and the turn
	// endpoint over clear-text HTTP inside the cluster: it cannot be given
	// the private CA that would verify them. What crosses that hop is a
	// five-minute JWT for one actor and one audience, and the run's prompts
	// and responses. Without the acknowledgement the backend refuses every
	// run and neither clear-text listener is opened.
	//
	// It is only safe on a CNI that enforces NetworkPolicy: see
	// PlaintextHopWarning.
	AllowPlaintextGatewayHop bool

	// Default makes Substrate the execution plane of runs that name no
	// backend (controller only). On unless SUBSTRATE_DEFAULT is false.
	Default bool
}

// PlaintextHopWarning is logged by the controller and the model gateway when
// the clear-text hop is acknowledged. Nothing in either process can tell
// whether the cluster's CNI enforces NetworkPolicy, so the operator is told
// every time.
const PlaintextHopWarning = "WARNING: SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP=true (chart: substrate.allowPlaintextGatewayHop) " +
	"is only safe on a CNI that enforces NetworkPolicy (for example Calico or Cilium). " +
	"The clear-text listeners for Substrate actors are open, and NetworkPolicy is what keeps other workloads off them. " +
	"On a CNI that does not enforce it (for example flannel) anything in the cluster can reach those listeners, " +
	"and nothing confines the hop that carries actor JWTs and the runs' prompts and responses in clear text"

// SettingsFromEnv reads SUBSTRATE_* variables. It returns nil when
// SUBSTRATE_JWT_ISSUER is unset: Substrate is not configured.
func SettingsFromEnv(getenv func(string) string) (*Settings, error) {
	if getenv("SUBSTRATE_JWT_ISSUER") == "" {
		return nil, nil
	}
	s := &Settings{
		APIEndpoint:    getenv("SUBSTRATE_API_ENDPOINT"),
		APIServerName:  getenv("SUBSTRATE_API_SERVER_NAME"),
		TokenFile:      getenv("SUBSTRATE_TOKEN_FILE"),
		RunnerImage:    getenv("SUBSTRATE_RUNNER_IMAGE"),
		TurnListen:     getenv("SUBSTRATE_TURN_LISTEN"),
		TurnHost:       getenv("SUBSTRATE_TURN_HOST"),
		GatewayHost:    getenv("SUBSTRATE_GATEWAY_HOST"),
		Issuer:         getenv("SUBSTRATE_JWT_ISSUER"),
		JWKSURL:        getenv("SUBSTRATE_JWKS_URL"),
		JWKSServerName: getenv("SUBSTRATE_JWKS_SERVER_NAME"),
		Audience:       getenv("SUBSTRATE_JWT_AUDIENCE"),
		CAFile:         getenv("SUBSTRATE_CA_FILE"),
	}
	if s.Audience == "" {
		s.Audience = "sympozium"
	}
	// Anything but an explicit true is a refusal to run, and a typo is an
	// error rather than a silent "no".
	if v := getenv("SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP"); v != "" {
		allow, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP: %q is not true or false", v)
		}
		s.AllowPlaintextGatewayHop = allow
	}
	s.Default = true
	if v := getenv("SUBSTRATE_DEFAULT"); v != "" {
		def, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("SUBSTRATE_DEFAULT: %q is not true or false", v)
		}
		s.Default = def
	}
	if s.JWKSURL == "" {
		s.JWKSURL = strings.TrimRight(s.Issuer, "/") + "/openid/v1/jwks"
	}
	for name, dst := range map[string]*int32{"SUBSTRATE_TURN_PORT": &s.TurnPort, "SUBSTRATE_GATEWAY_PORT": &s.GatewayPort} {
		if v := getenv(name); v != "" {
			port, err := strconv.ParseInt(v, 10, 32)
			if err != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("%s: %q is not a port", name, v)
			}
			*dst = int32(port)
		}
	}
	return s, nil
}

// ValidateForController reports what the controller still needs.
func (s *Settings) ValidateForController() error {
	for name, value := range map[string]string{
		"SUBSTRATE_API_ENDPOINT": s.APIEndpoint, "SUBSTRATE_TOKEN_FILE": s.TokenFile, "SUBSTRATE_CA_FILE": s.CAFile,
		"SUBSTRATE_RUNNER_IMAGE": s.RunnerImage, "SUBSTRATE_TURN_LISTEN": s.TurnListen,
		"SUBSTRATE_TURN_HOST": s.TurnHost, "SUBSTRATE_GATEWAY_HOST": s.GatewayHost,
	} {
		if value == "" {
			return fmt.Errorf("%s is required when SUBSTRATE_JWT_ISSUER is set", name)
		}
	}
	if s.TurnPort == 0 || s.GatewayPort == 0 {
		return fmt.Errorf("SUBSTRATE_TURN_PORT and SUBSTRATE_GATEWAY_PORT are required")
	}
	return nil
}

func (s *Settings) roots() (*x509.CertPool, error) {
	pem, err := os.ReadFile(s.CAFile)
	if err != nil {
		return nil, fmt.Errorf("SUBSTRATE_CA_FILE: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("SUBSTRATE_CA_FILE %s holds no certificates", s.CAFile)
	}
	return pool, nil
}

// Dial connects to the Control API. The token file is read on every call, so
// a projected ServiceAccount token is picked up as the kubelet rotates it.
func (s *Settings) Dial() (*Client, error) {
	pool, err := s.roots()
	if err != nil {
		return nil, err
	}
	return Dial(Config{
		Endpoint: s.APIEndpoint, RootCAs: pool, ServerName: s.APIServerName,
		Token: func(context.Context) (string, error) {
			b, err := os.ReadFile(s.TokenFile)
			return strings.TrimSpace(string(b)), err
		},
	})
}

// Verifier checks actor JWTs against the install's issuer.
func (s *Settings) Verifier() (*JWTVerifier, error) {
	// The key set decides whose tokens are believed. Fetched in clear text,
	// anyone on the path could supply their own keys.
	if !strings.HasPrefix(s.JWKSURL, "https://") {
		return nil, fmt.Errorf("SUBSTRATE_JWKS_URL %q must be an https URL", s.JWKSURL)
	}
	pool, err := s.roots()
	if err != nil {
		return nil, err
	}
	return &JWTVerifier{
		Issuer: s.Issuer, Audience: s.Audience, JWKSURL: s.JWKSURL,
		Client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: s.JWKSServerName},
		}},
	}, nil
}
