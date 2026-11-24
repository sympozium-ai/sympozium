// Command substrate-smoke drives one actor through the lifecycle the Substrate
// backend needs (spike 0b, #699) and prints how long each step took as JSON.
// It is the live half of the internal/substrate client's tests and the seed of
// the latency measurements; it is not shipped.
//
//	kubectl -n ate-system port-forward svc/api 8443:443 &
//	kubectl -n ate-system create token ate-client --audience api.ate-system.svc > /tmp/t
//	go run ./hack/substrate-smoke -endpoint 127.0.0.1:8443 -token-file /tmp/t -ca-file ca.pem \
//	    -image docker.io/hashicorp/http-echo@sha256:... -pool-label ate.dev/worker-pool=spike
package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sympozium-ai/sympozium/internal/substrate"
)

type step struct {
	Step   string  `json:"step"`
	Millis float64 `json:"ms"`
	State  string  `json:"state,omitempty"`
	Worker string  `json:"worker,omitempty"`
	Note   string  `json:"note,omitempty"`
}

func main() {
	endpoint := flag.String("endpoint", "127.0.0.1:8443", "ate-api-server host:port")
	serverName := flag.String("server-name", "api.ate-system.svc", "name in the serving certificate")
	tokenFile := flag.String("token-file", "", "bearer token file")
	caFile := flag.String("ca-file", "", "PEM bundle that verifies the serving certificate")
	atespace := flag.String("atespace", "symp-smoke", "atespace to use")
	image := flag.String("image", "", "digest-pinned image to run")
	args := flag.String("args", "", "comma-separated container args")
	port := flag.Int("port", 5678, "wakeup probe port")
	path := flag.String("path", "/", "wakeup probe path")
	selector := flag.String("worker-selector", "", "k=v worker selector")
	keep := flag.Bool("keep", false, "leave the actor in place")
	flag.Parse()

	pem, err := os.ReadFile(*caFile)
	must(err)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		must(fmt.Errorf("no certificates in %s", *caFile))
	}
	c, err := substrate.Dial(substrate.Config{
		Endpoint: *endpoint, RootCAs: pool, ServerName: *serverName,
		Token: func(context.Context) (string, error) {
			b, err := os.ReadFile(*tokenFile)
			return string(b), err
		},
	})
	must(err)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var steps []step
	timed := func(name string, fn func() (substrate.Actor, string, error)) {
		start := time.Now()
		a, note, err := fn()
		must(err)
		steps = append(steps, step{Step: name, Millis: float64(time.Since(start).Microseconds()) / 1000, State: string(a.State), Worker: a.Worker, Note: note})
	}

	tpl := substrate.Template{
		Atespace: *atespace, Name: "smoke-" + digestSuffix(*image), Image: *image,
		WakeupPath: *path, WakeupPort: int32(*port), CPUMillicores: 500, MemoryBytes: 256 << 20,
	}
	if *args != "" {
		tpl.Args = strings.Split(*args, ",")
	}
	if k, v, ok := strings.Cut(*selector, "="); ok {
		tpl.WorkerSelector = map[string]string{k: v}
	}
	actor := fmt.Sprintf("smoke-%d", time.Now().Unix())

	timed("ensure-atespace", func() (substrate.Actor, string, error) {
		return substrate.Actor{}, "", c.EnsureAtespace(ctx, *atespace)
	})
	timed("template-golden-ready", func() (substrate.Actor, string, error) {
		for {
			st, err := c.EnsureTemplate(ctx, tpl)
			if err != nil {
				return substrate.Actor{}, "", err
			}
			if st.GoldenError != "" {
				return substrate.Actor{}, "", fmt.Errorf("golden snapshot: %s", st.GoldenError)
			}
			if st.GoldenReady {
				return substrate.Actor{}, tpl.Name, nil
			}
			select {
			case <-ctx.Done():
				return substrate.Actor{}, "", ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	})
	timed("create-actor", func() (substrate.Actor, string, error) {
		a, created, err := c.CreateActor(ctx, *atespace, actor, tpl.Name)
		return a, fmt.Sprintf("created=%v", created), err
	})
	timed("create-actor-replay", func() (substrate.Actor, string, error) {
		a, created, err := c.CreateActor(ctx, *atespace, actor, tpl.Name)
		if err == nil && created {
			err = fmt.Errorf("replayed create made a second actor")
		}
		return a, fmt.Sprintf("created=%v", created), err
	})
	timed("resume-from-golden", func() (substrate.Actor, string, error) {
		return running(ctx, c, *atespace, actor)
	})
	timed("suspend-sync", func() (substrate.Actor, string, error) {
		a, err := c.SuspendActor(ctx, *atespace, actor, true)
		return a, "", err
	})
	timed("resume-from-own-snapshot", func() (substrate.Actor, string, error) {
		return running(ctx, c, *atespace, actor)
	})
	if !*keep {
		timed("delete-confirmed", func() (substrate.Actor, string, error) {
			for {
				gone, err := c.DeleteActor(ctx, *atespace, actor)
				if err != nil || gone {
					return substrate.Actor{}, fmt.Sprintf("gone=%v", gone), err
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	must(enc.Encode(map[string]any{"actor": actor, "atespace": *atespace, "image": *image, "steps": steps}))
}

// running resumes the actor and waits until Substrate reports it running.
func running(ctx context.Context, c *substrate.Client, atespace, name string) (substrate.Actor, string, error) {
	a, err := c.ResumeActor(ctx, atespace, name)
	for err == nil && a.State != substrate.StateRunning {
		if a.State == substrate.StateCrashed {
			return a, "", fmt.Errorf("actor crashed: %s", a.CrashMessage)
		}
		time.Sleep(20 * time.Millisecond)
		a, err = c.GetActor(ctx, atespace, name)
	}
	return a, "", err
}

func digestSuffix(image string) string {
	_, d, _ := strings.Cut(image, "@sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "substrate-smoke:", err)
		os.Exit(1)
	}
}
