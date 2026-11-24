// Command substrate-e2e runs AgentRuns with spec.backend: substrate through
// the real AgentRun controller and the real model gateway, against a real
// Agent Substrate install (spike 0b, #699, checkpoints 4 and 7). It is not
// shipped.
//
// The Kubernetes API is a local envtest API server, so nothing is installed
// on the cluster that hosts Substrate: the controller and gateway run in this
// process, and actors reach them through Substrate's egress gateway at
// -public-host. Substrate's Control API and issuer are reached by the
// SUBSTRATE_* settings the shipped binaries read.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller"
	"github.com/sympozium-ai/sympozium/internal/harnessgateway"
	"github.com/sympozium-ai/sympozium/internal/orchestrator"
	"github.com/sympozium-ai/sympozium/internal/substrate"
)

type staticProfiles struct{ set *harnessgateway.Set }

func (s staticProfiles) Load(context.Context) (*harnessgateway.Set, error) { return s.set, nil }

type timing struct {
	Run        string  `json:"run"`
	Phase      string  `json:"phase"`
	RunningMs  float64 `json:"createdToRunningMs"`
	OutcomeMs  float64 `json:"createdToResultReportedMs"`
	TerminalMs float64 `json:"createdToTerminalMs"`
	ReleasedMs float64 `json:"createdToActorReleasedMs"`
	Worker     string  `json:"worker"`
	Result     string  `json:"result"`
	Error      string  `json:"error,omitempty"`
	Tokens     int     `json:"totalTokens"`
	LogBytes   int     `json:"logBytes"`
}

func main() {
	n := flag.Int("n", 5, "runs")
	parallel := flag.Int("parallel", 1, "runs in flight at once")
	provider := flag.String("provider", "openai", "spec.model.provider; openai uses a stub provider in this process")
	modelName := flag.String("model", "stub", "spec.model.model")
	keyEnv := flag.String("key-env", "", "environment variable holding a real provider key (never printed); empty uses the stub")
	listenTurn := flag.String("listen-turn", "0.0.0.0:18777", "turn endpoint bind address")
	listenGateway := flag.String("listen-gateway", "0.0.0.0:18778", "model gateway bind address for actors")
	verbose := flag.Bool("v", false, "controller logs")
	flag.Parse()

	if *verbose {
		ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	} else {
		ctrl.SetLogger(logr.Discard())
	}
	settings, err := substrate.SettingsFromEnv(os.Getenv)
	must(err)
	if settings == nil {
		must(fmt.Errorf("SUBSTRATE_* settings are required"))
	}
	settings.TurnListen = *listenTurn
	if !settings.AllowPlaintextGatewayHop {
		must(fmt.Errorf("this driver serves the turn endpoint and the model gateway to actors in clear text; set SUBSTRATE_ALLOW_PLAINTEXT_GATEWAY_HOP=true to acknowledge that"))
	}

	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(api.AddToScheme(scheme))
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("config", "crd", "bases")}, Scheme: scheme}
	cfg, err := env.Start()
	must(err)
	defer func() { _ = env.Stop() }()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	must(err)

	// Provider profiles: what the chart ships, plus a stub for "openai".
	realKey := "sk-e2e-stub-key-0123456789abcdef"
	shipped, err := os.ReadFile("charts/sympozium/files/harness-gateway/profiles.yaml")
	must(err)
	allowed, err := os.ReadFile("charts/sympozium/files/harness-gateway/allowed.yaml")
	must(err)
	profiles := string(allowed) + "profiles:\n  " + strings.ReplaceAll(strings.TrimRight(string(shipped), "\n"), "\n", "\n  ") + "\n"
	var stubCalls, stubRefused int
	var stubMu sync.Mutex
	if *keyEnv == "" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		must(err)
		origin := "http://" + listener.Addr().String()
		go func() {
			_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				stubMu.Lock()
				defer stubMu.Unlock()
				if r.Header.Get("Authorization") != "Bearer "+realKey {
					stubRefused++
					http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
					return
				}
				stubCalls++
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "chatcmpl-stub", "object": "chat.completion", "created": time.Now().Unix(), "model": "stub",
					"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
						"message": map[string]any{"role": "assistant", "content": "the provider saw the Agent's key"}}},
					"usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12},
				})
			}))
		}()
		profiles = strings.Replace(profiles, "upstream: https://api.openai.com", "upstream: "+origin, 1)
		profiles = "insecureOrigins: [\"" + origin + "\"]\n" + profiles
	} else {
		realKey = os.Getenv(*keyEnv)
		if realKey == "" {
			must(fmt.Errorf("%s is empty", *keyEnv))
		}
	}
	set, err := harnessgateway.ParseSet([]byte(profiles))
	must(err)
	if len(set.Rejected) > 0 {
		must(fmt.Errorf("profiles rejected: %v", set.Rejected))
	}

	reconciler := &controller.AgentRunReconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Scheme: scheme,
		Log: ctrl.Log.WithName("agentrun"), PodBuilder: orchestrator.NewPodBuilder("e2e"), ImageTag: "e2e",
		RunHistoryLimit: 1000,
		HarnessGateway:  &controller.HarnessGatewayConfig{URL: "unused", Profiles: staticProfiles{set}, Reader: mgr.GetAPIReader()},
	}
	turns, err := controller.SetupSubstrate(reconciler, settings)
	must(err)
	must(mgr.Add(turns))
	must(reconciler.SetupWithManager(mgr))

	// The gateway as cmd/harness-gateway runs it for actors: uncached reads,
	// the cleartext actor listener, the install's issuer.
	verifier, err := settings.Verifier()
	must(err)
	uncached, err := client.New(cfg, client.Options{Scheme: scheme})
	must(err)
	gateway := &harnessgateway.Gateway{Reader: uncached, Profiles: staticProfiles{set}, Actors: verifier, MaxConcurrent: 256, MaxConcurrentPerRun: 16}
	go func() { must(http.ListenAndServe(*listenGateway, gateway.ActorHandler())) }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	go func() { must(mgr.Start(ctx)) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		must(fmt.Errorf("cache did not sync"))
	}

	// The namespace is the atespace, so name it for the spike.
	const ns = "symp-e2e"
	secretKey := map[string]string{"openai": "OPENAI_API_KEY", "deepseek": "DEEPSEEK_API_KEY", "anthropic": "ANTHROPIC_API_KEY"}[*provider]
	if secretKey == "" {
		must(fmt.Errorf("no secret key name known for provider %q", *provider))
	}
	for _, obj := range []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-key", Namespace: ns}, Data: map[string][]byte{secretKey: []byte(realKey)}},
		&api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "spike-agent", Namespace: ns}, Spec: api.AgentSpec{
			AuthRefs: []api.SecretRef{{Provider: *provider, Secret: "agent-key"}},
			Agents:   api.AgentsSpec{Default: api.AgentConfig{Model: *modelName}},
		}},
	} {
		must(uncached.Create(ctx, obj))
	}

	stamp := time.Now().Format("150405")
	results := make([]timing, *n)
	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	batchStart := time.Now()
	for i := 0; i < *n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = one(ctx, uncached, ns, fmt.Sprintf("run-%s-%03d", stamp, i), *provider, *modelName, realKey)
		}(i)
	}
	wg.Wait()
	batchMs := ms(time.Since(batchStart))

	out := map[string]any{
		"backend": "substrate", "provider": *provider, "model": *modelName, "realProvider": *keyEnv != "",
		"runs": results, "parallel": *parallel, "batchMs": batchMs, "summary": summarise(results),
	}
	if *keyEnv == "" {
		stubMu.Lock()
		out["providerCallsWithAgentKey"], out["providerCallsWithoutAgentKey"] = stubCalls, stubRefused
		stubMu.Unlock()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	must(enc.Encode(out))
}

func one(ctx context.Context, c client.Client, ns, name, provider, model, realKey string) timing {
	var task api.TaskSpec
	_ = json.Unmarshal([]byte(`"Reply with the single word: done."`), &task)
	run := &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: api.AgentRunSpec{AgentRef: "spike-agent", AgentID: "spike-agent", Task: &task, Backend: "substrate",
			Model: api.ModelSpec{Provider: provider, Model: model, AuthSecretRef: "agent-key"}},
	}
	t := timing{Run: name}
	start := time.Now()
	if err := c.Create(ctx, run); err != nil {
		t.Error = err.Error()
		return t
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		var got api.AgentRun
		if err := c.Get(ctx, client.ObjectKeyFromObject(run), &got); err != nil {
			t.Error = err.Error()
			return t
		}
		since := ms(time.Since(start))
		if t.RunningMs == 0 && got.Status.Phase == api.AgentRunPhaseRunning {
			t.RunningMs = since
		}
		if st := got.Status.Substrate; st != nil {
			if t.OutcomeMs == 0 && st.Outcome != nil {
				t.OutcomeMs = since
			}
			t.Worker = st.Worker
			if got.Status.Phase.IsTerminal() && t.TerminalMs == 0 {
				t.TerminalMs = since
			}
			if st.Released && len(got.Finalizers) == 0 {
				t.ReleasedMs = since
			}
		} else if got.Status.Phase.IsTerminal() {
			t.TerminalMs, t.ReleasedMs = since, since
		}
		if got.Status.Phase.IsTerminal() && t.ReleasedMs != 0 {
			t.Phase, t.Result, t.Error = string(got.Status.Phase), got.Status.Result, got.Status.Error
			if got.Status.TokenUsage != nil {
				t.Tokens = got.Status.TokenUsage.TotalTokens
			}
			var logs corev1.ConfigMap
			if c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "sympozium-run-logs-" + name}, &logs) == nil {
				t.LogBytes = len(logs.Data["agent.log"])
				if strings.Contains(logs.Data["agent.log"], realKey) {
					t.Error = "THE AGENT'S KEY APPEARS IN THE ACTOR'S LOGS"
				}
			}
			if strings.Contains(got.Status.Result, realKey) {
				t.Error = "THE AGENT'S KEY APPEARS IN THE RESULT"
			}
			return t
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error = "not finished within 3m"
	return t
}

func summarise(runs []timing) map[string]any {
	pick := func(f func(timing) float64) map[string]float64 {
		var v []float64
		for _, r := range runs {
			if r.Phase == string(api.AgentRunPhaseSucceeded) {
				v = append(v, f(r))
			}
		}
		if len(v) == 0 {
			return nil
		}
		sort.Float64s(v)
		q := func(p float64) float64 { return v[int(p*float64(len(v)-1)+0.5)] }
		return map[string]float64{"p50": q(0.5), "p95": q(0.95), "min": v[0], "max": v[len(v)-1]}
	}
	ok := 0
	for _, r := range runs {
		if r.Phase == string(api.AgentRunPhaseSucceeded) {
			ok++
		}
	}
	return map[string]any{
		"succeeded": ok, "of": len(runs),
		"createdToRunningMs":        pick(func(t timing) float64 { return t.RunningMs }),
		"createdToResultReportedMs": pick(func(t timing) float64 { return t.OutcomeMs }),
		"createdToTerminalMs":       pick(func(t timing) float64 { return t.TerminalMs }),
		"createdToActorReleasedMs":  pick(func(t timing) float64 { return t.ReleasedMs }),
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "substrate-e2e:", err)
		os.Exit(1)
	}
}
