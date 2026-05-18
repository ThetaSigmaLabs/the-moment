//go:build integration

package main

// =============================================================================
// bambu_test.go
// =============================================================================
// State machine tests for Bambu printer support. All tests use MockBambuClient
// so no real Bambu printer, MQTT broker, or network connection is needed.
//
// Test coverage:
//   - Idle: no updates when printer is idle
//   - NormalPrint: IDLE→RUNNING→FINISH deducts correct weight
//   - CancelledPrint: RUNNING→CANCEL deducts scaled weight
//   - PauseResume: pause does not cause premature deduction
//   - NoStatusYet: graceful handling before first MQTT message
//   - ConnectionFailure: graceful handling when Connect() errors
//   - AMS_MultiSlot: 4-slot AMS distributes weight evenly
//   - RapidPolling: FINISH polled 3× produces exactly one Spoolman update
//   - SpoolmanOffline: history written, pending outbox queued
//   - NFC_AssignmentSnapshot: SnapshotAssignmentsForPrint called correctly
// =============================================================================

import (
	"errors"
	"math"
	"testing"
)

// ─── TestBambu_Idle ───────────────────────────────────────────────────────────

// TestBambu_Idle confirms that no Spoolman updates or history entries are
// created while the printer is in the IDLE state.
func TestBambu_Idle(t *testing.T) {
	const spoolID = 1
	bridge, mock, spoolman := setupBridgeWithBambuMock(t, map[int]float64{spoolID: 1000})

	if err := bridge.SetToolheadMapping("X1C Printer", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	cfg := bambuTestConfig("X1C Printer", 1)
	mock.SetState("IDLE")

	pollBambu(t, bridge, "test-bambu", cfg)

	if len(spoolman.Updates()) != 0 {
		t.Errorf("expected no Spoolman updates while IDLE, got %d", len(spoolman.Updates()))
	}
}

// ─── TestBambu_NormalPrint ────────────────────────────────────────────────────

// TestBambu_NormalPrint confirms a complete print cycle deducts the correct
// filament weight exactly once.
//
// Sequence: IDLE → RUNNING → RUNNING(50%) → FINISH
func TestBambu_NormalPrint(t *testing.T) {
	const spoolID = 1
	const initialWeight = 1000.0
	const printWeight = 30.0

	bridge, mock, spoolman := setupBridgeWithBambuMock(t, map[int]float64{spoolID: initialWeight})

	if err := bridge.SetToolheadMapping("X1C Printer", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	cfg := bambuTestConfig("X1C Printer", 1)

	// Poll 1: IDLE — nothing happens
	mock.SetState("IDLE")
	pollBambu(t, bridge, "test-bambu", cfg)
	if len(spoolman.Updates()) != 0 {
		t.Error("expected no updates while IDLE")
	}

	// Poll 2: print starts
	mock.SetState("RUNNING")
	mock.SetProgress(0)
	mock.SetGcodeFile("bambu_test.gcode")
	pollBambu(t, bridge, "test-bambu", cfg)
	if len(spoolman.Updates()) != 0 {
		t.Error("expected no updates while RUNNING")
	}

	// Poll 3: mid-print
	mock.SetProgress(50)
	pollBambu(t, bridge, "test-bambu", cfg)

	// Poll 4: print finished
	mock.SetState("FINISH")
	mock.SetFilamentTotal(printWeight)
	pollBambu(t, bridge, "test-bambu", cfg)

	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("Spoolman was not updated after Bambu print finished")
	}
	if len(updates) > 1 {
		t.Errorf("Spoolman updated %d times — expected exactly 1", len(updates))
	}

	remaining := spoolman.RemainingWeight(spoolID)
	assertApproxWeight(t, "remaining weight", initialWeight-printWeight, remaining, 0.1)
	t.Logf("✅ Normal Bambu print: %.1fg used, %.1fg remaining", printWeight, remaining)
}

// ─── TestBambu_CancelledPrint ─────────────────────────────────────────────────

// TestBambu_CancelledPrint confirms that a cancelled print deducts a scaled
// amount (progress% × 0.95) rather than the full weight.
//
// Sequence: RUNNING(60%) → CANCEL
func TestBambu_CancelledPrint(t *testing.T) {
	const spoolID = 2
	const initialWeight = 800.0
	const fullWeight = 50.0
	const progressPct = 60

	bridge, mock, spoolman := setupBridgeWithBambuMock(t, map[int]float64{spoolID: initialWeight})

	if err := bridge.SetToolheadMapping("X1C Printer", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	cfg := bambuTestConfig("X1C Printer", 1)

	// Start print
	mock.SetState("RUNNING")
	mock.SetProgress(0)
	mock.SetGcodeFile("bambu_cancel.gcode")
	mock.SetFilamentTotal(fullWeight) // set total early — cancels use what's available
	pollBambu(t, bridge, "test-bambu", cfg)

	// Advance to 60% progress
	mock.SetProgress(progressPct)
	pollBambu(t, bridge, "test-bambu", cfg)

	// Cancel
	mock.SetState("CANCEL")
	pollBambu(t, bridge, "test-bambu", cfg)

	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("Spoolman was not updated after cancelled Bambu print")
	}

	scale := (float64(progressPct) / 100.0) * 0.95
	expected := initialWeight - (fullWeight * scale)
	remaining := spoolman.RemainingWeight(spoolID)
	// Allow 2g tolerance because of rounding in scale calculation
	diff := math.Abs(expected - remaining)
	if diff > 2.0 {
		t.Errorf("cancelled print: expected remaining ~%.2fg, got %.2fg (diff %.2fg)", expected, remaining, diff)
	}
	t.Logf("✅ Cancelled Bambu print: scale=%.3f, deducted ~%.2fg, remaining=%.2fg", scale, fullWeight*scale, remaining)
}

// ─── TestBambu_PauseResume ────────────────────────────────────────────────────

// TestBambu_PauseResume confirms that pausing does not trigger a premature
// deduction, and that the correct deduction happens only at FINISH.
//
// Sequence: RUNNING → PAUSE → RUNNING → FINISH
func TestBambu_PauseResume(t *testing.T) {
	const spoolID = 3
	const initialWeight = 600.0
	const printWeight = 22.0

	bridge, mock, spoolman := setupBridgeWithBambuMock(t, map[int]float64{spoolID: initialWeight})

	if err := bridge.SetToolheadMapping("X1C Printer", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	cfg := bambuTestConfig("X1C Printer", 1)

	// Start print
	mock.SetState("RUNNING")
	mock.SetGcodeFile("pause_test.gcode")
	pollBambu(t, bridge, "test-bambu", cfg)

	// Pause — no deduction expected
	mock.SetState("PAUSE")
	pollBambu(t, bridge, "test-bambu", cfg)
	if len(spoolman.Updates()) != 0 {
		t.Error("Spoolman was updated during PAUSE — should not happen")
	}

	// Resume
	mock.SetState("RUNNING")
	mock.SetProgress(80)
	pollBambu(t, bridge, "test-bambu", cfg)

	// Finish
	mock.SetState("FINISH")
	mock.SetFilamentTotal(printWeight)
	pollBambu(t, bridge, "test-bambu", cfg)

	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("Spoolman was not updated after pause-resume print finished")
	}
	if len(updates) > 1 {
		t.Errorf("Spoolman updated %d times — expected exactly 1", len(updates))
	}

	remaining := spoolman.RemainingWeight(spoolID)
	assertApproxWeight(t, "remaining weight after pause-resume", initialWeight-printWeight, remaining, 0.1)
	t.Logf("✅ Pause-resume Bambu print: deducted %.1fg correctly", printWeight)
}

// ─── TestBambu_NoStatusYet ────────────────────────────────────────────────────

// TestBambu_NoStatusYet confirms that the bridge handles the case where the
// MQTT client has connected but not yet received a status message gracefully:
// no crash, no Spoolman updates, state unchanged.
func TestBambu_NoStatusYet(t *testing.T) {
	const spoolID = 1
	bridge, mock, spoolman := setupBridgeWithBambuMock(t, map[int]float64{spoolID: 1000})

	cfg := bambuTestConfig("X1C Printer", 1)

	// Simulate "connected but no message received yet"
	mock.State.StatusUnavailable = true

	pollBambu(t, bridge, "test-bambu", cfg)

	if len(spoolman.Updates()) != 0 {
		t.Errorf("expected no Spoolman updates with no status yet, got %d", len(spoolman.Updates()))
	}
	t.Log("✅ No crash or spurious update when status not yet available")
}

// ─── TestBambu_ConnectionFailure ─────────────────────────────────────────────

// TestBambu_ConnectionFailure confirms that a Connect() error is handled
// gracefully: no crash, no Spoolman updates, state unchanged.
func TestBambu_ConnectionFailure(t *testing.T) {
	const spoolID = 1
	bridge, mock, spoolman := setupBridgeWithBambuMock(t, map[int]float64{spoolID: 1000})

	cfg := bambuTestConfig("X1C Printer", 1)
	mock.State.ConnectError = errors.New("connection refused (simulated)")

	pollBambu(t, bridge, "test-bambu", cfg)

	if len(spoolman.Updates()) != 0 {
		t.Errorf("expected no Spoolman updates after connection failure, got %d", len(spoolman.Updates()))
	}

	// On the next poll cycle the factory is called again (existing client was
	// never stored — Connect failed). Verify it doesn't panic.
	// Reset: clear the stored (nil) client so the factory re-runs.
	bridge.bambuMutex.Lock()
	delete(bridge.bambuClients, "test-bambu")
	bridge.bambuMutex.Unlock()

	mock.State.ConnectError = nil
	mock.SetState("IDLE")
	mock.State.StatusUnavailable = false
	pollBambu(t, bridge, "test-bambu", cfg)

	t.Log("✅ Connection failure handled gracefully; recovered on next poll")
}

// ─── TestBambu_AMS_MultiSlot ─────────────────────────────────────────────────

// TestBambu_AMS_MultiSlot confirms that when four AMS slots are active the
// total filament weight is distributed evenly across all four spools.
func TestBambu_AMS_MultiSlot(t *testing.T) {
	const totalWeight = 80.0
	const perSpool = totalWeight / 4

	spoolMap := map[int]float64{
		1: 1000, 2: 1000, 3: 1000, 4: 1000,
	}
	bridge, mock, spoolman := setupBridgeWithBambuMock(t, spoolMap)

	cfg := bambuTestConfig("X1C Printer", 4)
	printerName := cfg.Name

	// Map each AMS slot to a spool
	for slotIdx, spoolID := range []int{1, 2, 3, 4} {
		if err := bridge.SetToolheadMapping(printerName, slotIdx, spoolID); err != nil {
			t.Fatalf("SetToolheadMapping slot %d: %v", slotIdx, err)
		}
	}

	// Populate all four AMS slots
	for slotIdx := 0; slotIdx < 4; slotIdx++ {
		mock.SetAMSSlot(slotIdx, AMSSlotInfo{ID: slotIdx, Remain: 80, Type: "PLA", Color: "FFFFFFFF"})
	}

	// Start print
	mock.SetState("RUNNING")
	mock.SetGcodeFile("multicolor.gcode")
	pollBambu(t, bridge, "test-bambu", cfg)

	// Finish with 80g total
	mock.SetState("FINISH")
	mock.SetFilamentTotal(totalWeight)
	pollBambu(t, bridge, "test-bambu", cfg)

	// Each spool should have received ~20g deduction
	for _, spoolID := range []int{1, 2, 3, 4} {
		updates := spoolman.UpdatesForSpool(spoolID)
		if len(updates) == 0 {
			t.Errorf("spool %d was not updated", spoolID)
			continue
		}
		remaining := spoolman.RemainingWeight(spoolID)
		expected := 1000.0 - perSpool
		assertApproxWeight(t, "per-slot remaining", expected, remaining, 1.0)
	}
	t.Logf("✅ AMS multi-slot: %.1fg total distributed as ~%.1fg per slot", totalWeight, perSpool)
}

// ─── TestBambu_RapidPolling ───────────────────────────────────────────────────

// TestBambu_RapidPolling confirms that polling the FINISH state multiple times
// in a row results in exactly one Spoolman update (no double-count).
func TestBambu_RapidPolling(t *testing.T) {
	const spoolID = 1
	const initialWeight = 500.0
	const printWeight = 15.0

	bridge, mock, spoolman := setupBridgeWithBambuMock(t, map[int]float64{spoolID: initialWeight})

	if err := bridge.SetToolheadMapping("X1C Printer", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	cfg := bambuTestConfig("X1C Printer", 1)

	// Start print
	mock.SetState("RUNNING")
	mock.SetGcodeFile("rapid_poll.gcode")
	pollBambu(t, bridge, "test-bambu", cfg)

	// FINISH — first poll triggers processing
	mock.SetState("FINISH")
	mock.SetFilamentTotal(printWeight)
	pollBambu(t, bridge, "test-bambu", cfg)

	// Poll FINISH two more times — must NOT produce additional updates
	pollBambu(t, bridge, "test-bambu", cfg)
	pollBambu(t, bridge, "test-bambu", cfg)

	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) != 1 {
		t.Errorf("expected exactly 1 Spoolman update, got %d (double-count guard failed)", len(updates))
	}
	t.Log("✅ Rapid polling: single deduction even across multiple FINISH polls")
}

// ─── TestBambu_SpoolmanOffline ────────────────────────────────────────────────

// TestBambu_SpoolmanOffline confirms that when Spoolman is unreachable the
// print history is still written locally and the update is queued in the
// pending outbox for later retry.
func TestBambu_SpoolmanOffline(t *testing.T) {
	const spoolID = 1
	const initialWeight = 700.0
	const printWeight = 20.0

	bridge, mock, spoolman := setupBridgeWithBambuMock(t, map[int]float64{spoolID: initialWeight})

	if err := bridge.SetToolheadMapping("X1C Printer", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	cfg := bambuTestConfig("X1C Printer", 1)

	// Take Spoolman offline
	spoolman.SetOffline(true)

	// Run a full print cycle
	mock.SetState("RUNNING")
	mock.SetGcodeFile("offline_test.gcode")
	pollBambu(t, bridge, "test-bambu", cfg)

	mock.SetState("FINISH")
	mock.SetFilamentTotal(printWeight)
	pollBambu(t, bridge, "test-bambu", cfg)

	// Spoolman was offline so no updates landed
	if len(spoolman.Updates()) != 0 {
		t.Errorf("expected no Spoolman updates while offline, got %d", len(spoolman.Updates()))
	}

	// Verify the update was queued in the pending outbox
	rows, err := bridge.db.Query(`SELECT COUNT(*) FROM pending_spoolman_updates WHERE printer_name = ?`, cfg.Name)
	if err != nil {
		t.Fatalf("query pending_spoolman_updates: %v", err)
	}
	defer rows.Close()
	var count int
	if rows.Next() {
		rows.Scan(&count)
	}
	if count == 0 {
		t.Error("expected a pending Spoolman update in the outbox, got none")
	}

	// Verify print history was still written
	histRows, err := bridge.db.Query(`SELECT COUNT(*) FROM print_history WHERE printer_name = ?`, cfg.Name)
	if err != nil {
		t.Fatalf("query print_history: %v", err)
	}
	defer histRows.Close()
	var histCount int
	if histRows.Next() {
		histRows.Scan(&histCount)
	}
	if histCount == 0 {
		t.Error("expected a print_history row even when Spoolman is offline")
	}

	t.Logf("✅ Spoolman offline: history written (%d rows), %d updates queued", histCount, count)
}

// ─── TestBambu_NFC_AssignmentSnapshot ────────────────────────────────────────

// TestBambu_NFC_AssignmentSnapshot confirms that SnapshotAssignmentsForPrint
// is called after a Bambu print finishes, recording the spool-to-slot state
// in print_spool_events with event_type='start'.
func TestBambu_NFC_AssignmentSnapshot(t *testing.T) {
	const spoolID = 1
	const printWeight = 10.0

	bridge, mock, _ := setupBridgeWithBambuMock(t, map[int]float64{spoolID: 200})

	cfg := bambuTestConfig("X1C Printer", 1)

	// Create an active spool assignment for this slot
	if err := bridge.SetAssignment("test-bambu", 0, spoolID, "manual"); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	if err := bridge.SetToolheadMapping(cfg.Name, 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	// Run print
	mock.SetState("RUNNING")
	mock.SetGcodeFile("nfc_snap.gcode")
	pollBambu(t, bridge, "test-bambu", cfg)

	mock.SetState("FINISH")
	mock.SetFilamentTotal(printWeight)
	pollBambu(t, bridge, "test-bambu", cfg)

	// Verify at least one print history row was written
	var printID int
	row := bridge.db.QueryRow(`SELECT id FROM print_history WHERE printer_name = ? ORDER BY id DESC LIMIT 1`, cfg.Name)
	if err := row.Scan(&printID); err != nil {
		t.Fatalf("no print_history row found after Bambu print: %v", err)
	}

	// Verify print_spool_events has a 'start' row for this print
	var eventCount int
	err := bridge.db.QueryRow(
		`SELECT COUNT(*) FROM print_spool_events WHERE print_history_id = ? AND event_type = 'start'`,
		printID,
	).Scan(&eventCount)
	if err != nil {
		t.Fatalf("query print_spool_events: %v", err)
	}
	if eventCount == 0 {
		t.Errorf("expected print_spool_events 'start' row for print %d, got none", printID)
	}
	t.Logf("✅ NFC snapshot: print %d has %d start event(s) in print_spool_events", printID, eventCount)
}
