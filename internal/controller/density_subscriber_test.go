package controller

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	logrtesting "sigs.k8s.io/controller-runtime/pkg/log"
)

func TestHandleMessageSystem(t *testing.T) {
	cache := NewDensityCache(90 * time.Second)
	fs := &DensitySubscriber{
		Cache: cache,
		Log:   logrtesting.Log,
	}

	payload := map[string]interface{}{
		"timestamp":  "1715500000",
		"hostname":   "gpu-node-1",
		"event_type": "system",
		"version":    "1",
		"data": map[string]interface{}{
			"total_ram_gb":     64.0,
			"available_ram_gb": 48.5,
			"cpu_cores":        16,
			"cpu_name":         "AMD EPYC 7763",
			"has_gpu":          true,
			"gpu_vram_gb":      24.0,
			"gpu_name":         "NVIDIA RTX 4090",
			"gpu_count":        1,
			"unified_memory":   false,
			"backend":          "cuda",
			"gpus":             []interface{}{},
		},
	}
	data, _ := json.Marshal(payload)

	fs.handleMessage(&nats.Msg{
		Subject: "llmfit.system.gpu-node-1",
		Data:    data,
	})

	nf, ok := cache.Get("gpu-node-1")
	if !ok {
		t.Fatal("expected gpu-node-1 in cache after system event")
	}
	if nf.System.TotalRAMGb != 64.0 {
		t.Errorf("expected TotalRAMGb=64, got %f", nf.System.TotalRAMGb)
	}
	if nf.System.CPUCores != 16 {
		t.Errorf("expected CPUCores=16, got %d", nf.System.CPUCores)
	}
	if !nf.System.HasGPU {
		t.Error("expected HasGPU=true")
	}
}

func TestHandleMessageFit(t *testing.T) {
	cache := NewDensityCache(90 * time.Second)
	fs := &DensitySubscriber{
		Cache: cache,
		Log:   logrtesting.Log,
	}

	payload := map[string]interface{}{
		"timestamp":  "1715500000",
		"hostname":   "gpu-node-1",
		"event_type": "fit",
		"version":    "1",
		"data": map[string]interface{}{
			"total_models":    2,
			"returned_models": 2,
			"models": []map[string]interface{}{
				{
					"name":                "Qwen2.5-7B-Instruct",
					"score":               85.0,
					"fit_level":           "perfect",
					"runtime":             "llamacpp",
					"best_quant":          "Q4_K_M",
					"estimated_tps":       42.0,
					"memory_required_gb":  5.2,
					"memory_available_gb": 24.0,
					"utilization_pct":     21.7,
					"category":            "general",
				},
				{
					"name":                "Llama-3.1-8B",
					"score":               72.0,
					"fit_level":           "good",
					"runtime":             "vllm",
					"best_quant":          "Q8_0",
					"estimated_tps":       35.0,
					"memory_required_gb":  8.5,
					"memory_available_gb": 24.0,
					"utilization_pct":     35.4,
					"category":            "general",
				},
			},
		},
	}
	data, _ := json.Marshal(payload)

	fs.handleMessage(&nats.Msg{
		Subject: "llmfit.fit.gpu-node-1",
		Data:    data,
	})

	nf, ok := cache.Get("gpu-node-1")
	if !ok {
		t.Fatal("expected gpu-node-1 in cache after fit event")
	}
	if len(nf.ModelFits) != 2 {
		t.Fatalf("expected 2 model fits, got %d", len(nf.ModelFits))
	}
	if nf.ModelFits[0].Name != "Qwen2.5-7B-Instruct" {
		t.Errorf("expected first model Qwen2.5-7B-Instruct, got %s", nf.ModelFits[0].Name)
	}
	if nf.ModelFits[0].Score != 85.0 {
		t.Errorf("expected score 85, got %f", nf.ModelFits[0].Score)
	}
}

func TestHandleMessageRuntimes(t *testing.T) {
	cache := NewDensityCache(90 * time.Second)
	fs := &DensitySubscriber{
		Cache: cache,
		Log:   logrtesting.Log,
	}

	payload := map[string]interface{}{
		"timestamp":  "1715500000",
		"hostname":   "node-1",
		"event_type": "runtimes",
		"version":    "1",
		"data": map[string]interface{}{
			"runtimes": []map[string]interface{}{
				{"name": "ollama", "installed": true},
				{"name": "vllm", "installed": false},
			},
		},
	}
	data, _ := json.Marshal(payload)

	fs.handleMessage(&nats.Msg{
		Subject: "llmfit.runtimes.node-1",
		Data:    data,
	})

	nf, ok := cache.Get("node-1")
	if !ok {
		t.Fatal("expected node-1 in cache")
	}
	if len(nf.Runtimes) != 2 {
		t.Fatalf("expected 2 runtimes, got %d", len(nf.Runtimes))
	}
	if !nf.Runtimes[0].Installed {
		t.Error("expected ollama to be installed")
	}
}

func TestHandleMessageInstalled(t *testing.T) {
	cache := NewDensityCache(90 * time.Second)
	fs := &DensitySubscriber{
		Cache: cache,
		Log:   logrtesting.Log,
	}

	payload := map[string]interface{}{
		"timestamp":  "1715500000",
		"hostname":   "node-1",
		"event_type": "installed",
		"version":    "1",
		"data": map[string]interface{}{
			"models": []map[string]interface{}{
				{"name": "qwen2.5:7b", "runtime": "ollama"},
			},
		},
	}
	data, _ := json.Marshal(payload)

	fs.handleMessage(&nats.Msg{
		Subject: "llmfit.installed.node-1",
		Data:    data,
	})

	nf, ok := cache.Get("node-1")
	if !ok {
		t.Fatal("expected node-1 in cache")
	}
	if len(nf.InstalledModels) != 1 {
		t.Fatalf("expected 1 installed model, got %d", len(nf.InstalledModels))
	}
	if nf.InstalledModels[0].Name != "qwen2.5:7b" {
		t.Errorf("expected qwen2.5:7b, got %s", nf.InstalledModels[0].Name)
	}
}

func TestHandleMessageUnknownType(t *testing.T) {
	cache := NewDensityCache(90 * time.Second)
	fs := &DensitySubscriber{
		Cache: cache,
		Log:   logrtesting.Log,
	}

	payload := map[string]interface{}{
		"timestamp":  "1715500000",
		"hostname":   "node-1",
		"event_type": "future_event",
		"version":    "2",
		"data":       map[string]interface{}{"foo": "bar"},
	}
	data, _ := json.Marshal(payload)

	// Should not panic or error — just ignore.
	fs.handleMessage(&nats.Msg{
		Subject: "llmfit.future_event.node-1",
		Data:    data,
	})

	if cache.NodeCount() != 0 {
		t.Error("unknown event type should not populate cache")
	}
}

func TestHandleMessageHostnameFromSubject(t *testing.T) {
	cache := NewDensityCache(90 * time.Second)
	fs := &DensitySubscriber{
		Cache: cache,
		Log:   logrtesting.Log,
	}

	// Envelope with empty hostname — should fall back to subject parsing.
	payload := map[string]interface{}{
		"timestamp":  "1715500000",
		"hostname":   "",
		"event_type": "system",
		"version":    "1",
		"data": map[string]interface{}{
			"total_ram_gb": 32.0,
			"cpu_cores":    8,
		},
	}
	data, _ := json.Marshal(payload)

	fs.handleMessage(&nats.Msg{
		Subject: "llmfit.system.fallback-node",
		Data:    data,
	})

	_, ok := cache.Get("fallback-node")
	if !ok {
		t.Fatal("expected fallback-node extracted from subject")
	}
}
