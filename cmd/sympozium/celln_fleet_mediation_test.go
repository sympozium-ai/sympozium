package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

func TestParseMediatedRoutes(t *testing.T) {
	anthropic := cellninstall.MediatedRoute{Provider: "anthropic", Protocol: "anthropic-messages", Models: []string{"claude-sonnet-5", "claude-opus-5"}, EndpointOrigins: []string{"https://api.anthropic.com"}}
	for _, tc := range []struct {
		name    string
		specs   []string
		want    []cellninstall.MediatedRoute
		refused string
	}{
		{name: "none declared"},
		{name: "models joined with +", specs: []string{"provider=anthropic,protocol=anthropic-messages,origin=https://api.anthropic.com,models=claude-sonnet-5+claude-opus-5"}, want: []cellninstall.MediatedRoute{anthropic}},
		{name: "repeated keys and spaces", specs: []string{"provider=anthropic, protocol=anthropic-messages, origin=https://api.anthropic.com, model=claude-sonnet-5, model=claude-opus-5"}, want: []cellninstall.MediatedRoute{anthropic}},
		{name: "several routes and origins", specs: []string{"provider=anthropic,protocol=anthropic-messages,origin=https://api.anthropic.com,models=claude-sonnet-5+claude-opus-5", "provider=openai,protocol=openai-chat,origins=https://api.openai.com+https://eu.api.openai.com,models=gpt-5"},
			want: []cellninstall.MediatedRoute{anthropic, {Provider: "openai", Protocol: "openai-chat", Models: []string{"gpt-5"}, EndpointOrigins: []string{"https://api.openai.com", "https://eu.api.openai.com"}}}},
		{name: "not key=value", specs: []string{"anthropic"}, refused: "expected key=value pairs"},
		{name: "unknown key", specs: []string{"provider=openai,protocol=openai-chat,origin=https://api.openai.com,models=gpt,endpoint=https://api.openai.com/v1"}, refused: `unknown key "endpoint"`},
		{name: "no provider", specs: []string{"protocol=openai-chat,origin=https://api.openai.com,models=gpt"}, refused: "provider"},
		{name: "unknown protocol", specs: []string{"provider=google,protocol=gemini,origin=https://g.example,models=g"}, refused: "protocol must be openai-chat or anthropic-messages"},
		{name: "no model", specs: []string{"provider=openai,protocol=openai-chat,origin=https://api.openai.com"}, refused: "1-32 models"},
		{name: "wildcard model", specs: []string{"provider=openai,protocol=openai-chat,origin=https://api.openai.com,models=gpt-*"}, refused: "no wildcard"},
		{name: "empty model in the list", specs: []string{"provider=openai,protocol=openai-chat,origin=https://api.openai.com,models=gpt+"}, refused: "exact identifiers"},
		{name: "plain HTTP origin", specs: []string{"provider=openai,protocol=openai-chat,origin=http://api.openai.com,models=gpt"}, refused: "a Secret never crosses plain HTTP"},
		{name: "origin with a port", specs: []string{"provider=openai,protocol=openai-chat,origin=https://api.openai.com:8443,models=gpt"}, refused: "without port, path or credentials"},
		{name: "origin with a path", specs: []string{"provider=openai,protocol=openai-chat,origin=https://api.openai.com/v1,models=gpt"}, refused: "without port, path or credentials"},
		{name: "the second route is checked too", specs: []string{"provider=openai,protocol=openai-chat,origin=https://api.openai.com,models=gpt", "provider=x,protocol=openai-chat,origin=*,models=m"}, refused: `"provider=x,protocol=openai-chat,origin=*,models=m"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMediatedRoutes(tc.specs)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) || !strings.Contains(err.Error(), "--celln-mediated-route") {
					t.Fatalf("refusal = %v, want one mentioning %q", err, tc.refused)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("routes = %+v %v", got, err)
			}
		})
	}
}

func TestParseKeylessMediatedRoute(t *testing.T) {
	routes, err := parseMediatedRoutes([]string{"provider=llama-server,protocol=openai-chat,auth=none,allowInsecure=true,origin=http://framework:8080,models=local"})
	if err != nil || len(routes) != 1 || routes[0].Auth != "none" || !routes[0].AllowInsecure {
		t.Fatalf("keyless route: %+v %v", routes, err)
	}
	values, err := cellninstall.MediationValues(false, routes)
	if err != nil || !strings.Contains(strings.Join(values, "\n"), "auth=none") || !strings.Contains(strings.Join(values, "\n"), "allowInsecure=true") {
		t.Fatalf("keyless values: %v %v", values, err)
	}
}

// The flags declare routes and nothing else: they are refused before any
// cluster change unless this install enables mediation, and they never turn a
// provider on by themselves.
func TestFleetMediationValues(t *testing.T) {
	enabled := []string{"celln.mediation.enabled=true", "celln.mediation.clusterId=evaluation"}
	route := "provider=anthropic,protocol=anthropic-messages,origin=https://api.anthropic.com,models=claude-sonnet-5"
	for _, tc := range []struct {
		name     string
		backends bool
		specs    []string
		set      []string
		want     []string
		refused  string
	}{
		{name: "nothing declared sets nothing", set: enabled},
		{name: "nothing declared, mediation off"},
		{name: "a route", specs: []string{route}, set: enabled, want: []string{`celln.mediation.routes[0].provider=anthropic`, `celln.mediation.routes[0].protocol=anthropic-messages`, `celln.mediation.routes[0].models[0]=claude\-sonnet\-5`, `celln.mediation.routes[0].endpointOrigins[0]=https\:\/\/api\.anthropic\.com`}},
		{name: "the fleet's backends", backends: true, set: enabled, want: []string{"celln.mediation.mediateBackends=true"}},
		{name: "a route without mediation", specs: []string{route}, refused: "which this install does not enable"},
		{name: "a route with mediation switched off", specs: []string{route}, set: []string{"celln.mediation.enabled=false"}, refused: "which this install does not enable"},
		{name: "backends without mediation", backends: true, refused: "which this install does not enable"},
		{name: "an invalid route is refused for its own reason first", specs: []string{"provider=openai,protocol=openai-chat,origin=http://api.openai.com,models=gpt"}, refused: "plain HTTP"},
		{name: "two sources of routes", specs: []string{route}, set: append([]string{"celln.mediation.routes[0].provider=openai"}, enabled...), refused: "not both"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cellnFleetFlags{mediateBackends: tc.backends, mediatedRouteSpecs: tc.specs}
			got, err := f.mediationValues(tc.set)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("refusal = %v, want one mentioning %q", err, tc.refused)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("values = %q %v", got, err)
			}
			for _, value := range got {
				if strings.HasPrefix(value, "celln.mediation.enabled") {
					t.Fatalf("a route flag switched mediation on: %q", got)
				}
			}
		})
	}
}

func TestMediationSummary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record cellninstall.MediationRecord
		want   string
	}{
		{name: "enabled without a route warns", record: cellninstall.MediationRecord{Enabled: true}, want: "AUTH_ROUTE_MISMATCH"},
		{name: "backends", record: cellninstall.MediationRecord{Enabled: true, MediateBackends: true}, want: "every HTTPS backend of this fleet"},
		{name: "a route", record: cellninstall.MediationRecord{Enabled: true, Routes: []cellninstall.MediatedRoute{{Provider: "openai", Protocol: "openai-chat", Models: []string{"a", "b"}, EndpointOrigins: []string{"https://api.openai.com"}}}}, want: "openai/a+b (openai-chat) at https://api.openai.com"},
	} {
		if got := mediationSummary(tc.record); !strings.Contains(got, tc.want) {
			t.Fatalf("%s: %q lacks %q", tc.name, got, tc.want)
		}
	}
}

// Without a fleet there is no execution policy to extend; the install stops
// before it touches the cluster.
func TestInstallRefusesMediatedRoutesWithoutAFleet(t *testing.T) {
	for _, args := range [][]string{
		{"--no-celln", "--celln-mediated-route", "provider=anthropic,protocol=anthropic-messages,origin=https://api.anthropic.com,models=claude-sonnet-5"},
		{"--no-celln", "--celln-mediate-backends"},
	} {
		cmd := newInstallCmd()
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "this install has no fleet") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
