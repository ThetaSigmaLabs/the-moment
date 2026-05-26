// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// prusalink_reliability_test.go
// =============================================================================
// Unit tests for Phase 1 reliability additions:
//   - DB helpers: GetLastKnownState, UpsertLastKnownState, IsJobProcessed,
//     MarkJobProcessed, IncrementFailureCount
//   - pollIntervalForState mapping
//   - logStateTransition (no-op on same state)
//
// These tests use a real in-memory SQLite DB via NewFilamentBridge.
// Integration-level dedup test lives in prusalink_dedup_integration_test.go.
// =============================================================================

import (
	"testing"
	"time"
)

// insertPrinterConfig inserts a minimal printer_configs row so FK constraints pass.
func insertPrinterConfig(t *testing.T, bridge *FilamentBridge, printerID string) {
	t.Helper()
	_, err := bridge.db.Exec(`
		INSERT OR IGNORE INTO printer_configs
			(printer_id, name, model, ip_address, api_key, toolheads, is_virtual)
		VALUES (?, ?, '', '', '', 1, 0)`, printerID, printerID)
	if err != nil {
		t.Fatalf("insertPrinterConfig: %v", err)
	}
}

// ─── pollIntervalForState ─────────────────────────────────────────────────────

func TestPollIntervalForState(t *testing.T) {
	cases := []struct {
		state string
		want  time.Duration
	}{
		{StateFinished, 2 * time.Second},
		{StateAttention, 3 * time.Second},
		{StatePrinting, 5 * time.Second},
		{StatePaused, 10 * time.Second},
		{StateIdle, 20 * time.Second},
		{"unknown", 30 * time.Second},
		{"", 30 * time.Second},
	}
	for _, c := range cases {
		got := pollIntervalForState(c.state)
		if got != c.want {
			t.Errorf("pollIntervalForState(%q): got %v, want %v", c.state, got, c.want)
		}
	}
}

// ─── logStateTransition ───────────────────────────────────────────────────────

func TestLogStateTransition_NoopOnSameState(t *testing.T) {
	// logStateTransition is log-only; just verify it doesn't panic on same-state
	// polls (the early-return path) and on real transitions.
	logStateTransition("p1", StateIdle, StateIdle, 0, 0)        // same state — no-op
	logStateTransition("p1", StateIdle, StatePrinting, 42, 0.5) // transition — logs
}

// ─── GetLastKnownState / UpsertLastKnownState ─────────────────────────────────

func TestStateCache_RoundTrip(t *testing.T) {
	bridge := newTestBridge(t)
	const id = "printer-1"
	insertPrinterConfig(t, bridge, id)

	// Missing row returns zero-value struct (not an error).
	got, err := bridge.GetLastKnownState(id)
	if err != nil {
		t.Fatalf("GetLastKnownState on empty: %v", err)
	}
	if got.PrinterID != "" {
		t.Errorf("expected zero-value struct for missing row, got %+v", got)
	}

	// Upsert then read back.
	if err := bridge.UpsertLastKnownState(id, StatePrinting, 99, 55.5, 120); err != nil {
		t.Fatalf("UpsertLastKnownState: %v", err)
	}
	got, err = bridge.GetLastKnownState(id)
	if err != nil {
		t.Fatalf("GetLastKnownState after upsert: %v", err)
	}

	if got.PrinterID != id {
		t.Errorf("PrinterID: got %q, want %q", got.PrinterID, id)
	}
	if got.LastState != StatePrinting {
		t.Errorf("LastState: got %q, want %q", got.LastState, StatePrinting)
	}
	if got.LastJobID != 99 {
		t.Errorf("LastJobID: got %d, want 99", got.LastJobID)
	}
	if got.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures after success: got %d, want 0", got.ConsecutiveFailures)
	}

	// NextPollAt should be approximately now + pollIntervalForState(PRINTING)
	wantDelay := pollIntervalForState(StatePrinting)
	gap := got.NextPollAt.Sub(got.LastPolledAt)
	if gap < wantDelay-time.Second || gap > wantDelay+time.Second {
		t.Errorf("next_poll_at gap: got %v, want ≈%v", gap, wantDelay)
	}
}

func TestStateCache_UpsertResetsFailures(t *testing.T) {
	bridge := newTestBridge(t)
	const id = "printer-2"
	insertPrinterConfig(t, bridge, id)

	// Build up some failures first.
	for i := 0; i < 3; i++ {
		if _, err := bridge.IncrementFailureCount(id); err != nil {
			t.Fatalf("IncrementFailureCount: %v", err)
		}
	}
	got, _ := bridge.GetLastKnownState(id)
	if got.ConsecutiveFailures != 3 {
		t.Errorf("expected 3 failures, got %d", got.ConsecutiveFailures)
	}

	// Successful poll upsert should reset counter to 0.
	bridge.UpsertLastKnownState(id, StateIdle, 0, 0, 0)
	got, _ = bridge.GetLastKnownState(id)
	if got.ConsecutiveFailures != 0 {
		t.Errorf("expected 0 after success upsert, got %d", got.ConsecutiveFailures)
	}
}

// ─── IncrementFailureCount ────────────────────────────────────────────────────

func TestIncrementFailureCount(t *testing.T) {
	bridge := newTestBridge(t)
	const id = "printer-3"
	insertPrinterConfig(t, bridge, id)

	for want := 1; want <= 5; want++ {
		got, err := bridge.IncrementFailureCount(id)
		if err != nil {
			t.Fatalf("IncrementFailureCount iteration %d: %v", want, err)
		}
		if got != want {
			t.Errorf("iteration %d: got count %d, want %d", want, got, want)
		}
	}
}

// ─── IsJobProcessed / MarkJobProcessed ───────────────────────────────────────

func TestJobDedup_NotYetProcessed(t *testing.T) {
	bridge := newTestBridge(t)
	const id = "printer-4"
	insertPrinterConfig(t, bridge, id)

	processed, err := bridge.IsJobProcessed(id, 42)
	if err != nil {
		t.Fatalf("IsJobProcessed: %v", err)
	}
	if processed {
		t.Error("expected job 42 to be unprocessed, but got processed=true")
	}
}

func TestJobDedup_MarkThenCheck(t *testing.T) {
	bridge := newTestBridge(t)
	const id = "printer-5"
	insertPrinterConfig(t, bridge, id)

	if err := bridge.MarkJobProcessed(id, 77, "finished"); err != nil {
		t.Fatalf("MarkJobProcessed: %v", err)
	}

	processed, err := bridge.IsJobProcessed(id, 77)
	if err != nil {
		t.Fatalf("IsJobProcessed after mark: %v", err)
	}
	if !processed {
		t.Error("expected job 77 to be processed after MarkJobProcessed")
	}
}

func TestJobDedup_DifferentPrintersDontConflict(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-A")
	insertPrinterConfig(t, bridge, "printer-B")

	bridge.MarkJobProcessed("printer-A", 1, "finished")

	// Same job ID on a different printer must not be considered processed.
	processed, _ := bridge.IsJobProcessed("printer-B", 1)
	if processed {
		t.Error("job 1 on printer-B should be unprocessed; printer-A's record leaked")
	}
}

func TestJobDedup_ZeroJobIDNeverSuppressed(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-6")

	// MarkJobProcessed with ID 0 must be a no-op.
	bridge.MarkJobProcessed("printer-6", 0, "finished")

	// IsJobProcessed with ID 0 must always return false.
	processed, _ := bridge.IsJobProcessed("printer-6", 0)
	if processed {
		t.Error("job ID 0 should never be considered processed")
	}
}

func TestJobDedup_DoubleMarkIsIdempotent(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-7")

	if err := bridge.MarkJobProcessed("printer-7", 55, "finished"); err != nil {
		t.Fatalf("first MarkJobProcessed: %v", err)
	}
	// Second mark with a different outcome should not error (INSERT OR REPLACE).
	if err := bridge.MarkJobProcessed("printer-7", 55, "stopped"); err != nil {
		t.Fatalf("second MarkJobProcessed: %v", err)
	}

	processed, _ := bridge.IsJobProcessed("printer-7", 55)
	if !processed {
		t.Error("job should still be processed after double-mark")
	}
}

// TestJobDedup_RecoveredJobCanFinishNormally verifies that a job previously written
// as "recovered" (service restarted mid-print) is not blocked by the dedup check
// and can be processed normally when the print actually completes.
func TestJobDedup_RecoveredJobCanFinishNormally(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-8")

	// Simulate restart recovery — job 99 was in progress and got a placeholder row.
	if err := bridge.MarkJobProcessed("printer-8", 99, "recovered"); err != nil {
		t.Fatalf("MarkJobProcessed recovered: %v", err)
	}

	// IsJobProcessed must return false so the finish handler is NOT skipped.
	processed, _ := bridge.IsJobProcessed("printer-8", 99)
	if processed {
		t.Error("recovered job should not be blocked by dedup; IsJobProcessed must return false")
	}

	// After the print actually finishes, mark it with the real outcome.
	if err := bridge.MarkJobProcessed("printer-8", 99, "finished"); err != nil {
		t.Fatalf("MarkJobProcessed finished: %v", err)
	}

	// Now IsJobProcessed must return true — re-fires should be suppressed.
	processed, _ = bridge.IsJobProcessed("printer-8", 99)
	if !processed {
		t.Error("job 99 should be processed after being marked finished")
	}

	// Confirm the outcome was upgraded from "recovered" to "finished".
	var outcome string
	if err := bridge.db.QueryRow(
		`SELECT outcome FROM processed_jobs WHERE printer_id = 'printer-8' AND job_id = 99`,
	).Scan(&outcome); err != nil {
		t.Fatalf("query outcome: %v", err)
	}
	if outcome != "finished" {
		t.Errorf("expected outcome='finished' after upgrade, got %q", outcome)
	}
}
