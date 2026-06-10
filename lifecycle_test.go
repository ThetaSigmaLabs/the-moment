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
	"net/http"
	"net/http/httptest"
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
	if err := bridge.SavePrinterConfig("test-printer-id", cfg); err != nil {
		t.Fatalf("SavePrinterConfig: %v", err)
	}
	if err := bridge.monitorPrusaLink("test-printer-id", cfg); err != nil {
		t.Fatalf("monitorPrusaLink: %v", err)
	}
}

// pollWithCamera is like poll but injects a CameraSnapshotURL into the printer config.
func pollWithCamera(t *testing.T, bridge *FilamentBridge, printer *MockPrusaLink, printerName string, toolheads int, cameraURL string) {
	t.Helper()
	cfg := printer.PrinterConfig(printerName, toolheads)
	cfg.CameraSnapshotURL = cameraURL
	if err := bridge.SavePrinterConfig("test-printer-id", cfg); err != nil {
		t.Fatalf("SavePrinterConfig: %v", err)
	}
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

// minimalJPEG is the smallest valid JPEG (1×1 black pixel) for use in snapshot tests.
var minimalJPEG = []byte{
	0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
	0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xDB, 0x00, 0x43,
	0x00, 0x08, 0x06, 0x06, 0x07, 0x06, 0x05, 0x08, 0x07, 0x07, 0x07, 0x09,
	0x09, 0x08, 0x0A, 0x0C, 0x14, 0x0D, 0x0C, 0x0B, 0x0B, 0x0C, 0x19, 0x12,
	0x13, 0x0F, 0x14, 0x1D, 0x1A, 0x1F, 0x1E, 0x1D, 0x1A, 0x1C, 0x1C, 0x20,
	0x24, 0x2E, 0x27, 0x20, 0x22, 0x2C, 0x23, 0x1C, 0x1C, 0x28, 0x37, 0x29,
	0x2C, 0x30, 0x31, 0x34, 0x34, 0x34, 0x1F, 0x27, 0x39, 0x3D, 0x38, 0x32,
	0x3C, 0x2E, 0x33, 0x34, 0x32, 0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01,
	0x00, 0x01, 0x01, 0x01, 0x11, 0x00, 0xFF, 0xC4, 0x00, 0x1F, 0x00, 0x00,
	0x01, 0x05, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	0x09, 0x0A, 0x0B, 0xFF, 0xC4, 0x00, 0xB5, 0x10, 0x00, 0x02, 0x01, 0x03,
	0x03, 0x02, 0x04, 0x03, 0x05, 0x05, 0x04, 0x04, 0x00, 0x00, 0x01, 0x7D,
	0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00, 0xFB, 0xFF,
	0xD9,
}

// TestLifecycle_SnapshotCaptured_OnFinish verifies that a camera snapshot is saved
// as a "camera" attachment when a PrusaLink print completes normally.
func TestLifecycle_SnapshotCaptured_OnFinish(t *testing.T) {
	const spoolID = 40
	const initialWeight = 500.0
	const printWeight = 20.0

	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: initialWeight})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: printWeight})
	printer.SetCameraSnapshot(minimalJPEG)

	printer.SetState(StatePrinting)
	printer.SetProgress(50)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	// Snapshot endpoint must have been called once (for the "finished" snapshot)
	if n := printer.SnapshotCallCount(); n != 1 {
		t.Errorf("expected 1 snapshot call, got %d", n)
	}

	// Print history must exist and have a camera attachment
	history, err := bridge.GetPrintHistory(5)
	if err != nil || len(history) == 0 {
		t.Fatalf("GetPrintHistory: %v (len=%d)", err, len(history))
	}
	printID := history[0].ID
	attachments, err := bridge.GetPrintAttachments(printID)
	if err != nil {
		t.Fatalf("GetPrintAttachments: %v", err)
	}
	var cameraCount int
	for _, a := range attachments {
		if a.FileType == "camera" {
			cameraCount++
		}
	}
	if cameraCount != 1 {
		t.Errorf("expected 1 camera attachment, got %d (all: %v)", cameraCount, attachments)
	}

	t.Logf("✅ Snapshot captured on finish: %d camera attachment(s)", cameraCount)
}

// TestLifecycle_SnapshotCaptured_OnCancel verifies that a snapshot is saved when a print
// is cancelled mid-way through.
func TestLifecycle_SnapshotCaptured_OnCancel(t *testing.T) {
	const spoolID = 41
	const initialWeight = 500.0

	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: initialWeight})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 50.0})
	printer.SetCameraSnapshot(minimalJPEG)

	printer.SetState(StatePrinting)
	printer.SetProgress(60)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateStopped)
	poll(t, bridge, printer, "Core One L", 1)

	if n := printer.SnapshotCallCount(); n != 1 {
		t.Errorf("expected 1 snapshot call on cancel, got %d", n)
	}

	history, err := bridge.GetPrintHistory(5)
	if err != nil || len(history) == 0 {
		t.Fatalf("GetPrintHistory: %v (len=%d)", err, len(history))
	}
	attachments, err := bridge.GetPrintAttachments(history[0].ID)
	if err != nil {
		t.Fatalf("GetPrintAttachments: %v", err)
	}
	var cameraCount int
	for _, a := range attachments {
		if a.FileType == "camera" {
			cameraCount++
		}
	}
	if cameraCount != 1 {
		t.Errorf("expected 1 camera attachment on cancel, got %d", cameraCount)
	}

	t.Logf("✅ Snapshot captured on cancel: %d camera attachment(s)", cameraCount)
}

// TestLifecycle_SnapshotNoCamera verifies graceful no-op when the printer has no camera.
func TestLifecycle_SnapshotNoCamera(t *testing.T) {
	const spoolID = 42
	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 15.0})
	// No SetCameraSnapshot call — camera is nil (404)

	printer.SetState(StatePrinting)
	printer.SetProgress(100)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	// No snapshot calls, no crash
	if n := printer.SnapshotCallCount(); n != 0 {
		t.Errorf("expected 0 snapshot calls with no camera, got %d", n)
	}

	history, err := bridge.GetPrintHistory(5)
	if err != nil || len(history) == 0 {
		t.Fatalf("GetPrintHistory: %v (len=%d)", err, len(history))
	}
	attachments, err := bridge.GetPrintAttachments(history[0].ID)
	if err != nil {
		t.Fatalf("GetPrintAttachments: %v", err)
	}
	for _, a := range attachments {
		if a.FileType == "camera" {
			t.Errorf("unexpected camera attachment when printer has no camera: %+v", a)
		}
	}

	t.Logf("✅ No camera — no snapshot, no error")
}

// TestLifecycle_AttentionSnapshot verifies that snapshots captured at filament runout
// (ATTENTION), on resume, and at print completion are all attached to the record.
func TestLifecycle_AttentionSnapshot(t *testing.T) {
	const spoolID = 43
	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 30.0})
	printer.SetCameraSnapshot(minimalJPEG)

	// Start printing
	printer.SetState(StatePrinting)
	printer.SetProgress(40)
	poll(t, bridge, printer, "Core One L", 1)

	// Filament runout — should capture attention snapshot
	printer.SetState(StateAttention)
	poll(t, bridge, printer, "Core One L", 1)

	// Resume and finish — should capture finished snapshot
	printer.SetState(StatePrinting)
	printer.SetProgress(80)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	// Should have 3 snapshot calls: attention, resume, and finish
	if n := printer.SnapshotCallCount(); n != 3 {
		t.Errorf("expected 3 snapshot calls (attention + resume + finish), got %d", n)
	}

	history, err := bridge.GetPrintHistory(5)
	if err != nil || len(history) == 0 {
		t.Fatalf("GetPrintHistory: %v (len=%d)", err, len(history))
	}
	// Snapshots go to firstPrintID (oldest record in the session).
	// GetPrintHistory returns newest-first so scan all entries.
	var cameraCount int
	for _, hr := range history {
		attachments, aErr := bridge.GetPrintAttachments(hr.ID)
		if aErr != nil {
			t.Fatalf("GetPrintAttachments(id=%d): %v", hr.ID, aErr)
		}
		for _, a := range attachments {
			if a.FileType == "camera" {
				cameraCount++
			}
		}
	}
	if cameraCount != 3 {
		t.Errorf("expected 3 camera attachments (attention + resume + finish), got %d", cameraCount)
	}

	t.Logf("✅ Attention + resume + finish snapshots: %d camera attachment(s)", cameraCount)
}

// TestLifecycle_SnapshotCaptured_FromConfiguredHTTPURL verifies that when a
// CameraSnapshotURL is configured, snapshots are fetched from that URL rather
// than the PrusaLink /api/v1/cameras endpoint.
func TestLifecycle_SnapshotCaptured_FromConfiguredHTTPURL(t *testing.T) {
	const spoolID = 50
	const printWeight = 15.0

	// Stand up a tiny HTTP server that returns a JPEG on any GET request.
	var httpHits int
	cameraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHits++
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(minimalJPEG) //nolint:errcheck
	}))
	t.Cleanup(cameraServer.Close)

	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: printWeight})
	// No SetCameraSnapshot — PrusaLink camera API returns 404; snapshot must come from URL.

	cameraURL := cameraServer.URL + "/snapshot.jpg"

	printer.SetState(StatePrinting)
	printer.SetProgress(50)
	pollWithCamera(t, bridge, printer, "Core One L", 1, cameraURL)

	printer.SetState(StateFinished)
	pollWithCamera(t, bridge, printer, "Core One L", 1, cameraURL)

	// Configured HTTP URL must have been called (not the PrusaLink camera API).
	if httpHits == 0 {
		t.Error("expected at least one hit on configured camera URL, got 0")
	}
	if n := printer.SnapshotCallCount(); n != 0 {
		t.Errorf("PrusaLink /api/v1/cameras should not have been called when URL is configured, got %d call(s)", n)
	}

	// A camera attachment must exist on the finished print record.
	history, err := bridge.GetPrintHistory(5)
	if err != nil || len(history) == 0 {
		t.Fatalf("GetPrintHistory: %v (len=%d)", err, len(history))
	}
	var cameraCount int
	for _, hr := range history {
		atts, _ := bridge.GetPrintAttachments(hr.ID)
		for _, a := range atts {
			if a.FileType == "camera" {
				cameraCount++
			}
		}
	}
	if cameraCount == 0 {
		t.Error("expected at least one camera attachment, got 0")
	}
	t.Logf("✅ Configured HTTP URL: %d snapshot(s) captured, %d HTTP hit(s)", cameraCount, httpHits)
}

// TestLifecycle_SnapshotBlankURLDisables verifies that leaving CameraSnapshotURL
// empty produces no snapshots and no errors (same as no camera configured).
func TestLifecycle_SnapshotBlankURLDisables(t *testing.T) {
	const spoolID = 51

	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 10.0})
	// No camera, no URL — both are absent.

	printer.SetState(StatePrinting)
	printer.SetProgress(50)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateFinished)
	poll(t, bridge, printer, "Core One L", 1)

	if n := printer.SnapshotCallCount(); n != 0 {
		t.Errorf("expected 0 snapshot calls, got %d", n)
	}
	history, err := bridge.GetPrintHistory(5)
	if err != nil || len(history) == 0 {
		t.Fatalf("GetPrintHistory: %v (len=%d)", err, len(history))
	}
	for _, hr := range history {
		atts, _ := bridge.GetPrintAttachments(hr.ID)
		for _, a := range atts {
			if a.FileType == "camera" {
				t.Errorf("unexpected camera attachment on print %d: %s", hr.ID, a.Filename)
			}
		}
	}
	t.Logf("✅ Blank URL: no snapshots, no errors")
}

// pollWithSnapshot is like pollWithCamera but also sets a ProgressSnapshotConfig.
func pollWithSnapshot(t *testing.T, bridge *FilamentBridge, printer *MockPrusaLink, printerName string, toolheads int, cameraURL string, psc ProgressSnapshotConfig) {
	t.Helper()
	cfg := printer.PrinterConfig(printerName, toolheads)
	cfg.CameraSnapshotURL = cameraURL
	cfg.ProgressSnapshotConfig = psc
	if err := bridge.SavePrinterConfig("test-printer-id", cfg); err != nil {
		t.Fatalf("SavePrinterConfig: %v", err)
	}
	if err := bridge.monitorPrusaLink("test-printer-id", cfg); err != nil {
		t.Fatalf("monitorPrusaLink: %v", err)
	}
}

// countPendingSnapshots returns the number of rows in pending_print_snapshots for the printer.
func countPendingSnapshots(t *testing.T, bridge *FilamentBridge, printerID string) int {
	t.Helper()
	var n int
	bridge.db.QueryRow(`SELECT COUNT(*) FROM pending_print_snapshots WHERE printer_id = ?`, printerID).Scan(&n)
	return n
}

// lastSnapshotProgressDB returns the last_snapshot_progress for the active session.
func lastSnapshotProgressDB(t *testing.T, bridge *FilamentBridge, printerID string, jobID int) float64 {
	t.Helper()
	session, err := bridge.GetActivePrintSession(printerID, jobID)
	if err != nil || session == nil {
		t.Fatalf("GetActivePrintSession(%s, %d): %v", printerID, jobID, err)
	}
	return session.LastSnapshotProgress
}

// TestProgressSnapshot_IntervalMode verifies that progress snapshots fire at the
// correct thresholds in interval mode, and that last_snapshot_progress is updated.
//
// Sequence: PRINTING at 5% → 15% → 25%
// With interval=10: snapshots should be captured at 10% and 20% (two crossings).
func TestProgressSnapshot_IntervalMode(t *testing.T) {
	const spoolID = 60
	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())
	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 20.0})
	printer.SetCameraSnapshot(minimalJPEG)

	psc := ProgressSnapshotConfig{Mode: "interval", Interval: 10}

	// Poll at 5% — no threshold crossed yet
	printer.SetState(StatePrinting)
	printer.SetProgress(5)
	printer.SetJobID(200)
	pollWithSnapshot(t, bridge, printer, "Core One L", 1, "", psc)
	if n := countPendingSnapshots(t, bridge, "test-printer-id"); n != 0 {
		t.Errorf("at 5%%: expected 0 pending snapshots, got %d", n)
	}

	// Poll at 15% — crosses 10%
	printer.SetProgress(15)
	pollWithSnapshot(t, bridge, printer, "Core One L", 1, "", psc)
	if n := countPendingSnapshots(t, bridge, "test-printer-id"); n != 1 {
		t.Errorf("at 15%%: expected 1 pending snapshot (10%%), got %d", n)
	}
	if lsp := lastSnapshotProgressDB(t, bridge, "test-printer-id", 200); lsp != 10 {
		t.Errorf("last_snapshot_progress: want 10, got %.1f", lsp)
	}

	// Poll at 25% — crosses 20%
	printer.SetProgress(25)
	pollWithSnapshot(t, bridge, printer, "Core One L", 1, "", psc)
	if n := countPendingSnapshots(t, bridge, "test-printer-id"); n != 2 {
		t.Errorf("at 25%%: expected 2 pending snapshots (10%%, 20%%), got %d", n)
	}
	if lsp := lastSnapshotProgressDB(t, bridge, "test-printer-id", 200); lsp != 20 {
		t.Errorf("last_snapshot_progress: want 20, got %.1f", lsp)
	}

	t.Logf("✅ Progress snapshots: 2 captured at 10%% and 20%% thresholds")
}

// TestProgressSnapshot_NoDuplicates ensures polling at the same progress value
// twice does not produce duplicate snapshots.
func TestProgressSnapshot_NoDuplicates(t *testing.T) {
	const spoolID = 61
	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())
	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 20.0})
	printer.SetCameraSnapshot(minimalJPEG)
	printer.SetJobID(201)

	psc := ProgressSnapshotConfig{Mode: "interval", Interval: 25}

	printer.SetState(StatePrinting)
	printer.SetProgress(30) // crosses 25%
	pollWithSnapshot(t, bridge, printer, "Core One L", 1, "", psc)
	if n := countPendingSnapshots(t, bridge, "test-printer-id"); n != 1 {
		t.Errorf("first poll at 30%%: expected 1 snapshot, got %d", n)
	}

	// Poll again at same progress — no new snapshot
	pollWithSnapshot(t, bridge, printer, "Core One L", 1, "", psc)
	if n := countPendingSnapshots(t, bridge, "test-printer-id"); n != 1 {
		t.Errorf("second poll at 30%%: expected still 1 snapshot, got %d", n)
	}

	t.Logf("✅ No duplicate progress snapshots on repeated polls at same progress")
}

// TestProgressSnapshot_NoCameraNoOp confirms that progress snapshots produce
// no errors and no pending rows when no camera is configured.
func TestProgressSnapshot_NoCameraNoOp(t *testing.T) {
	const spoolID = 62
	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())
	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 20.0})
	printer.SetJobID(202)
	// No SetCameraSnapshot — PrusaLink camera API returns empty list

	psc := ProgressSnapshotConfig{Mode: "milestones", Milestones: []float64{25, 50, 75}}

	printer.SetState(StatePrinting)
	printer.SetProgress(80) // would cross 25, 50, 75
	pollWithSnapshot(t, bridge, printer, "Core One L", 1, "", psc)

	if n := countPendingSnapshots(t, bridge, "test-printer-id"); n != 0 {
		t.Errorf("no camera: expected 0 pending snapshots, got %d", n)
	}
	t.Logf("✅ No camera: no snapshots, no errors")
}

// TestProgressSnapshot_FlushLabel confirms that pending progress snapshots flushed
// to print_attachments carry the correct label.
func TestProgressSnapshot_FlushLabel(t *testing.T) {
	const spoolID = 63
	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())
	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 20.0})
	printer.SetCameraSnapshot(minimalJPEG)
	printer.SetJobID(203)

	psc := ProgressSnapshotConfig{Mode: "milestones", Milestones: []float64{50}}

	printer.SetState(StatePrinting)
	printer.SetProgress(60) // crosses 50%
	pollWithSnapshot(t, bridge, printer, "Core One L", 1, "", psc)

	if n := countPendingSnapshots(t, bridge, "test-printer-id"); n != 1 {
		t.Fatalf("expected 1 pending snapshot, got %d", n)
	}

	// Complete the print — pending snapshot should be flushed to print_attachments
	printer.SetState(StateFinished)
	pollWithSnapshot(t, bridge, printer, "Core One L", 1, "", psc)

	history, err := bridge.GetPrintHistory(5)
	if err != nil || len(history) == 0 {
		t.Fatalf("GetPrintHistory: %v", err)
	}
	attachments, err := bridge.GetPrintAttachments(history[0].ID)
	if err != nil {
		t.Fatalf("GetPrintAttachments: %v", err)
	}

	var found bool
	for _, a := range attachments {
		if a.FileType == "camera" && a.Label == "50% progress" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected camera attachment with label '50%% progress', got: %v", attachments)
	}
	t.Logf("✅ Progress snapshot label '50%% progress' correctly flushed to print_attachments")
}

// TestInterruptSnapshot_AttentionLabel verifies that an attention snapshot is
// stored as a pending snapshot with a progress-annotated label.
func TestInterruptSnapshot_AttentionLabel(t *testing.T) {
	const spoolID = 64
	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())
	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 50.0})
	printer.SetCameraSnapshot(minimalJPEG)
	printer.SetJobID(204)

	printer.SetState(StatePrinting)
	printer.SetProgress(42)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateAttention)
	poll(t, bridge, printer, "Core One L", 1)

	// Check that a pending snapshot exists with the attention label
	var label string
	bridge.db.QueryRow(
		`SELECT COALESCE(label,'') FROM pending_print_snapshots WHERE printer_id = ? AND event_type = 'attention'`,
		"test-printer-id",
	).Scan(&label)

	if label != "Attention @ 42%" {
		t.Errorf("attention snapshot label: want 'Attention @ 42%%', got %q", label)
	}
	t.Logf("✅ Attention snapshot label: %q", label)
}

// TestInterruptSnapshot_ResumeLabel verifies that a resume snapshot fires when
// the state transitions from StateAttention back to StatePrinting.
func TestInterruptSnapshot_ResumeLabel(t *testing.T) {
	const spoolID = 65
	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())
	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 50.0})
	printer.SetCameraSnapshot(minimalJPEG)
	printer.SetJobID(205)

	printer.SetState(StatePrinting)
	printer.SetProgress(42)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateAttention)
	poll(t, bridge, printer, "Core One L", 1)

	// Resume: state goes back to PRINTING
	printer.SetState(StatePrinting)
	printer.SetProgress(44) // slightly advanced after spool swap
	poll(t, bridge, printer, "Core One L", 1)

	var resumeLabel string
	bridge.db.QueryRow(
		`SELECT COALESCE(label,'') FROM pending_print_snapshots WHERE printer_id = ? AND event_type = 'resume'`,
		"test-printer-id",
	).Scan(&resumeLabel)

	if resumeLabel != "Resumed @ 44%" {
		t.Errorf("resume snapshot label: want 'Resumed @ 44%%', got %q", resumeLabel)
	}
	t.Logf("✅ Resume snapshot label: %q", resumeLabel)
}

// TestInterruptSnapshot_NoCameraNoOp verifies that attention/resume events with
// no camera configured produce no snapshots and no errors.
func TestInterruptSnapshot_NoCameraNoOp(t *testing.T) {
	const spoolID = 66
	bridge, printer, _ := setupBridgeWithMocks(t, map[int]float64{spoolID: 500.0})
	t.Setenv("THE_MOMENT_GCODE_PATH", t.TempDir())
	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	printer.SetGcodeUsage(map[int]float64{0: 50.0})
	printer.SetJobID(206)
	// No SetCameraSnapshot

	printer.SetState(StatePrinting)
	printer.SetProgress(50)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateAttention)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StatePrinting)
	printer.SetProgress(52)
	poll(t, bridge, printer, "Core One L", 1)

	if n := countPendingSnapshots(t, bridge, "test-printer-id"); n != 0 {
		t.Errorf("no camera: expected 0 pending snapshots, got %d", n)
	}
	t.Logf("✅ No camera: no attention/resume snapshots, no errors")
}
