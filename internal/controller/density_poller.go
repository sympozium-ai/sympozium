package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DensityPoller polls llmfit REST APIs on DaemonSet pods to populate the
// DensityCache. This is the fallback when the llmfit binary doesn't have
// the NATS feature compiled in. Implements manager.Runnable.
type DensityPoller struct {
	K8sClient    client.Client
	Cache        *DensityCache
	Log          logr.Logger
	PollInterval time.Duration // default 60s
	Namespace    string        // sympozium-system
	Port         int           // default 8787
}

// Start polls llmfit DaemonSet pods and populates the cache until ctx is cancelled.
func (fp *DensityPoller) Start(ctx context.Context) error {
	if fp.PollInterval == 0 {
		fp.PollInterval = 60 * time.Second
	}
	if fp.Port == 0 {
		fp.Port = 8787
	}
	if fp.Namespace == "" {
		fp.Namespace = "sympozium-system"
	}

	fp.Log.Info("Starting density poller",
		"interval", fp.PollInterval,
		"namespace", fp.Namespace,
	)

	// Initial poll after a short delay to let pods start.
	time.Sleep(5 * time.Second)
	fp.poll(ctx)

	ticker := time.NewTicker(fp.PollInterval)
	defer ticker.Stop()

	gcTicker := time.NewTicker(5 * time.Minute)
	defer gcTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			fp.Log.Info("Stopping density poller")
			return nil
		case <-ticker.C:
			fp.poll(ctx)
		case <-gcTicker.C:
			fp.Cache.GarbageCollect()
		}
	}
}

// poll discovers llmfit DaemonSet pods and queries their REST APIs.
func (fp *DensityPoller) poll(ctx context.Context) {
	var pods corev1.PodList
	if err := fp.K8sClient.List(ctx, &pods,
		client.InNamespace(fp.Namespace),
		client.MatchingLabels{"app.kubernetes.io/component": "llmfit-daemon"},
	); err != nil {
		fp.Log.V(1).Info("Failed to list llmfit pods", "error", err)
		return
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
			continue
		}

		nodeName := pod.Spec.NodeName
		podIP := pod.Status.PodIP

		go fp.pollPod(nodeName, podIP)
	}
}

// pollPod queries a single llmfit pod's REST API and updates the cache.
func (fp *DensityPoller) pollPod(nodeName, podIP string) {
	baseURL := fmt.Sprintf("http://%s:%d", podIP, fp.Port)
	httpClient := &http.Client{Timeout: 10 * time.Second}

	now := time.Now()
	nf := &NodeDensity{
		NodeName: nodeName,
		LastSeen: now,
	}

	// Fetch system specs (response: {"node":{...}, "system":{...}}).
	if data, err := httpGet(httpClient, baseURL+"/api/v1/system"); err == nil {
		var resp struct {
			System SystemSpecs `json:"system"`
		}
		if json.Unmarshal(data, &resp) == nil {
			nf.System = resp.System
		}
	}

	// Fetch top model fits (response: {"total_models":N, "returned_models":N, "models":[...]}).
	// Include all fit levels so the catalog shows the full picture.
	if data, err := httpGet(httpClient, baseURL+"/api/v1/models?limit=100&sort=score"); err == nil {
		var resp struct {
			Models []ModelFitInfo `json:"models"`
		}
		if json.Unmarshal(data, &resp) == nil {
			nf.ModelFits = resp.Models
		}
	}

	fp.Cache.Update(nf)
}

// httpGet performs a GET request and returns the response body.
func httpGet(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
