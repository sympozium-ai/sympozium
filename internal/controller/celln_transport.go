package controller

import (
	"context"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/sympozium-ai/sympozium/internal/celln"
)

// cellnClientFromEnv builds the single Celln client from controller
// configuration. Credentials belong to the deployment, never AgentRun task
// text. The credential file is re-read on every request so a mounted Secret
// rotation takes effect.
func cellnClientFromEnv() (*celln.Client, error) {
	return celln.New(celln.Config{
		BaseURL:       cellnRouterURL(),
		TokenFile:     os.Getenv("CELLN_TOKEN_FILE"),
		AllowInsecure: os.Getenv("CELLN_ALLOW_INSECURE_HTTP") == "true",
		Timeout:       30 * time.Second,
	})
}

// cellnRequest builds a credentialed request against the configured origin.
func cellnRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	c, err := cellnClientFromEnv()
	if err != nil {
		return nil, err
	}
	return c.Request(ctx, method, path, body)
}

// cellnHTTPClient sends already-built requests. The transport policy (no
// redirects, bounded timeout) is owned by the shared celln client.
var cellnHTTPClient = celln.DefaultHTTPClient
