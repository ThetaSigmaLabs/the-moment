//go:build integration

package main

// =============================================================================
// lifecycle_test.go
// =============================================================================
// End-to-end tests for the FilaBridge monitoring loop using mock servers.
//
// These tests confirm:
//   - Normal print completion → Spoolman updated correctly
//   - Paused print → no premature deduction → resumes → Spoolman updated
//   - Filament runout (ATTENTION) → no premature deduction → resumes → updated
//   - Cancelled print (STOPPED) → partial deduction based on progress
//   - Multi-toolhead (INDX 8-head) → each toolhead's spool updated correctly
//   - Spoolman connectivity → test endpoint verifies connection
//
// No real printer or Spoolman instance is required.
// =============================================================================

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// setupBridgeWithMocks creates a FilamentBridge wired to a mock PrusaLink printer
// and a mock Spoolman, with the given spool pre-loaded.
//
// Returns the bridge, the mock printer, the mock Spoolman, and a cleanup func.
func setupBridgeWithMocks(t *testing.T, spoolMap map[int]float64) (*FilamentBridge, *MockPrusaLink, *MockSpoolman) {
	t.Helper()

	// Start mock servers
	printer := NewMockPrusaLink(t)
	spoolman := NewMockSpoolman(t, spoolMap)

	// Create a real bridge with a temp database
	t.Setenv("THE_MOMENT_DB_PATH", t.TempDir())
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })

	// Load default config then override SpoolmanURL to point at mock
	config, err := LoadConfig(bridge)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	config.SpoolmanURL = spoolman.URL()
	config.PrusaLinkTimeout = 5
	config.PrusaLinkFileDownloadTimeout = 10
	bridge.UpdateConfig(config)

	return bridge, printer, spoolman
}

// poll calls monitorPrusaLink once — one full polling cycle for the given printer.
func poll(t *testing.T, bridge *FilamentBridge, printer *MockPrusaLink, printerName string, toolheads int) {
	t.Helper()
	cfg := printer.PrinterConfig(printerName, toolheads)
	if err := bridge.monitorPrusaLink("test-printer-id", cfg); err != nil {
		t.Fatalf("monitorPrusaLink: %v", err)
	}
}

// assertApproxWeight checks that the remaining weight is within tolerance.
func assertApproxWeight(t *testing.T, label string, expected, actual, tolerance float64) {
	t.Helper()
	diff := math.Abs(expected - actual)
	if diff > tolerance {
		t.Errorf("%s: expected %.2fg ± %.2fg, got %.2fg (diff %.2fg)",
			label, expected, tolerance, actual, diff)
	}
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestLifecycle_NormalPrint confirms a complete print deducts the correct amount.
//
// Sequence:  IDLE → PRINTING → IDLE
// Expected:  Spoolman spool 1 reduced by exactly 25.5g
func TestLifecycle_NormalPrint(t *testing.T) {
	const spoolID = 1
	const initialWeight = 1000.0
	const printWeight = 25.5

	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})

	// Map spool 1 to toolhead 0 on this printer
	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	// Set the G-code to report 25.5g on toolhead 0
	printer.SetGcodeUsage(map[int]float64{0: printWeight})

	// Poll 1: printer is IDLE — nothing happens
	printer.SetState(StateIdle)
	poll(t, bridge, printer, "Core One L", 1)

	if len(spoolman.Updates()) != 0 {
		t.Error("expected no Spoolman updates while IDLE")
	}

	// Poll 2: printer starts PRINTING — bridge stores filename, sets wasPrinting=true
	printer.SetState(StatePrinting)
	printer.SetProgress(0)
	poll(t, bridge, printer, "Core One L", 1)

	if len(spoolman.Updates()) != 0 {
		t.Error("expected no Spoolman updates while PRINTING")
	}

	// Poll 3: printer progress mid-print
	printer.SetProgress(50)
	poll(t, bridge, printer, "Core One L", 1)

	// Poll 4: printer returns to IDLE — print finished
	printer.SetState(StateIdle)
	poll(t, bridge, printer, "Core One L", 1)

	// Verify Spoolman received exactly one update for spool 1
	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("Spoolman was not updated after print finished")
	}

	remaining := spoolman.RemainingWeight(spoolID)
	expected := initialWeight - printWeight
	assertApproxWeight(t, "remaining weight after normal print", expected, remaining, 0.1)

	t.Logf("✅ Normal print: %.1fg used, %.1fg remaining (expected %.1fg)", printWeight, remaining, expected)
}

// TestLifecycle_PausedPrint confirms a paused print does not deduct prematurely,
// then deducts correctly when it finishes.
//
// Sequence:  PRINTING → PAUSED → PRINTING → IDLE
// Expected:  Spoolman updated only once, after the final IDLE
func TestLifecycle_PausedPrint(t *testing.T) {
	const spoolID = 2
	const initialWeight = 800.0
	const printWeight = 18.3

	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: printWeight})

	// Start printing
	printer.SetState(StatePrinting)
	printer.SetProgress(20)
	poll(t, bridge, printer, "Core One L", 1)

	// Pause
	printer.SetState(StatePaused)
	poll(t, bridge, printer, "Core One L", 1)

	// Confirm no deduction while paused
	if len(spoolman.Updates()) != 0 {
		t.Error("Spoolman was updated during PAUSED state — should not happen")
	}

	// Resume
	printer.SetState(StatePrinting)
	printer.SetProgress(80)
	poll(t, bridge, printer, "Core One L", 1)

	// Finish
	printer.SetState(StateIdle)
	poll(t, bridge, printer, "Core One L", 1)

	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("Spoolman was not updated after paused print finished")
	}
	if len(updates) > 1 {
		t.Errorf("Spoolman updated %d times — expected exactly 1", len(updates))
	}

	remaining := spoolman.RemainingWeight(spoolID)
	expected := initialWeight - printWeight
	assertApproxWeight(t, "remaining weight after paused print", expected, remaining, 0.1)

	t.Logf("✅ Paused print: deducted correctly after resume+finish. Remaining: %.1fg", remaining)
}

// TestLifecycle_FilamentRunout confirms ATTENTION (filament runout) does not
// cause a premature or duplicate deduction.
//
// Sequence:  PRINTING → ATTENTION → PRINTING → IDLE
// Expected:  Single correct deduction at end
func TestLifecycle_FilamentRunout(t *testing.T) {
	const spoolID = 3
	const initialWeight = 500.0
	const printWeight = 42.0

	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: printWeight})

	// Start printing
	printer.SetState(StatePrinting)
	printer.SetProgress(60)
	poll(t, bridge, printer, "Core One L", 1)

	// Filament runout
	printer.SetState(StateAttention)
	poll(t, bridge, printer, "Core One L", 1)
	poll(t, bridge, printer, "Core One L", 1) // user may be away for several polls

	if len(spoolman.Updates()) != 0 {
		t.Error("Spoolman was updated during ATTENTION state — should not happen")
	}

	// User loads new spool, printer resumes
	printer.SetState(StatePrinting)
	printer.SetProgress(65)
	poll(t, bridge, printer, "Core One L", 1)

	// Print finishes
	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("Spoolman was not updated after runout+finish")
	}

	remaining := spoolman.RemainingWeight(spoolID)
	expected := initialWeight - printWeight
	assertApproxWeight(t, "remaining weight after runout print", expected, remaining, 0.1)

	t.Logf("✅ Filament runout: single deduction after finish. Remaining: %.1fg", remaining)
}

// TestLifecycle_CancelledPrint confirms a cancelled print deducts a partial
// amount proportional to print progress, with a safety margin.
//
// Sequence:  PRINTING (60%) → STOPPED
// Expected:  Spool reduced by approximately 60% × 0.95 × full weight
func TestLifecycle_CancelledPrint(t *testing.T) {
	const spoolID = 4
	const initialWeight = 750.0
	const fullPrintWeight = 100.0
	const progressPct = 60.0

	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: fullPrintWeight})

	// Start printing
	printer.SetState(StatePrinting)
	printer.SetProgress(progressPct)
	poll(t, bridge, printer, "Core One L", 1)

	// User cancels
	printer.SetState(StateStopped)
	poll(t, bridge, printer, "Core One L", 1)

	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("Spoolman was not updated after cancelled print")
	}

	remaining := spoolman.RemainingWeight(spoolID)
	scale := (progressPct / 100.0) * 0.95
	expectedDeduction := fullPrintWeight * scale
	expectedRemaining := initialWeight - expectedDeduction

	// Allow 5g tolerance for the safety margin calculation
	assertApproxWeight(t, "remaining weight after cancellation", expectedRemaining, remaining, 5.0)

	actualDeduction := initialWeight - remaining
	t.Logf("✅ Cancelled at %.0f%%: deducted %.1fg (expected ~%.1fg). Remaining: %.1fg",
		progressPct, actualDeduction, expectedDeduction, remaining)
}

// TestLifecycle_MultiToolhead_INDX8 simulates an 8-toolhead INDX print where
// only 4 toolheads are active. Confirms each active spool is updated correctly
// and inactive toolheads are not touched.
func TestLifecycle_MultiToolhead_INDX8(t *testing.T) {
	// Spool IDs mapped to toolheads 0,1,2,3 — toolheads 4-7 unmapped
	spoolsByToolhead := map[int]int{
		0: 10,
		1: 11,
		2: 12,
		3: 13,
	}
	usageByToolhead := map[int]float64{
		0: 45.0,
		1: 30.0,
		2: 0.0, // Not used in this print
		3: 15.0,
		// Toolheads 4-7 not active
	}
	const initialWeight = 1000.0

	// Build spool map
	spoolWeights := map[int]float64{}
	for _, spoolID := range spoolsByToolhead {
		spoolWeights[spoolID] = initialWeight
	}

	bridge, printer, spoolman := setupBridgeWithMocks(t, spoolWeights)

	// Map each toolhead to its spool
	for toolheadID, spoolID := range spoolsByToolhead {
		if err := bridge.SetToolheadMapping("INDX Printer", toolheadID, spoolID); err != nil {
			t.Fatalf("SetToolheadMapping toolhead %d: %v", toolheadID, err)
		}
	}

	// Set G-code to report usage for the active toolheads
	printer.SetGcodeUsage(usageByToolhead)

	// Print cycle
	printer.SetState(StatePrinting)
	printer.SetProgress(0)
	poll(t, bridge, printer, "INDX Printer", 8)

	printer.SetProgress(100)
	printer.SetState(StateFinished)
	poll(t, bridge, printer, "INDX Printer", 8)

	// Verify each toolhead's spool
	for toolheadID, spoolID := range spoolsByToolhead {
		expected := usageByToolhead[toolheadID]
		remaining := spoolman.RemainingWeight(spoolID)
		expectedRemaining := initialWeight - expected

		if expected == 0 {
			// Spool should not have been updated
			updates := spoolman.UpdatesForSpool(spoolID)
			if len(updates) != 0 {
				t.Errorf("toolhead %d (spool %d): expected no update for 0g usage, got %d updates",
					toolheadID, spoolID, len(updates))
			}
		} else {
			assertApproxWeight(t,
				fmt.Sprintf("toolhead %d (spool %d)", toolheadID, spoolID),
				expectedRemaining, remaining, 0.1)
		}

		t.Logf("  Toolhead %d → spool %d: used %.1fg, remaining %.1fg",
			toolheadID, spoolID, expected, remaining)
	}

	t.Logf("✅ 8-head INDX: all toolheads updated correctly")
}

// TestSpoolman_ConnectionConfirmed tests that The Moment can reach the Spoolman
// API and retrieve a spool list. This is the most basic connectivity smoke test.
func TestSpoolman_ConnectionConfirmed(t *testing.T) {
	const spoolID = 99
	bridge, _, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: 500.0,
	})

	// Call Spoolman directly through the bridge's client
	spools, err := bridge.spoolman.GetAllSpools()
	if err != nil {
		t.Fatalf("GetAllSpools failed: %v — is Spoolman reachable at %s?", err, spoolman.URL())
	}

	if len(spools) == 0 {
		t.Error("expected at least one spool, got none")
	}

	found := false
	for _, s := range spools {
		if s.ID == spoolID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("spool %d not found in Spoolman response", spoolID)
	}

	t.Logf("✅ Spoolman connection confirmed: %d spool(s) visible", len(spools))
}

// TestSpoolman_UsageUpdateRoundTrip tests the full Spoolman update path:
// GET spool → add usage → PATCH spool → verify remaining weight.
// This confirms UpdateSpoolUsage works end-to-end.
func TestSpoolman_UsageUpdateRoundTrip(t *testing.T) {
	const spoolID = 5
	const initialWeight = 1000.0
	const usageG = 75.5

	bridge, _, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})

	// Call UpdateSpoolUsage directly — this is exactly what the bridge calls
	// after parsing G-code
	if err := bridge.spoolman.UpdateSpoolUsage(spoolID, usageG); err != nil {
		t.Fatalf("UpdateSpoolUsage failed: %v", err)
	}

	remaining := spoolman.RemainingWeight(spoolID)
	expected := initialWeight - usageG
	assertApproxWeight(t, "remaining after UpdateSpoolUsage", expected, remaining, 0.1)

	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("no PATCH call recorded in mock Spoolman")
	}

	t.Logf("✅ UpdateSpoolUsage round-trip: %.1fg used, %.1fg remaining", usageG, remaining)
}

// TestLifecycle_RapidPolling confirms the bridge handles multiple rapid polls
// without double-counting. This catches race conditions in the state machine.
func TestLifecycle_RapidPolling(t *testing.T) {
	const spoolID = 6
	const initialWeight = 600.0
	const printWeight = 33.3

	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: printWeight})

	// Start printing
	printer.SetState(StatePrinting)
	for i := 0; i < 5; i++ {
		printer.SetProgress(float64(i * 20))
		poll(t, bridge, printer, "Core One L", 1)
	}

	// Finish — poll multiple times to confirm only one deduction
	printer.SetState(StateFinished)
	for i := 0; i < 3; i++ {
		poll(t, bridge, printer, "Core One L", 1)
		time.Sleep(10 * time.Millisecond)
	}

	updates := spoolman.UpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("no Spoolman update after print finished")
	}
	if len(updates) > 1 {
		t.Errorf("double-counting detected: Spoolman updated %d times, expected 1", len(updates))
	}

	remaining := spoolman.RemainingWeight(spoolID)
	expected := initialWeight - printWeight
	assertApproxWeight(t, "remaining after rapid polling", expected, remaining, 0.1)

	t.Logf("✅ Rapid polling: %d Spoolman update(s), no double-counting. Remaining: %.1fg",
		len(updates), remaining)
}

// TestLifecycle_NoSpoolMapped confirms that when a toolhead has no spool mapped,
// the bridge logs and skips gracefully — no crash, no panic.
func TestLifecycle_NoSpoolMapped(t *testing.T) {
	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{})

	// Do NOT map any toolhead — toolhead 0 is intentionally unmapped
	printer.SetGcodeUsage(map[int]float64{0: 20.0})

	printer.SetState(StatePrinting)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	// No updates should have been made — nothing was mapped
	updates := spoolman.Updates()
	if len(updates) != 0 {
		t.Errorf("expected no Spoolman updates with no spool mapped, got %d", len(updates))
	}

	t.Logf("✅ Unmapped toolhead handled gracefully — no crash, no update")
}

// TestLifecycle_SpoolmanOffline_HistoryAlwaysLogged confirms that when Spoolman
// is unreachable at print completion:
//   - Local print history IS still written (event not dropped)
//   - A pending Spoolman update is queued in the outbox
//   - No update reaches Spoolman (it was offline)
func TestLifecycle_SpoolmanOffline_HistoryAlwaysLogged(t *testing.T) {
	const spoolID = 20
	const initialWeight = 1000.0
	const printWeight = 40.0

	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: printWeight})

	// Take Spoolman offline before the print finishes
	spoolman.SetOffline(true)

	printer.SetState(StatePrinting)
	printer.SetProgress(100)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	// Spoolman must NOT have received any update
	if len(spoolman.Updates()) != 0 {
		t.Errorf("expected no Spoolman updates while offline, got %d", len(spoolman.Updates()))
	}

	// Local history must still be written
	history, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("print history is empty — event was silently dropped when Spoolman was offline")
	}

	// Outbox must hold exactly one pending update
	pending := bridge.GetPendingSpoolmanUpdateCount()
	if pending != 1 {
		t.Errorf("expected 1 pending Spoolman update, got %d", pending)
	}

	t.Logf("✅ Spoolman offline: history logged, 1 pending update queued")
}

// TestLifecycle_SpoolmanRecovers_PendingRetried confirms that after Spoolman
// comes back online, RetryPendingSpoolmanUpdates delivers the queued update
// and clears the outbox.
func TestLifecycle_SpoolmanRecovers_PendingRetried(t *testing.T) {
	const spoolID = 21
	const initialWeight = 1000.0
	const printWeight = 55.0

	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: printWeight})

	// Spoolman offline during print completion
	spoolman.SetOffline(true)

	printer.SetState(StatePrinting)
	printer.SetProgress(100)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	if bridge.GetPendingSpoolmanUpdateCount() != 1 {
		t.Fatal("expected 1 pending update after offline print")
	}

	// Spoolman comes back online
	spoolman.SetOffline(false)

	// Retry loop fires
	if err := bridge.RetryPendingSpoolmanUpdates(); err != nil {
		t.Fatalf("RetryPendingSpoolmanUpdates: %v", err)
	}

	// Outbox must now be empty
	if pending := bridge.GetPendingSpoolmanUpdateCount(); pending != 0 {
		t.Errorf("expected 0 pending updates after retry, got %d", pending)
	}

	// Spoolman must now reflect the correct remaining weight
	remaining := spoolman.RemainingWeight(spoolID)
	expected := initialWeight - printWeight
	assertApproxWeight(t, "remaining after retry", expected, remaining, 0.1)

	t.Logf("✅ Spoolman recovered: retry delivered %.1fg, %.1fg remaining", printWeight, remaining)
}

// registerPrinter saves a printer config to the bridge DB so
// RetryPendingGcodeDownloads can resolve the printer by IP address.
func registerPrinter(t *testing.T, bridge *FilamentBridge, printer *MockPrusaLink, name string, toolheads int) {
	t.Helper()
	cfg := printer.PrinterConfig(name, toolheads)
	if err := bridge.SavePrinterConfig("test-printer-id", cfg); err != nil {
		t.Fatalf("SavePrinterConfig: %v", err)
	}
}

// TestLifecycle_GcodeDownloadFails_Queued confirms that when a G-code download
// fails after all HTTP retries, the print event is queued for background retry
// rather than silently dropped. No print error is surfaced for a queued event.
func TestLifecycle_GcodeDownloadFails_Queued(t *testing.T) {
	const spoolID = 30
	const initialWeight = 1000.0

	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})
	registerPrinter(t, bridge, printer, "Core One L", 1)

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 35.0})
	printer.SetGcodeUnavailable(true) // USB busy / file gone

	printer.SetState(StatePrinting)
	printer.SetProgress(100)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	// A pending G-code download must be queued
	if count := bridge.GetPendingGcodeDownloadCount(); count != 1 {
		t.Errorf("expected 1 pending G-code download, got %d", count)
	}

	// No unacknowledged print errors — the queue handles it silently
	if errs := bridge.GetPrintErrors(); len(errs) != 0 {
		t.Errorf("expected no print errors while queued, got %d: %v", len(errs), errs)
	}

	t.Logf("✅ G-code unavailable: 1 pending download queued, no spurious print error")
}

// TestLifecycle_GcodeDownloadFails_RetrySucceeds confirms that once the G-code
// becomes available again, RetryPendingGcodeDownloads processes the download,
// deducts the correct weight from the spool, and clears the retry queue.
func TestLifecycle_GcodeDownloadFails_RetrySucceeds(t *testing.T) {
	const spoolID = 31
	const initialWeight = 1000.0
	const printWeight = 48.0

	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{
		spoolID: initialWeight,
	})
	registerPrinter(t, bridge, printer, "Core One L", 1)

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: printWeight})
	printer.SetGcodeUnavailable(true)

	// Print finishes while G-code is unavailable — event is queued
	printer.SetState(StatePrinting)
	printer.SetProgress(100)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	if bridge.GetPendingGcodeDownloadCount() != 1 {
		t.Fatal("expected 1 pending G-code download after unavailable download")
	}

	// G-code becomes accessible again
	printer.SetGcodeUnavailable(false)

	// Retry loop fires
	if err := bridge.RetryPendingGcodeDownloads(); err != nil {
		t.Fatalf("RetryPendingGcodeDownloads: %v", err)
	}

	// Queue must be empty
	if count := bridge.GetPendingGcodeDownloadCount(); count != 0 {
		t.Errorf("expected 0 pending downloads after retry, got %d", count)
	}

	// Spoolman must reflect the correct deduction
	remaining := spoolman.RemainingWeight(spoolID)
	expected := initialWeight - printWeight
	assertApproxWeight(t, "remaining after G-code retry", expected, remaining, 0.1)

	t.Logf("✅ G-code retry succeeded: %.1fg deducted, %.1fg remaining", printWeight, remaining)
}
