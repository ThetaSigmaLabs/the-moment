// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// orphaned_mappings_test.go
// =============================================================================
// Tests for GetOrphanedMappings and ClearOrphanedMappings — the logic behind
// "Check for Stuck Assignments" in Settings.
//
// An orphaned mapping is a toolhead_mappings row whose printer_name no longer
// exists in printer_configs. This happens when a printer is deleted but its
// spool assignments were not cleaned up at deletion time.
//
// Run:
//   go test ./... -v -run TestOrphanedMappings
// =============================================================================

import (
	"testing"
)

// insertToolheadMapping writes a raw toolhead_mappings row directly.
// Used in orphan tests to create mappings without going through SetToolheadMapping,
// which checks for duplicate assignments across printers.
func insertToolheadMapping(t *testing.T, bridge *FilamentBridge, printerName string, toolheadID, spoolID int) {
	t.Helper()
	_, err := bridge.db.Exec(
		`INSERT OR REPLACE INTO toolhead_mappings (printer_name, toolhead_id, spool_id, mapped_at)
		 VALUES (?, ?, ?, datetime('now'))`,
		printerName, toolheadID, spoolID,
	)
	if err != nil {
		t.Fatalf("insertToolheadMapping: %v", err)
	}
}

// TestOrphanedMappings_NoOrphansWhenPrinterExists confirms that a mapping whose
// printer_name is still in printer_configs is NOT reported as orphaned.
func TestOrphanedMappings_NoOrphansWhenPrinterExists(t *testing.T) {
	bridge := testBridge(t)

	insertPrinterConfig(t, bridge, "Printer A")
	insertToolheadMapping(t, bridge, "Printer A", 0, 1)

	orphans, err := bridge.GetOrphanedMappings()
	if err != nil {
		t.Fatalf("GetOrphanedMappings: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans, got %d: %v", len(orphans), orphans)
	}
}

// TestOrphanedMappings_DetectsDeletedPrinter confirms that deleting a printer
// from printer_configs causes its toolhead mappings to be reported as orphaned,
// while mappings for surviving printers are unaffected.
func TestOrphanedMappings_DetectsDeletedPrinter(t *testing.T) {
	bridge := testBridge(t)

	insertPrinterConfig(t, bridge, "Printer A")
	insertPrinterConfig(t, bridge, "Printer B")
	insertToolheadMapping(t, bridge, "Printer A", 0, 1)
	insertToolheadMapping(t, bridge, "Printer B", 0, 2)

	// Simulate printer deletion (no cascade on toolhead_mappings by design).
	_, err := bridge.db.Exec(`DELETE FROM printer_configs WHERE name = 'Printer B'`)
	if err != nil {
		t.Fatalf("delete printer config: %v", err)
	}

	orphans, err := bridge.GetOrphanedMappings()
	if err != nil {
		t.Fatalf("GetOrphanedMappings: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d: %v", len(orphans), orphans)
	}

	o := orphans[0]
	if o["printer_name"] != "Printer B" {
		t.Errorf("orphan printer_name: got %q, want %q", o["printer_name"], "Printer B")
	}
	if o["toolhead_id"] != 0 {
		t.Errorf("orphan toolhead_id: got %v, want 0", o["toolhead_id"])
	}
	if o["spool_id"] != 2 {
		t.Errorf("orphan spool_id: got %v, want 2", o["spool_id"])
	}
}

// TestOrphanedMappings_ClearRemovesOrphansKeepsActive confirms that
// ClearOrphanedMappings removes only the orphaned rows and returns the correct
// count, leaving active printer mappings untouched.
func TestOrphanedMappings_ClearRemovesOrphansKeepsActive(t *testing.T) {
	bridge := testBridge(t)

	insertPrinterConfig(t, bridge, "Printer A")
	insertPrinterConfig(t, bridge, "Printer B")
	insertToolheadMapping(t, bridge, "Printer A", 0, 1)
	insertToolheadMapping(t, bridge, "Printer B", 0, 2)

	_, err := bridge.db.Exec(`DELETE FROM printer_configs WHERE name = 'Printer B'`)
	if err != nil {
		t.Fatalf("delete printer config: %v", err)
	}

	count, err := bridge.ClearOrphanedMappings()
	if err != nil {
		t.Fatalf("ClearOrphanedMappings: %v", err)
	}
	if count != 1 {
		t.Errorf("expected ClearOrphanedMappings to return 1, got %d", count)
	}

	// No orphans should remain.
	orphans, err := bridge.GetOrphanedMappings()
	if err != nil {
		t.Fatalf("GetOrphanedMappings after clear: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans after clear, got %d: %v", len(orphans), orphans)
	}

	// Printer A's mapping must still exist.
	spoolID, err := bridge.GetToolheadMapping("Printer A", 0)
	if err != nil {
		t.Fatalf("GetToolheadMapping: %v", err)
	}
	if spoolID != 1 {
		t.Errorf("Printer A T0 mapping: got spool %d, want 1", spoolID)
	}
}
