// Package harnessgateway lets a harness reach its model provider without the
// provider key entering the run pod. The harness holds a per-run token. The
// gateway checks the token, swaps it for the key and forwards the request.
package harnessgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// DataKey is the ConfigMap key holding the profiles.
const DataKey = "profiles.yaml"

// AuthTypeHeader sends the key in one request header.
const AuthTypeHeader = "header"

// Profile says how one family of providers is reached. It is operator
// configuration, because it decides where a real key goes.
type Profile struct {
	Name string `json:"name"`
	// Providers are the spec.model.provider values this profile serves.
	Providers []string `json:"providers"`
	// Upstream is the default origin plus any base path. spec.model.baseURL
	// replaces it.
	Upstream string `json:"upstream"`
	// SecretKeys are tried in order against spec.model.authSecretRef.
	SecretKeys []string `json:"secretKeys"`
	Auth       Auth     `json:"auth"`
	// Credentials are variants chosen by what the real key looks like. The first
	// whose KeyPrefix matches applies. A key that matches none uses Auth and a
	// plain token.
	Credentials []Credential `json:"credentials,omitempty"`
	// Paths is the allowlist. A "*" segment matches exactly one segment.
	Paths []string `json:"paths"`
	Env   Env      `json:"env"`
	// ClientBasePath is appended to the gateway URL in Env.BaseURL.
	ClientBasePath string `json:"clientBasePath,omitempty"`
}

// Auth says how the key is sent. Type is required so that a later scheme is a
// rejected entry on an older binary, not a silently ignored setting.
type Auth struct {
	Type   string `json:"type"`
	Header string `json:"header"`
	Prefix string `json:"prefix,omitempty"`
}

// Credential is one kind of key a profile can hold. Some harnesses choose their
// mode from the key's format, so the token has to look like the kind of key it
// stands in for, and the upstream may expect that kind in another header.
type Credential struct {
	// KeyPrefix selects this variant by the real key's prefix.
	KeyPrefix string `json:"keyPrefix"`
	// FakeKeyPattern is the shape of the token the harness holds. {token} is
	// replaced by random bytes, and it appears exactly once. It is static
	// configuration, so a token says what kind of key is behind it and nothing
	// more of the key itself.
	FakeKeyPattern string `json:"fakeKeyPattern,omitempty"`
	// Auth replaces the profile's Auth for this kind of key.
	Auth *Auth `json:"auth,omitempty"`
}

// Env names the variables the harness reads. Every name is set.
type Env struct {
	BaseURL []string `json:"baseURL"`
	APIKey  []string `json:"apiKey"`
}

// Set is a parsed profiles file.
type Set struct {
	privateOrigins []string
	// allowedEnvNames and allowedAuthHeaders are the only names a profile may
	// use. They are data so that a new provider needs no release.
	allowedEnvNames    []string
	allowedAuthHeaders []string
	// insecureOrigins may carry a key over plain HTTP. privateOrigins alone may
	// not: reaching a private address and sending a key in clear text are two
	// decisions, as privateReachable keeps them for Celln.
	insecureOrigins []string
	// allowedResponseHeaders are the provider headers relayed to the harness. A
	// trailing * matches a prefix.
	allowedResponseHeaders []string
	byProvider             map[string]*Profile
	// Rejected describes each entry that was skipped. A skipped entry leaves
	// its provider without a profile, so its runs fail closed.
	Rejected []string
}

// ParseSet reads a profiles file. A malformed entry is skipped and reported in
// Rejected. A later entry for a provider replaces an earlier one, so values can
// override what the chart ships.
func ParseSet(data []byte) (*Set, error) {
	var file struct {
		PrivateOrigins         []string          `json:"privateOrigins"`
		AllowedEnvNames        []string          `json:"allowedEnvNames"`
		AllowedAuthHeaders     []string          `json:"allowedAuthHeaders"`
		InsecureOrigins        []string          `json:"insecureOrigins"`
		AllowedResponseHeaders []string          `json:"allowedResponseHeaders"`
		Profiles               []json.RawMessage `json:"profiles"`
	}
	if err := yaml.UnmarshalStrict(data, &file); err != nil {
		return nil, fmt.Errorf("parsing harness gateway profiles: %w", err)
	}
	set := &Set{byProvider: map[string]*Profile{}}
	for _, name := range file.AllowedEnvNames {
		if !envNamePattern.MatchString(name) {
			return nil, fmt.Errorf("allowedEnvNames: %q is not an environment variable name", name)
		}
		set.allowedEnvNames = append(set.allowedEnvNames, name)
	}
	for _, header := range file.AllowedAuthHeaders {
		if !headerPattern.MatchString(header) {
			return nil, fmt.Errorf("allowedAuthHeaders: %q is not a header name", header)
		}
		set.allowedAuthHeaders = append(set.allowedAuthHeaders, http.CanonicalHeaderKey(header))
	}
	for _, origin := range file.PrivateOrigins {
		set.privateOrigins = append(set.privateOrigins, strings.ToLower(strings.TrimSuffix(origin, "/")))
	}
	for _, origin := range file.InsecureOrigins {
		set.insecureOrigins = append(set.insecureOrigins, strings.ToLower(strings.TrimSuffix(origin, "/")))
	}
	for _, header := range file.AllowedResponseHeaders {
		if !headerPattern.MatchString(header) {
			return nil, fmt.Errorf("allowedResponseHeaders: %q is not a header name", header)
		}
		set.allowedResponseHeaders = append(set.allowedResponseHeaders, http.CanonicalHeaderKey(header))
	}
	for i, raw := range file.Profiles {
		profile, err := decodeProfile(raw, set)
		if err != nil {
			set.Rejected = append(set.Rejected, fmt.Sprintf("profile %d: %v", i, err))
			continue
		}
		for _, provider := range profile.Providers {
			set.byProvider[strings.ToLower(provider)] = profile
		}
	}
	return set, nil
}

func decodeProfile(raw json.RawMessage, set *Set) (*Profile, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return nil, err
	}
	if err := profile.validate(set); err != nil {
		return nil, fmt.Errorf("%s: %w", profile.Name, err)
	}
	return &profile, nil
}

var (
	envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	headerPattern  = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
	pathPattern    = regexp.MustCompile(`^(/[A-Za-z0-9._~*:-]+)+$`)
	// fakeKeyPatternShape keeps a token safe in a header and an env var.
	fakeKeyPatternShape = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{0,64}\{token\}[A-Za-z0-9._~+/=-]{0,64}$`)
)

func (p *Profile) validate(set *Set) error {
	if p.Name == "" || len(p.Providers) == 0 || len(p.SecretKeys) == 0 || len(p.Paths) == 0 {
		return errors.New("name, providers, secretKeys and paths are required")
	}
	if err := p.Auth.validate(set); err != nil {
		return err
	}
	for _, credential := range p.Credentials {
		if credential.KeyPrefix == "" {
			return errors.New("credentials need a keyPrefix")
		}
		if credential.FakeKeyPattern != "" && !fakeKeyPatternShape.MatchString(credential.FakeKeyPattern) {
			return fmt.Errorf("fakeKeyPattern %q must hold {token} once, and only characters safe in a header", credential.FakeKeyPattern)
		}
		if credential.Auth != nil {
			if err := credential.Auth.validate(set); err != nil {
				return err
			}
		}
	}
	if _, _, err := set.upstreamOrigin(p.Upstream); err != nil {
		return err
	}
	for _, path := range p.Paths {
		if !validPath(path) {
			return fmt.Errorf("path %q is not allowed", path)
		}
	}
	if p.ClientBasePath != "" && !validPath(p.ClientBasePath) {
		return fmt.Errorf("clientBasePath %q is not allowed", p.ClientBasePath)
	}
	if len(p.Env.BaseURL) == 0 || len(p.Env.APIKey) == 0 {
		return errors.New("env.baseURL and env.apiKey are required")
	}
	for _, name := range slices.Concat(p.Env.BaseURL, p.Env.APIKey) {
		if !slices.Contains(set.allowedEnvNames, name) {
			return fmt.Errorf("env name %q is not in allowedEnvNames", name)
		}
	}
	return nil
}

func (a Auth) validate(set *Set) error {
	if a.Type != AuthTypeHeader {
		return fmt.Errorf("unknown auth.type %q", a.Type)
	}
	if !slices.Contains(set.allowedAuthHeaders, http.CanonicalHeaderKey(a.Header)) {
		return fmt.Errorf("auth.header %q is not in allowedAuthHeaders", a.Header)
	}
	if strings.ContainsAny(a.Prefix, "\r\n") {
		return errors.New("auth.prefix must be one line")
	}
	return nil
}

// Key returns the first of the profile's SecretKeys present in a Secret. Whitespace
// goes, because a token pasted from a wrapped terminal line often carries some.
func (p *Profile) Key(data map[string][]byte) string {
	for _, name := range p.SecretKeys {
		if key := strings.Join(strings.Fields(string(data[name])), ""); key != "" {
			return key
		}
	}
	return ""
}

func (p *Profile) credentialFor(key string) *Credential {
	for i := range p.Credentials {
		if strings.HasPrefix(key, p.Credentials[i].KeyPrefix) {
			return &p.Credentials[i]
		}
	}
	return nil
}

// AuthFor says how this key is sent upstream.
func (p *Profile) AuthFor(key string) Auth {
	if credential := p.credentialFor(key); credential != nil && credential.Auth != nil {
		return *credential.Auth
	}
	return p.Auth
}

// TokenFor shapes random bytes into the token a harness holds in place of this
// key. Only the matching variant's static pattern comes through, never the key.
func (p *Profile) TokenFor(key, random string) string {
	if credential := p.credentialFor(key); credential != nil && credential.FakeKeyPattern != "" {
		return strings.Replace(credential.FakeKeyPattern, "{token}", random, 1)
	}
	return random
}

// auths is every way this profile can send a key, so a harness may present its
// token, and only its token, in any of those headers.
func (p *Profile) auths() []Auth {
	auths := []Auth{p.Auth}
	for _, credential := range p.Credentials {
		if credential.Auth != nil {
			auths = append(auths, *credential.Auth)
		}
	}
	return auths
}

func validPath(path string) bool {
	if !pathPattern.MatchString(path) {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." || (strings.Contains(segment, "*") && segment != "*") {
			return false
		}
	}
	return true
}

// PathAllowed reports whether the profile allows this request path.
func (p *Profile) PathAllowed(path string) bool {
	got := strings.Split(path, "/")
	return slices.ContainsFunc(p.Paths, func(allowed string) bool {
		want := strings.Split(allowed, "/")
		if len(want) != len(got) {
			return false
		}
		for i := range want {
			if want[i] != got[i] && (want[i] != "*" || got[i] == "") {
				return false
			}
		}
		return true
	})
}

// Route is where one run's model calls go.
type Route struct {
	Profile *Profile
	// Upstream is the base URL requests are forwarded under.
	Upstream string
	// Origin is scheme://host[:port], lowercased: what the key is pinned to.
	Origin string
	// Private means the origin is on an operator's list, so private addresses are
	// allowed for it.
	Private bool

	allowedResponseHeaders []string
}

// responseHeaderAllowed reports whether a provider's header reaches the harness.
// Celln relays none, because a header can echo a credential. A harness needs
// Retry-After and rate limits to back off, so these are listed instead.
func (r *Route) responseHeaderAllowed(header string) bool {
	header = http.CanonicalHeaderKey(header)
	if header == "Content-Type" {
		return true
	}
	return slices.ContainsFunc(r.allowedResponseHeaders, func(allowed string) bool {
		if prefix, ok := strings.CutSuffix(allowed, "*"); ok {
			return strings.HasPrefix(header, prefix)
		}
		return allowed == header
	})
}

// NoProfileError reports a provider with no usable profile.
type NoProfileError struct{ Provider string }

func (e *NoProfileError) Error() string {
	return fmt.Sprintf("the harness gateway has no profile for provider %q", e.Provider)
}

// Route resolves a run's provider and baseURL. Both come from the run's spec,
// never from a request.
func (s *Set) Route(provider, baseURL string) (*Route, error) {
	profile, ok := s.byProvider[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return nil, &NoProfileError{Provider: provider}
	}
	upstream := profile.Upstream
	if strings.TrimSpace(baseURL) != "" {
		upstream = strings.TrimSpace(baseURL)
	}
	origin, private, err := s.upstreamOrigin(upstream)
	if err != nil {
		return nil, err
	}
	return &Route{Profile: profile, Upstream: strings.TrimRight(upstream, "/"), Origin: origin, Private: private, allowedResponseHeaders: s.allowedResponseHeaders}, nil
}

func (s *Set) upstreamOrigin(upstream string) (string, bool, error) {
	u, err := url.Parse(upstream)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(u.Path, "..") {
		return "", false, fmt.Errorf("upstream %q must be a URL without credentials, query or fragment", upstream)
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	insecure := slices.Contains(s.insecureOrigins, origin)
	if u.Scheme != "https" && (u.Scheme != "http" || !insecure) {
		return "", false, fmt.Errorf("upstream origin %s must use https, or be listed in harnessGateway.insecureOrigins", origin)
	}
	return origin, insecure || slices.Contains(s.privateOrigins, origin), nil
}

// URL is where a request path goes. An upstream that ends in /v1 and a path
// that starts with it share one /v1.
func (r *Route) URL(path, rawQuery string) string {
	target := r.Upstream
	if strings.HasSuffix(target, "/v1") && strings.HasPrefix(path, "/v1/") {
		target = strings.TrimSuffix(target, "/v1")
	}
	target += path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	return target
}
