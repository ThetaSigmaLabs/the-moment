// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// prusalink_session_test.go
// =============================================================================
// Unit tests for Phase 2 active session tracking and startup reconciliation:
//   - UpsertActivePrintSession / GetActivePrintSession / UpdateSessionProgress /
//     DeleteActivePrintSession
//   - ReconcileActiveSessions: orphan → recovery row; already-processed → skip
// =============================================================================

import (
	"strings"
	"testing"
	"time"
)

// ─── ActivePrintSession CRUD ──────────────────────────────────────────────────

func TestActiveSession_RoundTrip(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-s1")

	startedAt := time.Now().UTC().Truncate(time.Second)

	if err := bridge.UpsertActivePrintSession("printer-s1", 100, startedAt, "usb/model.gcode", 512000, `[{"id":1}]`); err != nil {
		t.Fatalf("UpsertActivePrintSession: %v", err)
	}

	got, err := bridge.GetActivePrintSession("printer-s1", 100)
	if err != nil {
		t.Fatalf("GetActivePrintSession: %v", err)
	}
	if got == nil {
		t.Fatal("expected session, got nil")
	}
	if got.PrinterID != "printer-s1" {
		t.Errorf("PrinterID: got %q, want %q", got.PrinterID, "printer-s1")
	}
	if got.JobID != 100 {
		t.Errorf("JobID: got %d, want 100", got.JobID)
	}
	if got.FilePath != "usb/model.gcode" {
		t.Errorf("FilePath: got %q", got.FilePath)
	}
	if got.FileSizeBytes != 512000 {
		t.Errorf("FileSizeBytes: got %d, want 512000", got.FileSizeBytes)
	}
	if got.InitialAssignmentsJSON != `[{"id":1}]` {
		t.Errorf("InitialAssignmentsJSON: got %q", got.InitialAssignmentsJSON)
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt should not be zero")
	}
}

func TestActiveSession_MissingReturnsNil(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-s2")

	got, err := bridge.GetActivePrintSession("printer-s2", 999)
	if err != nil {
		t.Fatalf("GetActivePrintSession on missing: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for missing session, got %+v", got)
	}
}

func TestActiveSession_UpsertIsIdempotent(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-s3")

	startedAt := time.Now().UTC()
	if err := bridge.UpsertActivePrintSession("printer-s3", 200, startedAt, "file-a.gcode", 0, ""); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Second upsert for same (printer, job) must be a no-op (ON CONFLICT DO NOTHING)
	if err := bridge.UpsertActivePrintSession("printer-s3", 200, startedAt, "file-b.gcode", 0, ""); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, _ := bridge.GetActivePrintSession("printer-s3", 200)
	if got == nil {
		t.Fatal("expected session to still exist")
	}
	// First write wins
	if got.FilePath != "file-a.gcode" {
		t.Errorf("expected first file path to win, got %q", got.FilePath)
	}
}

func TestUpdateSessionProgress(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-s4")

	bridge.UpsertActivePrintSession("printer-s4", 300, time.Now(), "f.gcode", 0, "")

	if err := bridge.UpdateSessionProgress("printer-s4", 300, 42.5, 1800); err != nil {
		t.Fatalf("UpdateSessionProgress: %v", err)
	}

	got, _ := bridge.GetActivePrintSession("printer-s4", 300)
	if got == nil {
		t.Fatal("expected session")
	}
	if got.LastSeenProgress != 42.5 {
		t.Errorf("LastSeenProgress: got %.1f, want 42.5", got.LastSeenProgress)
	}
	if got.LastSeenTimePrinting != 1800 {
		t.Errorf("LastSeenTimePrinting: got %d, want 1800", got.LastSeenTimePrinting)
	}
}

func TestDeleteActivePrintSession(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-s5")

	bridge.UpsertActivePrintSession("printer-s5", 400, time.Now(), "f.gcode", 0, "")

	if err := bridge.DeleteActivePrintSession("printer-s5", 400); err != nil {
		t.Fatalf("DeleteActivePrintSession: %v", err)
	}

	got, err := bridge.GetActivePrintSession("printer-s5", 400)
	if err != nil {
		t.Fatalf("GetActivePrintSession after delete: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

// ─── ReconcileActiveSessions ──────────────────────────────────────────────────

func TestReconcile_NoOrphans(t *testing.T) {
	bridge := newTestBridge(t)
	// Empty DB — reconcile must not panic and must log "no orphaned sessions"
	bridge.ReconcileActiveSessions() // no assertions needed; just must not panic
}

func TestReconcile_OrphanedSession_WritesRecoveryRow(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-r1")

	bridge.UpsertActivePrintSession("printer-r1", 500, time.Now().Add(-30*time.Minute), "models/benchy.gcode", 0, "")
	bridge.UpdateSessionProgress("printer-r1", 500, 65.0, 900)

	bridge.ReconcileActiveSessions()

	// Session must be deleted
	got, _ := bridge.GetActivePrintSession("printer-r1", 500)
	if got != nil {
		t.Error("orphaned session should have been deleted after reconcile")
	}

	// Job must be in processed_jobs with outcome "recovered" so it won't be re-recovered
	// on the next restart — but IsJobProcessed returns false for "recovered" entries
	// because the job can still complete normally if the printer resumes.
	var outcome string
	err := bridge.db.QueryRow(
		`SELECT outcome FROM processed_jobs WHERE printer_id = 'printer-r1' AND job_id = 500`,
	).Scan(&outcome)
	if err != nil {
		t.Fatalf("processed_jobs query: %v", err)
	}
	if outcome != "recovered" {
		t.Errorf("expected outcome='recovered', got %q", outcome)
	}
	canProcessAgain, _ := bridge.IsJobProcessed("printer-r1", 500)
	if canProcessAgain {
		t.Error("IsJobProcessed should return false for 'recovered' jobs so they can complete normally")
	}

	// A print_history row must exist with recovered=1
	var count int
	var jobName string
	err = bridge.db.QueryRow(
		`SELECT COUNT(*), COALESCE(job_name,'') FROM print_history WHERE printer_name = 'printer-r1' AND recovered = 1`,
	).Scan(&count, &jobName)
	if err != nil {
		t.Fatalf("query recovered row: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 recovered history row, got %d", count)
	}
	if !strings.Contains(jobName, "[RECOVERED]") {
		t.Errorf("job_name should contain [RECOVERED], got %q", jobName)
	}
}

func TestReconcile_AlreadyProcessedSessionSkipped(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-r2")

	bridge.UpsertActivePrintSession("printer-r2", 600, time.Now().Add(-time.Hour), "f.gcode", 0, "")
	// Mark as already processed — reconcile should leave it alone (no duplicate row)
	bridge.MarkJobProcessed("printer-r2", 600, "finished")

	bridge.ReconcileActiveSessions()

	// No recovery row should be written since job was already processed
	var count int
	bridge.db.QueryRow(
		`SELECT COUNT(*) FROM print_history WHERE printer_name = 'printer-r2' AND recovered = 1`,
	).Scan(&count)
	if count != 0 {
		t.Errorf("already-processed session should not create a recovery row, got %d rows", count)
	}
}

func TestReconcile_SecondRunIsIdempotent(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-r3")

	bridge.UpsertActivePrintSession("printer-r3", 700, time.Now().Add(-time.Hour), "f.gcode", 0, "")

	bridge.ReconcileActiveSessions()
	bridge.ReconcileActiveSessions() // second run must not create a duplicate row

	var count int
	bridge.db.QueryRow(
		`SELECT COUNT(*) FROM print_history WHERE printer_name = 'printer-r3' AND recovered = 1`,
	).Scan(&count)
	if count != 1 {
		t.Errorf("second reconcile run should not duplicate recovery rows, got %d", count)
	}
}
