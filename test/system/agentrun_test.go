package system_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestRunCreateDelete(t *testing.T) {
	ns := createTestNamespace(t)
	agentName := "sys-run-agent"

	// Create agent via API (lm-studio provider with baseURL = no auth secret needed).
	agentBody := map[string]any{
		"name":     agentName,
		"provider": "lm-studio",
		"model":    "test-model",
		"baseURL":  "http://fake-lmstudio:1234/v1",
	}
	rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/agents?namespace=%s", ns), agentBody)
	requireStatus(t, rec, http.StatusCreated)

	t.Cleanup(func() {
		httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", agentName, ns), nil)
	})

	// Dispatch a run and extract name from response.
	type runResp struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	rr, code := httpJSON[runResp](t, http.MethodPost, fmt.Sprintf("/api/v1/runs?namespace=%s", ns), map[string]any{
		"agentRef": agentName,
		"task":     "Say hello",
	})
	if code != http.StatusCreated {
		t.Fatalf("create run status = %d", code)
	}
	runName := rr.Metadata.Name
	if runName == "" {
		t.Fatal("run name is empty in response")
	}

	// Wait for the AgentRun controller to create a Job.
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var jobs batchv1.JobList
		if err := k8sClient.List(testCtx, &jobs, client.InNamespace(ns)); err != nil {
			return false
		}
		for _, j := range jobs.Items {
			if j.Labels["sympozium.ai/agent-run"] == runName {
				return true
			}
		}
		return false
	})

	// Delete the run via API.
	rec = httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/runs/%s?namespace=%s", runName, ns), nil)
	if rec.Code != 200 && rec.Code != 204 {
		t.Fatalf("delete run status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Verify AgentRun is gone.
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var run sympoziumv1alpha1.AgentRun
		err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: runName}, &run)
		return err != nil
	})
}

func TestRunRequiresAgent(t *testing.T) {
	ns := createTestNamespace(t)

	body := map[string]any{
		"agentRef": "nonexistent-agent",
		"task":     "Should fail",
	}
	rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/runs?namespace=%s", ns), body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestRunJobShape(t *testing.T) {
	ns := createTestNamespace(t)
	agentName := "sys-run-shape"

	agentBody := map[string]any{
		"name":     agentName,
		"provider": "lm-studio",
		"model":    "qwen/qwen3.5-9b",
		"baseURL":  "http://fake-lmstudio:1234/v1",
	}
	rec := httpDo(t, http.MethodPost, fmt.Sprintf("/api/v1/agents?namespace=%s", ns), agentBody)
	requireStatus(t, rec, http.StatusCreated)
	t.Cleanup(func() {
		httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/agents/%s?namespace=%s", agentName, ns), nil)
	})

	// Create a run.
	runBody := map[string]any{
		"agentRef": agentName,
		"task":     "Verify job shape",
	}
	type runResp struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	rr, code := httpJSON[runResp](t, http.MethodPost, fmt.Sprintf("/api/v1/runs?namespace=%s", ns), runBody)
	if code != http.StatusCreated {
		t.Fatalf("create run status = %d", code)
	}
	runName := rr.Metadata.Name

	t.Cleanup(func() {
		httpDo(t, http.MethodDelete, fmt.Sprintf("/api/v1/runs/%s?namespace=%s", runName, ns), nil)
	})

	// Wait for Job and verify its shape.
	var foundJob batchv1.Job
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var jobs batchv1.JobList
		if err := k8sClient.List(testCtx, &jobs, client.InNamespace(ns)); err != nil {
			return false
		}
		for _, j := range jobs.Items {
			if j.Labels["sympozium.ai/agent-run"] == runName {
				foundJob = j
				return true
			}
		}
		return false
	})

	// Verify the Job has at least one container.
	containers := foundJob.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		t.Fatal("job has no containers")
	}

	// Verify the Job labels reference the correct agent.
	if foundJob.Labels["sympozium.ai/instance"] != agentName {
		t.Errorf("job instance label = %q, want %q", foundJob.Labels["sympozium.ai/instance"], agentName)
	}

	// Verify the input ConfigMap was created with the task text.
	var inputCM corev1.ConfigMap
	assertExists(t, &inputCM, ns, fmt.Sprintf("%s-input", runName))
	if inputCM.Data["task"] != "Verify job shape" {
		t.Errorf("input ConfigMap task = %q, want %q", inputCM.Data["task"], "Verify job shape")
	}
}
