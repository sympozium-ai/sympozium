package controller

import (
	"testing"
	"time"
)

func TestFitnessCacheUpdateAndGet(t *testing.T) {
	cache := NewFitnessCache(90 * time.Second)

	nf := &NodeFitness{
		NodeName: "node-1",
		LastSeen: time.Now(),
		System: SystemSpecs{
			TotalRAMGb: 64,
			CPUCores:   16,
			HasGPU:     true,
		},
		ModelFits: []ModelFitInfo{
			{Name: "Qwen2.5-7B", Score: 85.0, FitLevel: "perfect"},
		},
	}

	cache.Update(nf)

	got, ok := cache.Get("node-1")
	if !ok {
		t.Fatal("expected node-1 to exist in cache")
	}
	if got.System.TotalRAMGb != 64 {
		t.Errorf("expected TotalRAMGb=64, got %f", got.System.TotalRAMGb)
	}
	if len(got.ModelFits) != 1 {
		t.Fatalf("expected 1 model fit, got %d", len(got.ModelFits))
	}
	if got.ModelFits[0].Name != "Qwen2.5-7B" {
		t.Errorf("expected model name Qwen2.5-7B, got %s", got.ModelFits[0].Name)
	}

	_, ok = cache.Get("node-2")
	if ok {
		t.Error("expected node-2 to not exist in cache")
	}
}

func TestFitnessCacheMerge(t *testing.T) {
	cache := NewFitnessCache(90 * time.Second)

	// First update: system specs.
	cache.Update(&NodeFitness{
		NodeName: "node-1",
		LastSeen: time.Now(),
		System:   SystemSpecs{TotalRAMGb: 64, CPUCores: 16},
	})

	// Second update: model fits only (system specs should be preserved).
	cache.Update(&NodeFitness{
		NodeName:  "node-1",
		LastSeen:  time.Now(),
		ModelFits: []ModelFitInfo{{Name: "Llama-3.1-8B", Score: 72.0, FitLevel: "good"}},
	})

	got, ok := cache.Get("node-1")
	if !ok {
		t.Fatal("expected node-1 to exist")
	}
	if got.System.TotalRAMGb != 64 {
		t.Error("system specs should have been preserved across merge")
	}
	if len(got.ModelFits) != 1 || got.ModelFits[0].Name != "Llama-3.1-8B" {
		t.Error("model fits should have been updated")
	}
}

func TestFitnessCacheStaleness(t *testing.T) {
	cache := NewFitnessCache(100 * time.Millisecond)

	cache.Update(&NodeFitness{
		NodeName: "node-1",
		LastSeen: time.Now(),
	})

	if cache.IsStale("node-1") {
		t.Error("node-1 should not be stale immediately after update")
	}

	time.Sleep(150 * time.Millisecond)

	if !cache.IsStale("node-1") {
		t.Error("node-1 should be stale after TTL")
	}

	if !cache.IsStale("nonexistent") {
		t.Error("nonexistent node should be stale")
	}
}

func TestFitnessCacheAll(t *testing.T) {
	cache := NewFitnessCache(100 * time.Millisecond)

	cache.Update(&NodeFitness{NodeName: "fresh", LastSeen: time.Now()})
	cache.Update(&NodeFitness{NodeName: "stale", LastSeen: time.Now().Add(-200 * time.Millisecond)})

	all := cache.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 non-stale node, got %d", len(all))
	}
	if all[0].NodeName != "fresh" {
		t.Errorf("expected fresh node, got %s", all[0].NodeName)
	}
}

func TestFitnessCacheGarbageCollect(t *testing.T) {
	cache := NewFitnessCache(50 * time.Millisecond)

	cache.Update(&NodeFitness{NodeName: "old", LastSeen: time.Now().Add(-200 * time.Millisecond)})
	cache.Update(&NodeFitness{NodeName: "recent", LastSeen: time.Now()})

	if cache.NodeCount() != 2 {
		t.Fatalf("expected 2 nodes before GC, got %d", cache.NodeCount())
	}

	cache.GarbageCollect()

	if cache.NodeCount() != 1 {
		t.Fatalf("expected 1 node after GC, got %d", cache.NodeCount())
	}
	_, ok := cache.Get("old")
	if ok {
		t.Error("old node should have been garbage collected")
	}
}

func TestBestNodeForModel(t *testing.T) {
	cache := NewFitnessCache(90 * time.Second)

	cache.Update(&NodeFitness{
		NodeName: "gpu-node-1",
		LastSeen: time.Now(),
		ModelFits: []ModelFitInfo{
			{Name: "Qwen2.5-7B-Instruct", Score: 85.0, FitLevel: "perfect", EstimatedTPS: 42.0},
			{Name: "Llama-3.1-70B", Score: 30.0, FitLevel: "marginal", EstimatedTPS: 5.0},
		},
	})
	cache.Update(&NodeFitness{
		NodeName: "gpu-node-2",
		LastSeen: time.Now(),
		ModelFits: []ModelFitInfo{
			{Name: "Qwen2.5-7B-Instruct", Score: 72.0, FitLevel: "good", EstimatedTPS: 35.0},
			{Name: "Llama-3.1-70B", Score: 65.0, FitLevel: "good", EstimatedTPS: 12.0},
		},
	})

	// Best node for Qwen2.5 should be gpu-node-1 (score 85 > 72).
	node, score, _ := cache.BestNodeForModel("Qwen2.5", "good")
	if node != "gpu-node-1" {
		t.Errorf("expected gpu-node-1, got %s", node)
	}
	if score != 85.0 {
		t.Errorf("expected score 85, got %f", score)
	}

	// Best node for Llama-3.1-70B with min_fit=good should be gpu-node-2
	// (gpu-node-1 is marginal which doesn't meet "good").
	node, score, _ = cache.BestNodeForModel("Llama-3.1-70B", "good")
	if node != "gpu-node-2" {
		t.Errorf("expected gpu-node-2, got %s", node)
	}
	if score != 65.0 {
		t.Errorf("expected score 65, got %f", score)
	}

	// No match for a model that doesn't exist.
	node, score, _ = cache.BestNodeForModel("NonExistentModel", "good")
	if node != "" {
		t.Errorf("expected empty node, got %s", node)
	}
	if score != 0 {
		t.Errorf("expected score 0, got %f", score)
	}
}

func TestBestNodeForModelSkipsStale(t *testing.T) {
	cache := NewFitnessCache(50 * time.Millisecond)

	cache.Update(&NodeFitness{
		NodeName: "stale-node",
		LastSeen: time.Now().Add(-100 * time.Millisecond),
		ModelFits: []ModelFitInfo{
			{Name: "Qwen2.5-7B", Score: 99.0, FitLevel: "perfect"},
		},
	})

	node, _, _ := cache.BestNodeForModel("Qwen2.5", "good")
	if node != "" {
		t.Error("should not return a stale node")
	}
}

func TestFitMeetsMinimum(t *testing.T) {
	tests := []struct {
		actual, minimum string
		want            bool
	}{
		{"perfect", "perfect", true},
		{"perfect", "good", true},
		{"perfect", "marginal", true},
		{"good", "perfect", false},
		{"good", "good", true},
		{"good", "marginal", true},
		{"marginal", "good", false},
		{"marginal", "marginal", true},
		{"too_tight", "marginal", false},
	}

	for _, tt := range tests {
		got := fitMeetsMinimum(tt.actual, tt.minimum)
		if got != tt.want {
			t.Errorf("fitMeetsMinimum(%q, %q) = %v, want %v",
				tt.actual, tt.minimum, got, tt.want)
		}
	}
}
