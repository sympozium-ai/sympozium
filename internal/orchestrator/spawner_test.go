package orchestrator

import "testing"

func TestSpawnRunName_SingleChild(t *testing.T) {
	// ChildIndex=0 means legacy single-child naming.
	req := SpawnRequest{
		ParentRunName: "parent-run-abc",
		CurrentDepth:  0,
		ChildIndex:    0,
	}

	runName := buildSubagentRunName(req.ParentRunName, req.CurrentDepth+1, req.ChildIndex, req.BatchID)

	want := "sub-parent-run-abc-1"
	if runName != want {
		t.Errorf("runName = %q, want %q", runName, want)
	}
}

func TestSpawnRunName_BatchChildren(t *testing.T) {
	tests := []struct {
		childIndex int
		want       string
	}{
		{1, "sub-parent-1-1"},
		{2, "sub-parent-1-2"},
		{3, "sub-parent-1-3"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			req := SpawnRequest{
				ParentRunName: "parent",
				CurrentDepth:  0,
				ChildIndex:    tt.childIndex,
			}

			runName := buildSubagentRunName(req.ParentRunName, req.CurrentDepth+1, req.ChildIndex, req.BatchID)

			if runName != tt.want {
				t.Errorf("runName = %q, want %q", runName, tt.want)
			}
		})
	}
}

func TestSpawnRunName_NestedDepth(t *testing.T) {
	req := SpawnRequest{
		ParentRunName: "sub-root-1-2",
		CurrentDepth:  1,
		ChildIndex:    3,
	}

	runName := buildSubagentRunName(req.ParentRunName, req.CurrentDepth+1, req.ChildIndex, req.BatchID)

	want := "sub-sub-root-1-2-2-3"
	if runName != want {
		t.Errorf("runName = %q, want %q", runName, want)
	}
}

func TestSpawnRunName_BatchIDDisambiguatesRepeatedBatches(t *testing.T) {
	first := buildSubagentRunName("parent", 1, 1, "batch-a")
	second := buildSubagentRunName("parent", 1, 1, "batch-b")

	if first == second {
		t.Fatalf("expected distinct child names for distinct batches, got %q", first)
	}
	if first != "sub-parent-batch-a-1-1" {
		t.Errorf("first batch runName = %q, want %q", first, "sub-parent-batch-a-1-1")
	}
	if second != "sub-parent-batch-b-1-1" {
		t.Errorf("second batch runName = %q, want %q", second, "sub-parent-batch-b-1-1")
	}
}

func TestSpawnRunName_LongBatchIDIsShortened(t *testing.T) {
	runName := buildSubagentRunName("parent", 1, 1, "1782460863531807897-extra-long-batch-id")
	if len(runName) > 253 {
		t.Fatalf("runName length = %d, want <= 253", len(runName))
	}
	if runName == "sub-parent-1782460863531807897-extra-long-batch-id-1-1" {
		t.Fatal("expected long batch id to be shortened")
	}
}
