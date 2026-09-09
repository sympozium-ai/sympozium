package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/sympozium-ai/sympozium/internal/apiserver"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Serve the built application with real API handlers, on loopback only. No
// intercepted responses, default kubeconfig discovery or external dev server.
func browserServer(t *testing.T, ctx context.Context, c client.Client, namespace string) string {
	t.Helper()
	if image := os.Getenv("CELLN_LIVE_APISERVER_IMAGE"); image != "" {
		return deployedBrowserServer(t, ctx, c, namespace, image)
	}
	web := os.Getenv("CELLN_LIVE_WEB_ROOT")
	if !filepath.IsAbs(web) {
		t.Fatal("absolute CELLN_LIVE_WEB_ROOT required")
	}
	index, err := os.ReadFile(filepath.Join(web, "dist", "index.html"))
	must(t, err)
	server := apiserver.NewServer(c, nil, nil, logr.Discard())
	ref := func(name string) types.NamespacedName { return types.NamespacedName{Namespace: namespace, Name: name} }
	must(t, server.ConfigureCellnPreview(c, []apiserver.CellnPreviewBinding{{Agent: ref("agent"), OperatorSource: ref("operator"), RuntimeSource: ref("runtime"), AgentSource: ref("agent")}}))
	api := server.Handler(nil)
	assets := http.FileServer(http.Dir(filepath.Join(web, "dist")))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			api.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/assets/"):
			assets.ServeHTTP(w, r)
		case r.URL.Path == "/ws":
			http.Error(w, "live fixture uses API polling, not NATS streaming", http.StatusServiceUnavailable)
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(index)
		}
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func runBrowser(t *testing.T, ctx context.Context, endpoint, namespace, run, task string) {
	runBrowserAction(t, ctx, endpoint, namespace, run, task, "")
}

func runBrowserAction(t *testing.T, ctx context.Context, endpoint, namespace, run, task, action string) {
	t.Helper()
	web := os.Getenv("CELLN_LIVE_WEB_ROOT")
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CYPRESS_") || strings.HasPrefix(key, "CELLN_") || key == "KUBECONFIG" || strings.Contains(key, "API_KEY") || key == "ELECTRON_RUN_AS_NODE" {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "CYPRESS_BASE_URL="+endpoint, "CYPRESS_PROOF_NAMESPACE="+namespace, "CYPRESS_PROOF_RUN="+run, "CYPRESS_PROOF_TASK="+task, "CYPRESS_PROOF_ACTION="+action)
	env = append(env, "CYPRESS_PROOF_TOKEN="+liveBrowserToken(t), "CYPRESS_PROOF_INTERACTIVE="+os.Getenv("CELLN_LIVE_INTERACTIVE"))
	out := command(t, ctx, env, filepath.Join(web, "node_modules", ".bin", "cypress"), "run", "--project", web, "--config-file", "cypress.celln-live.config.ts", "--browser", "electron")
	t.Logf("browser proof: %s", out)
}
