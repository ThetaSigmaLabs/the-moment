// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// spool_assignment_test.go
// =============================================================================
// Tests that verify spool IDs from toolhead assignments are correctly stored in
// print records and available for cost calculations.
//
// Context: a spool assigned to a toolhead must propagate through to
// print_history.spool_id and print_filament_usage.spool_id so that cost
// calculations and the history UI can reference the correct filament.
//
// Run:
//   go test ./... -v -run TestLogOctoPrintRecord_SpoolID|TestLogPrintUsageFull_Spool
// =============================================================================

import (
	"testing"
	"time"
)

// TestLogOctoPrintRecord_SpoolIDStoredInHistory verifies that a non-zero SpoolID
// sent in the OctoPrint payload is stored in both print_history (T0 backfill) and
// the corresponding print_filament_usage row.
func TestLogOctoPrintRecord_SpoolIDStoredInHistory(t *testing.T) {
	bridge := testBridge(t)

	payload := OctoPrintPayload{
		Source:            "octoprint",
		PrinterID:         "ender3-v3-se",
		FileName:          "petg_part.gcode",
		Status:            "completed",
		StartedAt:         time.Now().Add(-45 * time.Minute),
		EndedAt:           time.Now(),
		TotalDurationSec:  2700,
		PrintDurationSec:  2700,
		TimePrecision:     "exact",
		FilamentPrecision: "measured",
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, SpoolID: 42, FilamentUsedMM: 3200, FilamentUsedG: 9.5},
		},
	}

	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord: %v", err)
	}

	entry, err := bridge.GetPrintHistoryEntry(printID)
	if err != nil {
		t.Fatalf("GetPrintHistoryEntry: %v", err)
	}

	// print_history.spool_id must be backfilled from the T0 primary segment.
	if entry.SpoolID != 42 {
		t.Errorf("print_history.spool_id: got %d, want 42", entry.SpoolID)
	}

	if len(entry.FilamentUsages) != 1 {
		t.Fatalf("expected 1 filament_usage row, got %d", len(entry.FilamentUsages))
	}
	if entry.FilamentUsages[0].SpoolID != 42 {
		t.Errorf("print_filament_usage.spool_id: got %d, want 42", entry.FilamentUsages[0].SpoolID)
	}
}

// TestLogOctoPrintRecord_MultiToolheadSpoolIDs verifies that each toolhead's
// SpoolID is stored in its filament_usage row, and that print_history.spool_id
// is backfilled from T0 only.
func TestLogOctoPrintRecord_MultiToolheadSpoolIDs(t *testing.T) {
	bridge := testBridge(t)

	payload := OctoPrintPayload{
		Source:            "octoprint",
		PrinterID:         "ender3-v3-se",
		FileName:          "dual_material.gcode",
		Status:            "completed",
		StartedAt:         time.Now().Add(-90 * time.Minute),
		EndedAt:           time.Now(),
		TotalDurationSec:  5400,
		PrintDurationSec:  5400,
		TimePrecision:     "exact",
		FilamentPrecision: "measured",
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, SpoolID: 10, FilamentUsedMM: 2500, FilamentUsedG: 5.0},
			{ToolIndex: 1, SpoolID: 20, FilamentUsedMM: 1800, FilamentUsedG: 3.0},
		},
	}

	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord: %v", err)
	}

	entry, err := bridge.GetPrintHistoryEntry(printID)
	if err != nil {
		t.Fatalf("GetPrintHistoryEntry: %v", err)
	}

	// print_history.spool_id is T0's spool only.
	if entry.SpoolID != 10 {
		t.Errorf("print_history.spool_id: got %d, want 10 (T0 backfill)", entry.SpoolID)
	}

	if len(entry.FilamentUsages) != 2 {
		t.Fatalf("expected 2 filament_usage rows, got %d", len(entry.FilamentUsages))
	}
	if entry.FilamentUsages[0].SpoolID != 10 {
		t.Errorf("T0 spool_id: got %d, want 10", entry.FilamentUsages[0].SpoolID)
	}
	if entry.FilamentUsages[1].SpoolID != 20 {
		t.Errorf("T1 spool_id: got %d, want 20", entry.FilamentUsages[1].SpoolID)
	}
}

// TestLogPrintUsageFull_SpoolFromToolheadMapping verifies the PrusaLink path:
// a spool mapped to a toolhead via SetToolheadMapping is correctly stored in the
// resulting print_history record.
func TestLogPrintUsageFull_SpoolFromToolheadMapping(t *testing.T) {
	bridge := testBridge(t)

	insertPrinterConfig(t, bridge, "prusa-printer")

	// Assign spool 99 to T0 — this is the mapping the monitor would look up
	// via GetToolheadMapping before calling LogPrintUsageFull.
	if err := bridge.SetToolheadMapping("prusa-printer", 0, 99); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	// Verify the mapping round-trips before using it.
	spoolID, err := bridge.GetToolheadMapping("prusa-printer", 0)
	if err != nil {
		t.Fatalf("GetToolheadMapping: %v", err)
	}
	if spoolID != 99 {
		t.Fatalf("GetToolheadMapping: got %d, want 99", spoolID)
	}

	// Simulate what the monitor does: look up the spool and pass it to the log call.
	printID, err := bridge.LogPrintUsageFull(
		"prusa-printer", 0, spoolID, 10.0,
		"model.gcode", 30, "completed", "", "", "prusalink",
	)
	if err != nil {
		t.Fatalf("LogPrintUsageFull: %v", err)
	}
	if printID <= 0 {
		t.Fatalf("expected positive printID, got %d", printID)
	}

	entry, err := bridge.GetPrintHistoryEntry(printID)
	if err != nil {
		t.Fatalf("GetPrintHistoryEntry: %v", err)
	}

	if entry.SpoolID != 99 {
		t.Errorf("print_history.spool_id: got %d, want 99", entry.SpoolID)
	}
	if entry.PrinterName != "prusa-printer" {
		t.Errorf("printer_name: got %q, want %q", entry.PrinterName, "prusa-printer")
	}
	if entry.ToolheadID != 0 {
		t.Errorf("toolhead_id: got %d, want 0", entry.ToolheadID)
	}
}
