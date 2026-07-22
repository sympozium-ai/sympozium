package controller

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// TestNextScheduledRunNumber_NoExistingRuns: clean slate → returns
// TotalRuns+1 as expected.
func TestNextScheduledRunNumber_NoExistingRuns(t *testing.T) {
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "sched", Namespace: "default"},
		Status:     sympoziumv1alpha1.SympoziumScheduleStatus{TotalRuns: 2},
	}
	r, _ := newScheduleTestReconciler(t, schedule)

	n, err := r.nextScheduledRunNumber(context.Background(), schedule)
	if err != nil {
		t.Fatalf("nextScheduledRunNumber: %v", err)
	}
	if n != 3 {
		t.Errorf("got %d, want 3 (TotalRuns+1)", n)
	}
}

// TestNextScheduledRunNumber_RecoversFromOrphanedRuns is the regression
// guard: simulates a schedule that was deleted and recreated, so
// TotalRuns reset to 0 but orphan AgentRuns from the previous
// incarnation still exist with suffixes 1..9. The scheduler must pick
// 10, not 1.
func TestNextScheduledRunNumber_RecoversFromOrphanedRuns(t *testing.T) {
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "sched", Namespace: "default"},
		Status:     sympoziumv1alpha1.SympoziumScheduleStatus{TotalRuns: 0},
	}
	objs := []client.Object{schedule}
	for i := 1; i <= 9; i++ {
		objs = append(objs, makeOrphanRun("sched", i))
	}
	r, _ := newScheduleTestReconciler(t, objs...)

	n, err := r.nextScheduledRunNumber(context.Background(), schedule)
	if err != nil {
		t.Fatalf("nextScheduledRunNumber: %v", err)
	}
	if n != 10 {
		t.Errorf("got %d, want 10 (must skip orphans 1..9)", n)
	}
}

// TestNextScheduledRunNumber_HonoursHigherTotalRuns: when TotalRuns is
// ahead of observed orphans, use TotalRuns+1.
func TestNextScheduledRunNumber_HonoursHigherTotalRuns(t *testing.T) {
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "sched", Namespace: "default"},
		Status:     sympoziumv1alpha1.SympoziumScheduleStatus{TotalRuns: 50},
	}
	r, _ := newScheduleTestReconciler(t, schedule, makeOrphanRun("sched", 3))

	n, err := r.nextScheduledRunNumber(context.Background(), schedule)
	if err != nil {
		t.Fatalf("nextScheduledRunNumber: %v", err)
	}
	if n != 51 {
		t.Errorf("got %d, want 51", n)
	}
}

// TestNextScheduledRunNumber_IgnoresOtherSchedulesRuns: only runs whose
// label matches THIS schedule count. Runs belonging to other schedules
// are ignored.
func TestNextScheduledRunNumber_IgnoresOtherSchedulesRuns(t *testing.T) {
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "sched", Namespace: "default"},
		Status:     sympoziumv1alpha1.SympoziumScheduleStatus{TotalRuns: 0},
	}
	own := makeOrphanRun("sched", 7)
	other := makeOrphanRun("other-sched", 99)
	r, _ := newScheduleTestReconciler(t, schedule, own, other)

	n, err := r.nextScheduledRunNumber(context.Background(), schedule)
	if err != nil {
		t.Fatalf("nextScheduledRunNumber: %v", err)
	}
	if n != 8 {
		t.Errorf("got %d, want 8 (should ignore other-sched's run)", n)
	}
}

// TestNextScheduledRunNumber_IgnoresMalformedSuffixes: runs with
// non-numeric suffixes are skipped.
func TestNextScheduledRunNumber_IgnoresMalformedSuffixes(t *testing.T) {
	schedule := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "sched", Namespace: "default"},
		Status:     sympoziumv1alpha1.SympoziumScheduleStatus{TotalRuns: 0},
	}
	goodRun := makeOrphanRun("sched", 5)
	// Same prefix, non-numeric suffix — must be skipped.
	bogus := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sched-abc123",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/schedule": "sched"},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{AgentRef: "i", Task: sympoziumv1alpha1.NewStringTask("x")},
	}
	r, _ := newScheduleTestReconciler(t, schedule, goodRun, bogus)

	n, err := r.nextScheduledRunNumber(context.Background(), schedule)
	if err != nil {
		t.Fatalf("nextScheduledRunNumber: %v", err)
	}
	if n != 6 {
		t.Errorf("got %d, want 6 (only numeric suffixes counted)", n)
	}
}

func makeOrphanRun(scheduleName string, suffix int) *sympoziumv1alpha1.AgentRun {
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", scheduleName, suffix),
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/schedule": scheduleName,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "any-instance",
			Task:     sympoziumv1alpha1.NewStringTask("x"),
		},
	}
}
