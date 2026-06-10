// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

// =============================================================================
// monitor_hooks_test.go
// =============================================================================
// Unit tests for the NFC print-hook integration (no mock servers required):
//   - SnapshotAssignmentsForPrint with current and point-in-time queries
//   - OctoPrint record logging triggers snapshot linked to p.StartedAt
// =============================================================================

import (
	"testing"
	"time"
)

// newTestBridge creates an isolated FilamentBridge backed by a temp SQLite DB.
func newTestBridge(t *testing.T) *FilamentBridge {
	t.Helper()
	t.Setenv("THE_MOMENT_DB_PATH", t.TempDir())
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })
	return bridge
}

// countSpoolEvents returns the number of print_spool_events rows for a print.
func countSpoolEvents(t *testing.T, bridge *FilamentBridge, printID int) int {
	t.Helper()
	var n int
	if err := bridge.db.QueryRow(
		`SELECT COUNT(*) FROM print_spool_events WHERE print_history_id = ?`, printID,
	).Scan(&n); err != nil {
		t.Fatalf("countSpoolEvents: %v", err)
	}
	return n
}

// insertDummyPrintHistory inserts a minimal print_history row and returns its ID.
// Used to satisfy the print_spool_events foreign key in pure-unit tests.
func (b *FilamentBridge) insertDummyPrintHistory(t *testing.T) (int, error) {
	t.Helper()
	res, err := b.db.Exec(`
		INSERT INTO print_history
			(printer_name, toolhead_id, spool_id, filament_used,
			 print_started, print_finished, job_name, status)
		VALUES ('test', 0, 0, 0, datetime('now'), datetime('now'), 'unit-test', 'completed')
	`)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return int(id), nil
}

// ─── SnapshotAssignmentsForPrint — current assignments ────────────────────────

// TestSnapshot_CurrentAssignments verifies that a zero atTime snapshots whatever
// assignments are currently active.
func TestSnapshot_CurrentAssignments(t *testing.T) {
	bridge := newTestBridge(t)
	const pid = "test-printer"

	if err := bridge.SetAssignment(pid, 0, 10, "manual"); err != nil {
		t.Fatalf("SetAssignment t0: %v", err)
	}
	if err := bridge.SetAssignment(pid, 1, 20, "manual"); err != nil {
		t.Fatalf("SetAssignment t1: %v", err)
	}

	printID, err := bridge.insertDummyPrintHistory(t)
	if err != nil {
		t.Fatalf("insertDummyPrintHistory: %v", err)
	}

	if err := bridge.SnapshotAssignmentsForPrint(printID, pid, time.Time{}); err != nil {
		t.Fatalf("SnapshotAssignmentsForPrint: %v", err)
	}

	events, err := bridge.GetPrintSpoolEvents(printID)
	if err != nil {
		t.Fatalf("GetPrintSpoolEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 spool events, got %d", len(events))
	}
	if events[0].ToolheadIndex != 0 || events[0].NewSpoolmanSpoolID != 10 {
		t.Errorf("toolhead 0: want spool 10, got %+v", events[0])
	}
	if events[1].ToolheadIndex != 1 || events[1].NewSpoolmanSpoolID != 20 {
		t.Errorf("toolhead 1: want spool 20, got %+v", events[1])
	}
	for _, e := range events {
		if e.EventType != "start" {
			t.Errorf("expected event_type 'start', got %q", e.EventType)
		}
		if e.OldSpoolmanSpoolID != nil {
			t.Errorf("expected nil old_spool_id for start event, got %v", e.OldSpoolmanSpoolID)
		}
	}
}

// TestSnapshot_NoAssignments verifies that a zero atTime with no assignments
// produces zero spool events without error.
func TestSnapshot_NoAssignments(t *testing.T) {
	bridge := newTestBridge(t)

	printID, err := bridge.insertDummyPrintHistory(t)
	if err != nil {
		t.Fatalf("insertDummyPrintHistory: %v", err)
	}

	if err := bridge.SnapshotAssignmentsForPrint(printID, "empty-printer", time.Time{}); err != nil {
		t.Fatalf("SnapshotAssignmentsForPrint: %v", err)
	}

	if n := countSpoolEvents(t, bridge, printID); n != 0 {
		t.Errorf("expected 0 spool events for unassigned printer, got %d", n)
	}
}

// ─── SnapshotAssignmentsForPrint — point-in-time query ────────────────────────

// TestSnapshot_PointInTime verifies that a non-zero atTime captures assignments
// active at that instant, not the current state.
//
// Timeline:
//
//	T0: assign spool 99 to toolhead 0
//	    sleep 10ms
//	T1: record printStartTime
//	    sleep 10ms
//	T2: swap toolhead 0 to spool 77 (simulates mid-print swap)
//	T3: snapshot with atTime=T1 → must see spool 99, not 77
func TestSnapshot_PointInTime(t *testing.T) {
	bridge := newTestBridge(t)
	const pid = "test-printer"

	if err := bridge.SetAssignment(pid, 0, 99, "manual"); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	printStartTime := time.Now()
	time.Sleep(10 * time.Millisecond)

	// Swap spool after print started
	if err := bridge.SetAssignment(pid, 0, 77, "manual"); err != nil {
		t.Fatalf("SetAssignment swap: %v", err)
	}

	// Current state must be spool 77
	current, err := bridge.GetCurrentAssignment(pid, 0)
	if err != nil {
		t.Fatalf("GetCurrentAssignment: %v", err)
	}
	if current == nil || current.SpoolmanSpoolID != 77 {
		t.Fatalf("expected current spool 77, got %v", current)
	}

	// Snapshot at print start — must capture spool 99
	printID, err := bridge.insertDummyPrintHistory(t)
	if err != nil {
		t.Fatalf("insertDummyPrintHistory: %v", err)
	}

	if err := bridge.SnapshotAssignmentsForPrint(printID, pid, printStartTime); err != nil {
		t.Fatalf("SnapshotAssignmentsForPrint: %v", err)
	}

	events, err := bridge.GetPrintSpoolEvents(printID)
	if err != nil {
		t.Fatalf("GetPrintSpoolEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 spool event, got %d: %+v", len(events), events)
	}
	if events[0].NewSpoolmanSpoolID != 99 {
		t.Errorf("expected spool 99 at print start, got spool %d", events[0].NewSpoolmanSpoolID)
	}
}

// TestSnapshot_PointInTime_MultipleToolheads verifies point-in-time with two
// toolheads where only one was swapped mid-print.
func TestSnapshot_PointInTime_MultipleToolheads(t *testing.T) {
	bridge := newTestBridge(t)
	const pid = "multi-head-printer"

	// T0: both toolheads assigned
	if err := bridge.SetAssignment(pid, 0, 10, "manual"); err != nil {
		t.Fatalf("SetAssignment t0: %v", err)
	}
	if err := bridge.SetAssignment(pid, 1, 20, "manual"); err != nil {
		t.Fatalf("SetAssignment t1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	printStartTime := time.Now()
	time.Sleep(10 * time.Millisecond)

	// Swap toolhead 1 only
	if err := bridge.SetAssignment(pid, 1, 30, "manual"); err != nil {
		t.Fatalf("SetAssignment t1 swap: %v", err)
	}

	printID, err := bridge.insertDummyPrintHistory(t)
	if err != nil {
		t.Fatalf("insertDummyPrintHistory: %v", err)
	}

	if err := bridge.SnapshotAssignmentsForPrint(printID, pid, printStartTime); err != nil {
		t.Fatalf("SnapshotAssignmentsForPrint: %v", err)
	}

	events, err := bridge.GetPrintSpoolEvents(printID)
	if err != nil {
		t.Fatalf("GetPrintSpoolEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 spool events, got %d: %+v", len(events), events)
	}

	// Toolhead 0 → spool 10 (unchanged), toolhead 1 → spool 20 (pre-swap)
	for _, e := range events {
		switch e.ToolheadIndex {
		case 0:
			if e.NewSpoolmanSpoolID != 10 {
				t.Errorf("toolhead 0: expected spool 10, got %d", e.NewSpoolmanSpoolID)
			}
		case 1:
			if e.NewSpoolmanSpoolID != 20 {
				t.Errorf("toolhead 1: expected spool 20 (pre-swap), got %d", e.NewSpoolmanSpoolID)
			}
		default:
			t.Errorf("unexpected toolhead index %d in events", e.ToolheadIndex)
		}
	}
}

// ─── OctoPrint print hook ─────────────────────────────────────────────────────

// TestOctoPrintHook_SnapshotCreated verifies that LogOctoPrintRecord creates
// print_spool_events rows for the NFC-assigned spool.
func TestOctoPrintHook_SnapshotCreated(t *testing.T) {
	bridge := newTestBridge(t)
	const printerID = "octo-printer"
	const spoolID = 55

	if err := bridge.SetAssignment(printerID, 0, spoolID, "manual"); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}

	// StartedAt must be after the assignment was made so the point-in-time query matches.
	managed := false
	payload := OctoPrintPayload{
		PrinterID:         printerID,
		FileName:          "test.gcode",
		Status:            "completed",
		Source:            "octoprint",
		StartedAt:         time.Now().Add(time.Millisecond), // assignment was just made; start is slightly after
		EndedAt:           time.Now().Add(5 * time.Minute),
		TotalDurationSec:  300,
		PrintDurationSec:  300,
		TimePrecision:     "exact",
		FilamentPrecision: "measured",
		SpoolmanManaged:   &managed,
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, ChangeNumber: 0, SpoolID: spoolID, FilamentUsedG: 20.0, FilamentUsedMM: 6000},
		},
	}

	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord: %v", err)
	}

	events, err := bridge.GetPrintSpoolEvents(printID)
	if err != nil {
		t.Fatalf("GetPrintSpoolEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no print_spool_events rows created for OctoPrint print")
	}
	if events[0].NewSpoolmanSpoolID != spoolID || events[0].EventType != "start" {
		t.Errorf("unexpected spool event: %+v", events[0])
	}
}

// TestOctoPrintHook_StartedAtPointInTime verifies the snapshot uses p.StartedAt
// so that mid-print reassignments are not captured.
func TestOctoPrintHook_StartedAtPointInTime(t *testing.T) {
	bridge := newTestBridge(t)
	const pid = "octo-printer-2"

	// Assign spool 11 before the print started
	if err := bridge.SetAssignment(pid, 0, 11, "manual"); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	printStartedAt := time.Now()
	time.Sleep(10 * time.Millisecond)

	// Mid-print swap to spool 22
	if err := bridge.SetAssignment(pid, 0, 22, "manual"); err != nil {
		t.Fatalf("mid-print SetAssignment: %v", err)
	}

	managed := false
	payload := OctoPrintPayload{
		PrinterID:         pid,
		FileName:          "test2.gcode",
		Status:            "completed",
		Source:            "octoprint",
		StartedAt:         printStartedAt,
		EndedAt:           time.Now(),
		TotalDurationSec:  300,
		PrintDurationSec:  300,
		TimePrecision:     "exact",
		FilamentPrecision: "measured",
		SpoolmanManaged:   &managed,
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, ChangeNumber: 0, SpoolID: 11, FilamentUsedG: 5.0},
		},
	}

	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord: %v", err)
	}

	events, err := bridge.GetPrintSpoolEvents(printID)
	if err != nil {
		t.Fatalf("GetPrintSpoolEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
	}
	// Must be spool 11 (active at printStartedAt), not spool 22 (mid-print swap)
	if events[0].NewSpoolmanSpoolID != 11 {
		t.Errorf("expected spool 11 at print start, got spool %d", events[0].NewSpoolmanSpoolID)
	}
}
