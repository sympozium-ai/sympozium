package controller

import (
	"testing"
	"time"
)

func ptrFloat(f float64) *float64 { return &f }
func ptrStr(s string) *string     { return &s }

func TestScheduleModelPlacementGPUPreference(t *testing.T) {
	cache := NewFitnessCache(90 * time.Second)

	cache.Update(&NodeFitness{
		NodeName: "cpu-node",
		LastSeen: time.Now(),
		System: SystemSpecs{
			TotalRAMGb: 128, CPUCores: 32,
			HasGPU: false, Backend: "cpu",
		},
		ModelFits: []ModelFitInfo{
			{Name: "Qwen2.5-7B", Score: 50, FitLevel: "marginal", EstimatedTPS: 5},
		},
	})
	cache.Update(&NodeFitness{
		NodeName: "gpu-node",
		LastSeen: time.Now(),
		System: SystemSpecs{
			TotalRAMGb: 64, CPUCores: 16,
			HasGPU: true, GPUVRAMGb: ptrFloat(24), GPUName: ptrStr("RTX 4090"),
			GPUCount: 1, Backend: "cuda",
			GPUs: []GPUInfo{{Name: "RTX 4090", VRAMGb: 24, Backend: "cuda", Count: 1}},
		},
		ModelFits: []ModelFitInfo{
			{Name: "Qwen2.5-7B", Score: 85, FitLevel: "perfect", EstimatedTPS: 42},
		},
	})

	results := cache.ScheduleModelPlacement("Qwen2.5", SchedulePreference{PreferGPU: true})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].NodeName != "gpu-node" {
		t.Errorf("expected gpu-node first, got %s", results[0].NodeName)
	}
	if results[0].GPUName != "RTX 4090" {
		t.Errorf("expected GPU name RTX 4090, got %s", results[0].GPUName)
	}
}

func TestScheduleModelPlacementVRAMThreshold(t *testing.T) {
	cache := NewFitnessCache(90 * time.Second)

	cache.Update(&NodeFitness{
		NodeName: "small-gpu",
		LastSeen: time.Now(),
		System: SystemSpecs{
			HasGPU: true, GPUVRAMGb: ptrFloat(8), GPUCount: 1, Backend: "cuda",
			GPUs: []GPUInfo{{Name: "RTX 3070", VRAMGb: 8, Backend: "cuda", Count: 1}},
		},
		ModelFits: []ModelFitInfo{
			{Name: "Llama-3.1-70B", Score: 40, FitLevel: "marginal", EstimatedTPS: 3},
		},
	})
	cache.Update(&NodeFitness{
		NodeName: "big-gpu",
		LastSeen: time.Now(),
		System: SystemSpecs{
			HasGPU: true, GPUVRAMGb: ptrFloat(80), GPUCount: 1, Backend: "cuda",
			GPUs: []GPUInfo{{Name: "A100-80GB", VRAMGb: 80, Backend: "cuda", Count: 1}},
		},
		ModelFits: []ModelFitInfo{
			{Name: "Llama-3.1-70B", Score: 75, FitLevel: "good", EstimatedTPS: 25},
		},
	})

	results := cache.ScheduleModelPlacement("Llama-3.1-70B", SchedulePreference{
		PreferGPU: true,
		MinVRAMGb: 40,
	})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	// big-gpu should rank first — meets VRAM threshold.
	if results[0].NodeName != "big-gpu" {
		t.Errorf("expected big-gpu first, got %s", results[0].NodeName)
	}
}

func TestScheduleModelPlacementBackendPreference(t *testing.T) {
	cache := NewFitnessCache(90 * time.Second)

	cache.Update(&NodeFitness{
		NodeName: "cuda-node",
		LastSeen: time.Now(),
		System:   SystemSpecs{HasGPU: true, Backend: "cuda"},
		ModelFits: []ModelFitInfo{
			{Name: "Model-X", Score: 70, FitLevel: "good", EstimatedTPS: 30},
		},
	})
	cache.Update(&NodeFitness{
		NodeName: "metal-node",
		LastSeen: time.Now(),
		System:   SystemSpecs{HasGPU: true, Backend: "metal", UnifiedMemory: true},
		ModelFits: []ModelFitInfo{
			{Name: "Model-X", Score: 70, FitLevel: "good", EstimatedTPS: 30},
		},
	})

	// Prefer metal backend.
	results := cache.ScheduleModelPlacement("Model-X", SchedulePreference{
		PreferBackend: "metal",
	})

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].NodeName != "metal-node" {
		t.Errorf("expected metal-node first with metal preference, got %s", results[0].NodeName)
	}
}

func TestHasGPUNodes(t *testing.T) {
	cache := NewFitnessCache(90 * time.Second)

	cache.Update(&NodeFitness{
		NodeName: "cpu-only",
		LastSeen: time.Now(),
		System:   SystemSpecs{HasGPU: false},
	})

	if cache.HasGPUNodes() {
		t.Error("expected no GPU nodes")
	}

	cache.Update(&NodeFitness{
		NodeName: "gpu-node",
		LastSeen: time.Now(),
		System:   SystemSpecs{HasGPU: true},
	})

	if !cache.HasGPUNodes() {
		t.Error("expected GPU nodes")
	}
}

func TestGPUBackends(t *testing.T) {
	cache := NewFitnessCache(90 * time.Second)

	cache.Update(&NodeFitness{
		NodeName: "n1",
		LastSeen: time.Now(),
		System:   SystemSpecs{HasGPU: true, Backend: "cuda"},
	})
	cache.Update(&NodeFitness{
		NodeName: "n2",
		LastSeen: time.Now(),
		System:   SystemSpecs{HasGPU: true, Backend: "metal"},
	})
	cache.Update(&NodeFitness{
		NodeName: "n3",
		LastSeen: time.Now(),
		System:   SystemSpecs{HasGPU: false, Backend: "cpu"},
	})

	backends := cache.GPUBackends()
	if len(backends) != 2 {
		t.Fatalf("expected 2 GPU backends, got %d: %v", len(backends), backends)
	}
	// Sorted alphabetically.
	if backends[0] != "cuda" || backends[1] != "metal" {
		t.Errorf("expected [cuda metal], got %v", backends)
	}
}
