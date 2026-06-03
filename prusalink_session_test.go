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

	// Session must be kept alive for continued progress tracking and start-time preservation.
	// The finish handler (handlePrusaLinkPrintFinished) is responsible for cleanup.
	got, _ := bridge.GetActivePrintSession("printer-r1", 500)
	if got == nil {
		t.Error("orphaned session should be kept alive after reconcile for progress tracking")
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

	// Session is now kept alive after reconcile (not deleted), so the second
	// run finds it again, sees alreadyRecovered=true, and skips stub creation.
	bridge.ReconcileActiveSessions()
	bridge.ReconcileActiveSessions()

	var count int
	bridge.db.QueryRow(
		`SELECT COUNT(*) FROM print_history WHERE printer_name = 'printer-r3' AND recovered = 1`,
	).Scan(&count)
	if count != 1 {
		t.Errorf("second reconcile run should not duplicate recovery rows, got %d", count)
	}
}

// TestReconcile_MultipleRestartsNoDuplicateStubs simulates the real-world restart cycle:
// reconcile → MonitorPrinters re-creates session → reconcile → re-creates → reconcile.
// Only one [RECOVERED] stub should ever exist regardless of restart count.
func TestReconcile_MultipleRestartsNoDuplicateStubs(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-dup")

	bridge.UpsertActivePrintSession("printer-dup", 800, time.Now().Add(-time.Hour), "model.gcode", 0, "")

	// Restart 1: creates stub, keeps session
	bridge.ReconcileActiveSessions()

	// Simulate MonitorPrinters detecting the print is still running and re-upserting
	// the session (ON CONFLICT DO NOTHING — first write wins, so started_at is stable)
	bridge.UpsertActivePrintSession("printer-dup", 800, time.Now().Add(-30*time.Minute), "model.gcode", 0, "")

	// Restart 2: alreadyRecovered=true — must NOT create another stub
	bridge.ReconcileActiveSessions()

	bridge.UpsertActivePrintSession("printer-dup", 800, time.Now().Add(-10*time.Minute), "model.gcode", 0, "")

	// Restart 3: still alreadyRecovered=true
	bridge.ReconcileActiveSessions()

	var count int
	bridge.db.QueryRow(
		`SELECT COUNT(*) FROM print_history WHERE printer_name = 'printer-dup' AND recovered = 1`,
	).Scan(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 recovery stub across 3 restarts, got %d", count)
	}
}

// TestReconcile_SessionPreservedAfterReconcile verifies that the active session
// is NOT deleted by reconcile — it stays alive for progress tracking.
func TestReconcile_SessionPreservedAfterReconcile(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-sp")

	bridge.UpsertActivePrintSession("printer-sp", 900, time.Now().Add(-time.Hour), "part.gcode", 0, "")
	bridge.UpdateSessionProgress("printer-sp", 900, 72.0, 3600)

	bridge.ReconcileActiveSessions()

	got, err := bridge.GetActivePrintSession("printer-sp", 900)
	if err != nil {
		t.Fatalf("GetActivePrintSession after reconcile: %v", err)
	}
	if got == nil {
		t.Fatal("session should be kept alive after reconcile")
	}
	if got.LastSeenProgress != 72.0 {
		t.Errorf("LastSeenProgress: got %.1f, want 72.0", got.LastSeenProgress)
	}
}

// TestReconcile_AlreadyRecoveredRestoresInMemoryState verifies that on a second reconcile
// (alreadyRecovered=true) the in-memory maps are still populated, so MonitorPrinters
// won't treat the ongoing print as a new job.
func TestReconcile_AlreadyRecoveredRestoresInMemoryState(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-mem")

	bridge.UpsertActivePrintSession("printer-mem", 910, time.Now().Add(-time.Hour), "part.gcode", 0, "")

	// First reconcile: creates stub, marks recovered
	bridge.ReconcileActiveSessions()

	// Simulate a restart by wiping in-memory state
	bridge.mutex.Lock()
	bridge.wasPrinting["printer-mem"] = false
	bridge.currentJobFile["printer-mem"] = ""
	bridge.currentJobID["printer-mem"] = 0
	bridge.mutex.Unlock()

	// Second reconcile: alreadyRecovered=true, must still restore in-memory state
	bridge.ReconcileActiveSessions()

	bridge.mutex.RLock()
	wasPrinting := bridge.wasPrinting["printer-mem"]
	jobFile := bridge.currentJobFile["printer-mem"]
	jobID := bridge.currentJobID["printer-mem"]
	bridge.mutex.RUnlock()

	if !wasPrinting {
		t.Error("wasPrinting should be true after second reconcile")
	}
	if jobFile == "" {
		t.Error("currentJobFile should be restored after second reconcile")
	}
	if jobID != 910 {
		t.Errorf("currentJobID: got %d, want 910", jobID)
	}
}

// TestReconcile_DeduplicateMigrationCleansExistingStubs verifies the startup migration
// removes pre-existing duplicate [RECOVERED] rows (created by the pre-fix bug).
func TestReconcile_DeduplicateMigrationCleansExistingStubs(t *testing.T) {
	bridge := newTestBridge(t)
	insertPrinterConfig(t, bridge, "printer-mig")

	// Insert 3 duplicate stubs as the pre-fix code would have left behind
	for i := 0; i < 3; i++ {
		_, err := bridge.db.Exec(`
			INSERT INTO print_history
				(printer_name, toolhead_id, spool_id, filament_used,
				 print_started, print_finished, job_name,
				 print_time_minutes, status, session_id, source,
				 time_precision, filament_precision,
				 outcome, progress_at_stop, recovered, gcode_unavailable)
			VALUES ('printer-mig', 0, 0, 0, datetime('now','-1 hour'), datetime('now'),
			        'COREON~1.GCO [RECOVERED]', 60, 'completed', ?, 'prusalink',
			        'approximate', 'estimated', 'recovered', 50, 1, 1)`,
			newSessionID())
		if err != nil {
			t.Fatalf("insert stub %d: %v", i, err)
		}
	}

	var before int
	bridge.db.QueryRow(
		`SELECT COUNT(*) FROM print_history WHERE printer_name='printer-mig' AND recovered=1`,
	).Scan(&before)
	if before != 3 {
		t.Fatalf("precondition: expected 3 stubs, got %d", before)
	}

	if err := bridge.deduplicateRecoveryStubs(); err != nil {
		t.Fatalf("deduplicateRecoveryStubs: %v", err)
	}

	var after int
	bridge.db.QueryRow(
		`SELECT COUNT(*) FROM print_history WHERE printer_name='printer-mig' AND recovered=1`,
	).Scan(&after)
	if after != 1 {
		t.Errorf("expected 1 stub after dedup migration, got %d", after)
	}
}
