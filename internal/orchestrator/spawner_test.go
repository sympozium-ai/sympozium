package orchestrator

import (
	"fmt"
	"testing"
)

func TestSpawnRunName_SingleChild(t *testing.T) {
	// ChildIndex=0 means legacy single-child naming.
	req := SpawnRequest{
		ParentRunName: "parent-run-abc",
		CurrentDepth:  0,
		ChildIndex:    0,
	}

	runName := fmt.Sprintf("sub-%s-%d", req.ParentRunName, req.CurrentDepth+1)
	if req.ChildIndex > 0 {
		runName = fmt.Sprintf("sub-%s-%d-%d", req.ParentRunName, req.CurrentDepth+1, req.ChildIndex)
	}

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
		t.Run(fmt.Sprintf("child_%d", tt.childIndex), func(t *testing.T) {
			req := SpawnRequest{
				ParentRunName: "parent",
				CurrentDepth:  0,
				ChildIndex:    tt.childIndex,
			}

			runName := fmt.Sprintf("sub-%s-%d", req.ParentRunName, req.CurrentDepth+1)
			if req.ChildIndex > 0 {
				runName = fmt.Sprintf("sub-%s-%d-%d", req.ParentRunName, req.CurrentDepth+1, req.ChildIndex)
			}

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

	runName := fmt.Sprintf("sub-%s-%d", req.ParentRunName, req.CurrentDepth+1)
	if req.ChildIndex > 0 {
		runName = fmt.Sprintf("sub-%s-%d-%d", req.ParentRunName, req.CurrentDepth+1, req.ChildIndex)
	}

	want := "sub-sub-root-1-2-2-3"
	if runName != want {
		t.Errorf("runName = %q, want %q", runName, want)
	}
}
