// Command substrate-turn runs agent-runner turns inside Agent Substrate
// actors and prints what each step cost (spike 0b, #699, checkpoint 2). It
// stands in for the controller: it serves the turn endpoint the actor pulls
// from, and a stub OpenAI-compatible model so the provider is out of the
// timings. It is not shipped.
//
// The actor reaches this process through the egress gateway, so -public-host
// must be a name the cluster resolves to wherever -listen is bound (on the
// spike cluster: a selector-less Service pointing at the node).
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sympozium-ai/sympozium/internal/substrate"
)

type iteration struct {
	Actor         string  `json:"actor"`
	CreateMs      float64 `json:"createMs"`
	EgressMs      float64 `json:"egressPolicyMs"`
	ResumeMs      float64 `json:"resumeMs"`
	ClaimMs       float64 `json:"resumeToTurnClaimedMs"`
	TurnMs        float64 `json:"turnMs"`
	DeleteMs      float64 `json:"deleteMs"`
	TotalMs       float64 `json:"totalMs"`
	Worker        string  `json:"worker"`
	ExitCode      int     `json:"exitCode"`
	Response      string  `json:"response"`
	ModelCalls    int     `json:"modelCalls"`
	ModelIdentity string  `json:"modelCallerIdentity"`
	// Key path only.
	Run             string `json:"run,omitempty"`
	ModelURL        string `json:"modelBaseURL,omitempty"`
	ProviderCalls   int    `json:"providerCallsWithAgentKey"`
	KeySeenInResult bool   `json:"agentKeyInResultOrLogs"`
}

// modelStub answers chat completions and records who the egress gateway said
// was calling. It refuses a request whose credential is not a valid actor JWT,
// which is the model gateway's job in the real path.
type modelStub struct {
	verifier substrate.Verifier
	mu       sync.Mutex
	calls    map[string]int
	refused  []string
}

func (m *modelStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	actor, err := m.verifier.Verify(r.Context(), token)
	m.mu.Lock()
	if err != nil {
		m.refused = append(m.refused, err.Error())
		m.mu.Unlock()
		http.Error(w, `{"error":{"message":"caller not identified"}}`, http.StatusUnauthorized)
		return
	}
	m.calls[actor.String()]++
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion", "created": time.Now().Unix(), "model": "stub",
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": "turn completed inside actor " + actor.String()}}},
		"usage": map[string]int{"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12},
	})
}

func main() {
	endpoint := flag.String("endpoint", "127.0.0.1:18443", "ate-api-server host:port")
	serverName := flag.String("server-name", "api.ate-system.svc", "name in the API serving certificate")
	tokenFile := flag.String("token-file", "", "bearer token file for the Control API")
	caFile := flag.String("ca-file", "", "PEM bundle for Substrate's serving certificates")
	atespace := flag.String("atespace", "symp-spike", "atespace to use")
	image := flag.String("image", "", "digest-pinned agent-runner image")
	listen := flag.String("listen", "0.0.0.0:18777", "address the turn endpoint and model stub bind")
	publicHost := flag.String("public-host", "", "hostname actors use to reach -listen")
	publicPort := flag.Int("public-port", 18777, "port actors use to reach -listen")
	issuer := flag.String("issuer", "", "actor JWT issuer")
	jwksURL := flag.String("jwks-url", "", "actor JWT key set URL")
	jwksServerName := flag.String("jwks-server-name", "", "name in the key set server's certificate")
	audience := flag.String("audience", "sympozium-spike", "audience actors' JWTs are minted for")
	n := flag.Int("n", 1, "turns to run, one actor each")
	keep := flag.Bool("keep", false, "leave actors in place")
	modelBase := flag.String("model-base-url", "", "override the model base URL handed to the runner (to prove egress refuses it)")
	useKeyPath := flag.Bool("key-path", false, "send model calls through the harness gateway, which adds the Agent's key (checkpoint 3)")
	impostor := flag.Bool("impostor", false, "with -key-path: add a run whose actor calls the first run's gateway path")
	cleanup := flag.Bool("cleanup", false, "delete every run-* actor in the atespace and exit")
	flag.Parse()

	pem, err := os.ReadFile(*caFile)
	must(err)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		must(fmt.Errorf("no certificates in %s", *caFile))
	}
	c, err := substrate.Dial(substrate.Config{
		Endpoint: *endpoint, RootCAs: pool, ServerName: *serverName,
		Token: func(context.Context) (string, error) { b, err := os.ReadFile(*tokenFile); return string(b), err },
	})
	must(err)
	defer c.Close()

	verifier := &substrate.JWTVerifier{Issuer: *issuer, Audience: *audience, JWKSURL: *jwksURL,
		Client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: *jwksServerName}}}}
	store := substrate.NewMemoryTurnStore()
	model := &modelStub{verifier: verifier, calls: map[string]int{}}
	mux := http.NewServeMux()
	mux.Handle("/v1/actor/", substrate.TurnHandler{Verifier: verifier, Store: store})
	mux.Handle("/v1/chat/completions", model)
	var keys *keyPath
	if *useKeyPath {
		keys, err = newKeyPath(verifier)
		must(err)
		mux.Handle("/runs/", keys.gateway)
	}
	go func() { must(http.ListenAndServe(*listen, mux)) }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	must(c.EnsureAtespace(ctx, *atespace))
	if *cleanup {
		actors, err := c.ListActors(ctx, *atespace)
		must(err)
		for _, a := range actors {
			if strings.HasPrefix(a.Name, "run-") {
				_, err := c.DeleteActor(ctx, a.Atespace, a.Name)
				must(err)
				fmt.Println("deleted", a.Name, "was", a.State)
			}
		}
		return
	}

	_, digest, _ := strings.Cut(*image, "@sha256:")
	base := fmt.Sprintf("http://%s:%d", *publicHost, *publicPort)
	tpl := substrate.Template{
		Atespace: *atespace, Name: "runner-" + digest[:12], Image: *image, Args: []string{"actor"},
		Env:        map[string]string{"ACTOR_TURN_URL": base, "ACTOR_HEALTH_ADDR": ":8089"},
		WakeupPath: "/healthz", WakeupPort: 8089, CPUMillicores: 500, MemoryBytes: 512 << 20,
	}
	goldenStart := time.Now()
	for {
		st, err := c.EnsureTemplate(ctx, tpl)
		must(err)
		if st.GoldenError != "" {
			must(fmt.Errorf("golden snapshot: %s", st.GoldenError))
		}
		if st.GoldenReady {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	goldenMs := ms(time.Since(goldenStart))

	modelURL := base + "/v1"
	if *modelBase != "" {
		modelURL = *modelBase
	}
	var runs []iteration
	total, firstRun := *n, ""
	if keys != nil && *impostor {
		total++
	}
	for i := 0; i < total; i++ {
		suffix := make([]byte, 6)
		_, _ = rand.Read(suffix)
		ref := substrate.ActorRef{Atespace: *atespace, Name: "run-" + hex.EncodeToString(suffix)}
		it := iteration{Actor: ref.String()}
		runModelURL := modelURL
		before := 0
		if keys != nil {
			// The run is named after its actor; the controller will do the same.
			it.Run = ref.Name
			must(keys.bind(ctx, it.Run, ref.String()))
			target := it.Run
			if firstRun == "" {
				firstRun = it.Run
			} else if *impostor && i == total-1 {
				target = firstRun
			}
			if *modelBase == "" {
				runModelURL = fmt.Sprintf("%s/runs/%s/%s/v1", base, keyPathNamespace, target)
			}
			it.ModelURL = runModelURL
			before, _ = keys.counts()
		}
		start := time.Now()

		_, _, err := c.CreateActor(ctx, ref.Atespace, ref.Name, tpl.Name)
		must(err)
		it.CreateMs = ms(time.Since(start))

		t := time.Now()
		must(c.SetEgress(ctx, ref.Atespace, ref.Name, []substrate.EgressTarget{{Hostname: *publicHost, Port: int32(*publicPort), ActorJWTAudience: *audience}}))
		it.EgressMs = ms(time.Since(t))

		done := store.Offer(ref, substrate.Turn{ID: "turn-" + hex.EncodeToString(suffix), Env: map[string]string{
			"TASK": "Say that the turn completed.", "AGENT_RUN_ID": ref.Name, "INSTANCE_NAME": "spike", "AGENT_NAMESPACE": "default",
			"MODEL_PROVIDER": "openai", "MODEL_NAME": "stub", "MODEL_BASE_URL": runModelURL,
			// Not a key: the placeholder the egress gateway replaces with the actor's JWT.
			"OPENAI_API_KEY": "substrate-actor", "LLM_MAX_RETRIES": "0",
		}})

		resumeStart := time.Now()
		a, err := c.ResumeActor(ctx, ref.Atespace, ref.Name)
		must(err)
		it.ResumeMs, it.Worker = ms(time.Since(resumeStart)), a.Worker

		select {
		case res := <-done:
			claimed := store.ClaimedAt(ref)
			it.ClaimMs, it.TurnMs, it.ExitCode = ms(claimed.Sub(resumeStart)), ms(time.Since(claimed)), res.ExitCode
			var body struct {
				Status   string `json:"status"`
				Response string `json:"response"`
				Error    string `json:"error"`
			}
			_ = json.Unmarshal(res.Result, &body)
			it.Response = strings.TrimSpace(body.Status + ": " + body.Response + body.Error)
			if keys != nil {
				it.KeySeenInResult = strings.Contains(string(res.Result), keys.realKey) || strings.Contains(res.Logs, keys.realKey)
			}
			if res.ExitCode != 0 || body.Status != "success" {
				fmt.Fprintf(os.Stderr, "--- runner logs for %s ---\n%s\n", ref, res.Logs)
			}
		case <-time.After(90 * time.Second):
			it.ExitCode, it.Response = -1, "no result within 90s"
		}

		if !*keep {
			t = time.Now()
			for {
				gone, err := c.DeleteActor(ctx, ref.Atespace, ref.Name)
				must(err)
				if gone {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			it.DeleteMs = ms(time.Since(t))
		}
		it.TotalMs = ms(time.Since(start))
		model.mu.Lock()
		it.ModelCalls = model.calls[ref.String()]
		if it.ModelCalls > 0 {
			it.ModelIdentity = "actor JWT for " + ref.String()
		}
		model.mu.Unlock()
		if keys != nil {
			after, _ := keys.counts()
			it.ProviderCalls = after - before
		}
		runs = append(runs, it)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	model.mu.Lock()
	defer model.mu.Unlock()
	out := map[string]any{"template": tpl.Name, "image": *image, "goldenSnapshotMs": goldenMs, "runs": runs, "modelRefusals": model.refused}
	if keys != nil {
		_, other := keys.counts()
		out["providerCallsWithoutAgentKey"] = other
	}
	must(enc.Encode(out))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "substrate-turn:", err)
		os.Exit(1)
	}
}
