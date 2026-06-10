package main

import (
	"testing"
)

// ─── snapshotTargets ─────────────────────────────────────────────────────────

func TestSnapshotTargets_Interval(t *testing.T) {
	targets := snapshotTargets(ProgressSnapshotConfig{Mode: "interval", Interval: 10})
	want := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90}
	if len(targets) != len(want) {
		t.Fatalf("interval=10: got %v, want %v", targets, want)
	}
	for i, v := range want {
		if targets[i] != v {
			t.Errorf("targets[%d] = %v, want %v", i, targets[i], v)
		}
	}
}

func TestSnapshotTargets_Interval25(t *testing.T) {
	targets := snapshotTargets(ProgressSnapshotConfig{Mode: "interval", Interval: 25})
	want := []float64{25, 50, 75}
	if len(targets) != len(want) {
		t.Fatalf("interval=25: got %v, want %v", targets, want)
	}
	for i, v := range want {
		if targets[i] != v {
			t.Errorf("targets[%d] = %v, want %v", i, targets[i], v)
		}
	}
}

func TestSnapshotTargets_MilestonesSorted(t *testing.T) {
	targets := snapshotTargets(ProgressSnapshotConfig{Mode: "milestones", Milestones: []float64{75, 25, 50}})
	want := []float64{25, 50, 75}
	if len(targets) != len(want) {
		t.Fatalf("got %v, want %v", targets, want)
	}
	for i, v := range want {
		if targets[i] != v {
			t.Errorf("targets[%d] = %v, want %v", i, targets[i], v)
		}
	}
}

func TestSnapshotTargets_MilestonesDedup(t *testing.T) {
	targets := snapshotTargets(ProgressSnapshotConfig{Mode: "milestones", Milestones: []float64{50, 50, 75}})
	if len(targets) != 2 {
		t.Fatalf("expected 2 unique milestones, got %v", targets)
	}
}

func TestSnapshotTargets_MilestonesFiltersEdges(t *testing.T) {
	// 0 and 100 must be excluded (handled by other events)
	targets := snapshotTargets(ProgressSnapshotConfig{Mode: "milestones", Milestones: []float64{0, 50, 100}})
	if len(targets) != 1 || targets[0] != 50 {
		t.Fatalf("expected [50], got %v", targets)
	}
}

func TestSnapshotTargets_None(t *testing.T) {
	if snapshotTargets(ProgressSnapshotConfig{Mode: "none"}) != nil {
		t.Fatal("mode=none should return nil")
	}
}

func TestSnapshotTargets_ZeroValue(t *testing.T) {
	if snapshotTargets(ProgressSnapshotConfig{}) != nil {
		t.Fatal("zero-value config should return nil")
	}
}

func TestSnapshotTargets_InvalidInterval(t *testing.T) {
	if snapshotTargets(ProgressSnapshotConfig{Mode: "interval", Interval: 0}) != nil {
		t.Fatal("interval=0 should return nil")
	}
	if snapshotTargets(ProgressSnapshotConfig{Mode: "interval", Interval: 100}) != nil {
		t.Fatal("interval=100 should return nil")
	}
}

// ─── crossedTargets ──────────────────────────────────────────────────────────

func TestCrossedTargets_BasicInterval(t *testing.T) {
	targets := []float64{10, 20, 30}

	got := crossedTargets(targets, 0, 15)
	if len(got) != 1 || got[0] != 10 {
		t.Fatalf("(0→15): want [10], got %v", got)
	}
}

func TestCrossedTargets_MultipleInOneTick(t *testing.T) {
	targets := []float64{10, 20, 30}
	got := crossedTargets(targets, 0, 35)
	want := []float64{10, 20, 30}
	if len(got) != len(want) {
		t.Fatalf("(0→35): want %v, got %v", want, got)
	}
}

func TestCrossedTargets_NoDuplicate(t *testing.T) {
	targets := []float64{25, 50}
	// lastPct already at 25 — should not re-fire 25
	got := crossedTargets(targets, 25, 30)
	if len(got) != 0 {
		t.Fatalf("lastPct=25, currentPct=30: expected nothing crossed, got %v", got)
	}
}

func TestCrossedTargets_ExactBoundary(t *testing.T) {
	targets := []float64{50}
	got := crossedTargets(targets, 40, 50)
	if len(got) != 1 || got[0] != 50 {
		t.Fatalf("(40→50): want [50], got %v", got)
	}
}

func TestCrossedTargets_NilTargets(t *testing.T) {
	if crossedTargets(nil, 0, 50) != nil {
		t.Fatal("nil targets should return nil")
	}
}

func TestCrossedTargets_NoProgress(t *testing.T) {
	targets := []float64{10, 20, 30}
	// Same value — no crossing
	if crossedTargets(targets, 25, 25) != nil {
		t.Fatal("equal lastPct and currentPct should return nil")
	}
	// Regression (shouldn't happen in practice but must be safe)
	if crossedTargets(targets, 30, 20) != nil {
		t.Fatal("regressed progress should return nil")
	}
}

func TestCrossedTargets_Milestones(t *testing.T) {
	targets := []float64{25, 50, 75}

	got := crossedTargets(targets, 0, 30)
	if len(got) != 1 || got[0] != 25 {
		t.Fatalf("(0→30): want [25], got %v", got)
	}

	got = crossedTargets(targets, 25, 80)
	if len(got) != 2 || got[0] != 50 || got[1] != 75 {
		t.Fatalf("(25→80): want [50 75], got %v", got)
	}
}

// ─── labelFor ────────────────────────────────────────────────────────────────

func TestLabelFor(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
	}{
		{25, "25% progress"},
		{50, "50% progress"},
		{75, "75% progress"},
		{10, "10% progress"},
		{90, "90% progress"},
	}
	for _, tc := range cases {
		got := labelFor(tc.pct)
		if got != tc.want {
			t.Errorf("labelFor(%.0f) = %q, want %q", tc.pct, got, tc.want)
		}
	}
}
