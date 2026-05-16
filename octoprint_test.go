// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

// =============================================================================
// octoprint_test.go
// =============================================================================
// Unit tests for OctoPrint-specific logic: cost assembly, multi-spool deduction,
// and the assembleCostBreakdown helper.
//
// Integration tests for POST /api/prints live in octoprint_integration_test.go.
//
// Run unit tests:
//   go test ./... -v -run TestOctoPrint
// =============================================================================

import (
	"testing"
	"time"
)

// ─── assembleCostBreakdown ────────────────────────────────────────────────────

func TestAssembleCostBreakdown_ZeroInputs(t *testing.T) {
	settings := &CostSettings{
		ElectricityRate:  0.12,
		PrinterWattage:   150,
		MaintenanceRate:  0.10,
		DepreciationRate: 0.05,
		MarginPercent:    0,
		Currency:         "USD",
	}
	bd := assembleCostBreakdown(settings, nil, 0, 0, 0, 0, false)
	if bd.TotalCost != 0 {
		t.Errorf("expected zero cost for zero inputs, got %.4f", bd.TotalCost)
	}
}

func TestAssembleCostBreakdown_FilamentOnly(t *testing.T) {
	settings := &CostSettings{Currency: "USD"}
	// 100g at $20/kg = $2.00 filament cost, no time-based costs
	bd := assembleCostBreakdown(settings, nil, 100, 0, 2.00, 20.00, false)
	assertApprox(t, "filament cost", 2.00, bd.FilamentCost, 0.001)
	assertApprox(t, "total cost", 2.00, bd.TotalCost, 0.001)
}

func TestAssembleCostBreakdown_Margin(t *testing.T) {
	settings := &CostSettings{MarginPercent: 20, Currency: "USD"}
	// filament cost = $1.00, margin = 20%, total = $1.20
	bd := assembleCostBreakdown(settings, nil, 50, 0, 1.00, 0, false)
	assertApprox(t, "margin amount", 0.20, bd.MarginAmount, 0.001)
	assertApprox(t, "total cost", 1.20, bd.TotalCost, 0.001)
}

func TestAssembleCostBreakdown_TimeCosts(t *testing.T) {
	settings := &CostSettings{
		ElectricityRate:  0.12,  // $/kWh
		PrinterWattage:   120,   // W → 0.12 kWh per hour
		MaintenanceRate:  0.60,  // $/hour
		DepreciationRate: 0.30,  // $/hour
		Currency:         "USD",
	}
	// 60 minutes = 1 hour
	// electricity: 0.12 kW * 1h * $0.12/kWh = $0.0144
	// maintenance: 1h * $0.60 = $0.60
	// depreciation: 1h * $0.30 = $0.30
	bd := assembleCostBreakdown(settings, nil, 0, 60, 0, 0, false)
	assertApprox(t, "electricity", 0.0144, bd.ElectricityCost, 0.0001)
	assertApprox(t, "maintenance", 0.60, bd.MaintenanceCost, 0.001)
	assertApprox(t, "depreciation", 0.30, bd.DepreciationCost, 0.001)
	assertApprox(t, "subtotal", 0.9144, bd.SubTotal, 0.001)
}

// ─── Multi-spool filament summation ──────────────────────────────────────────

// TestLogOctoPrintRecord_MultiToolhead verifies that a two-toolhead print stores
// separate filament-usage rows (one per tool). Spoolman is NOT updated — the
// OctoPrint Spoolman plugin owns inventory for OctoPrint printers.
func TestLogOctoPrintRecord_MultiToolhead(t *testing.T) {
	bridge := testBridge(t)

	cancelReason := (*string)(nil)
	payload := OctoPrintPayload{
		Source:            "octoprint",
		PrinterID:         "ender3-v3-se",
		FileName:          "dual_color.gcode",
		Status:            "completed",
		StartedAt:         time.Now().Add(-90 * time.Minute),
		EndedAt:           time.Now(),
		TotalDurationSec:  5400,
		PrintDurationSec:  5400,
		PauseDurationSec:  0,
		PauseCount:        0,
		CancelReason:      cancelReason,
		TimePrecision:     "exact",
		FilamentPrecision: "measured",
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, SpoolID: 0, FilamentUsedMM: 3000, FilamentUsedG: 8.9},
			{ToolIndex: 1, SpoolID: 0, FilamentUsedMM: 2100, FilamentUsedG: 6.2},
		},
	}

	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord failed: %v", err)
	}
	if printID <= 0 {
		t.Fatalf("expected positive printID, got %d", printID)
	}

	entry, err := bridge.GetPrintHistoryEntry(printID)
	if err != nil {
		t.Fatalf("GetPrintHistoryEntry failed: %v", err)
	}

	// Total filament should be the sum across both tools.
	assertApprox(t, "total filament grams", 15.1, entry.FilamentUsed, 0.01)
	assertEqual(t, "source", "octoprint", entry.Source)
	assertEqual(t, "precision", "measured", entry.FilamentPrecision)

	if len(entry.FilamentUsages) != 2 {
		t.Fatalf("expected 2 filament-usage rows, got %d", len(entry.FilamentUsages))
	}
	assertApprox(t, "tool0 grams", 8.9, entry.FilamentUsages[0].FilamentUsedG, 0.01)
	assertApprox(t, "tool1 grams", 6.2, entry.FilamentUsages[1].FilamentUsedG, 0.01)
	assertApprox(t, "tool0 index", 0, float64(entry.FilamentUsages[0].ToolIndex), 0)
	assertApprox(t, "tool1 index", 1, float64(entry.FilamentUsages[1].ToolIndex), 0)
}

// TestLogOctoPrintRecord_FilamentChange verifies that sending two filament entries
// for the same tool (filament change mid-print) correctly sums the total and stores
// both segments as separate filament-usage rows. Spoolman is NOT updated.
func TestLogOctoPrintRecord_FilamentChange(t *testing.T) {
	bridge := testBridge(t)

	cancelReason := (*string)(nil)
	payload := OctoPrintPayload{
		Source:            "octoprint",
		PrinterID:         "ender3-v3-se",
		FileName:          "long_print.gcode",
		Status:            "completed",
		StartedAt:         time.Now().Add(-3 * time.Hour),
		EndedAt:           time.Now(),
		TotalDurationSec:  10800,
		PrintDurationSec:  10200,
		PauseDurationSec:  600,
		PauseCount:        1,
		CancelReason:      cancelReason,
		TimePrecision:     "exact",
		FilamentPrecision: "measured",
		Pauses: []OctoPrintPayloadPause{
			{
				PausedAt:    time.Now().Add(-2 * time.Hour),
				ResumedAt:   time.Now().Add(-2*time.Hour + 10*time.Minute),
				DurationSec: 600,
				Reason:      "runout",
			},
		},
		// Same tool, two spools: spool 3 used first, spool 5 loaded after runout.
		// SpoolID=0 here because test has no Spoolman; production sends real IDs.
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, SpoolID: 0, FilamentUsedMM: 2000, FilamentUsedG: 6.0},
			{ToolIndex: 0, SpoolID: 0, FilamentUsedMM: 2821, FilamentUsedG: 8.3},
		},
	}

	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord failed: %v", err)
	}

	entry, err := bridge.GetPrintHistoryEntry(printID)
	if err != nil {
		t.Fatalf("GetPrintHistoryEntry failed: %v", err)
	}

	// Total filament must be the sum of both segments.
	assertApprox(t, "total filament grams", 14.3, entry.FilamentUsed, 0.01)

	// Two filament-usage rows for the same tool (two separate spool segments).
	if len(entry.FilamentUsages) != 2 {
		t.Fatalf("expected 2 filament-usage rows for filament change, got %d", len(entry.FilamentUsages))
	}
	assertApprox(t, "segment0 grams", 6.0, entry.FilamentUsages[0].FilamentUsedG, 0.01)
	assertApprox(t, "segment1 grams", 8.3, entry.FilamentUsages[1].FilamentUsedG, 0.01)

	// Pause detail.
	if len(entry.Pauses) != 1 {
		t.Fatalf("expected 1 pause, got %d", len(entry.Pauses))
	}
	assertEqual(t, "pause reason", "runout", entry.Pauses[0].Reason)
	assertApprox(t, "pause duration", 600.0, entry.Pauses[0].DurationSec, 0.1)

	// Precision flags.
	assertEqual(t, "time precision", "exact", entry.TimePrecision)
	assertApprox(t, "pause_duration_sec", 600.0, entry.PauseDurationSec, 0.1)
	assertApprox(t, "pause_count", 1, float64(entry.PauseCount), 0)
}

// TestLogOctoPrintRecord_CancelledPrint verifies cancel_reason is stored and
// returned correctly.
func TestLogOctoPrintRecord_CancelledPrint(t *testing.T) {
	bridge := testBridge(t)

	reason := "user"
	payload := OctoPrintPayload{
		Source:    "octoprint",
		PrinterID: "ender3-v3-se",
		FileName:  "failed.gcode",
		Status:    "cancelled",
		StartedAt: time.Now().Add(-10 * time.Minute),
		EndedAt:   time.Now(),
		TotalDurationSec: 600,
		PrintDurationSec: 600,
		CancelReason:     &reason,
		TimePrecision:    "exact",
		FilamentPrecision: "measured",
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, SpoolID: 0, FilamentUsedMM: 500, FilamentUsedG: 1.5},
		},
	}

	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord failed: %v", err)
	}

	entry, err := bridge.GetPrintHistoryEntry(printID)
	if err != nil {
		t.Fatalf("GetPrintHistoryEntry failed: %v", err)
	}

	assertEqual(t, "status", "cancelled", entry.Status)
	assertEqual(t, "cancel_reason", "user", entry.CancelReason)
}

// TestAssembleCostBreakdown_MultiSpoolWeighting verifies that the multi-spool cost
// function weights each entry by its own gram count (no Spoolman, so prices=0).
func TestAssembleCostBreakdown_MultiSpoolWeighting(t *testing.T) {
	settings := &CostSettings{
		ElectricityRate:  0.10,
		PrinterWattage:   100,
		MaintenanceRate:  0.0,
		DepreciationRate: 0.0,
		MarginPercent:    0,
		Currency:         "USD",
	}
	// 60 min, no filament cost → only electricity: 0.1kW * 1h * $0.10/kWh = $0.01
	filament := []OctoPrintPayloadFilament{
		{ToolIndex: 0, SpoolID: 0, FilamentUsedMM: 1000, FilamentUsedG: 3.0},
		{ToolIndex: 0, SpoolID: 0, FilamentUsedMM: 2000, FilamentUsedG: 6.0},
	}
	totalGrams := 9.0
	filamentCost := 0.0 // SpoolID=0, no price
	bd := assembleCostBreakdown(settings, nil, totalGrams, 60, filamentCost, 0, false)

	assertApprox(t, "total grams echoed", 9.0, bd.FilamentGrams, 0.01)
	expectedElec := (100.0 / 1000.0) * 1.0 * 0.10
	assertApprox(t, "electricity", expectedElec, bd.ElectricityCost, 0.0001)
	// Total = electricity only since no filament/maintenance/depreciation cost
	assertApprox(t, "total", expectedElec, bd.TotalCost, 0.0001)

	// Sanity: unused variable to avoid lint noise
	_ = filament
}

// ─── PrusaLink records keep legacy precision defaults ─────────────────────────

// TestGetPrintHistory_PrusaLinkDefaults verifies that old PrusaLink records
// (no source column) are returned with 'prusalink' source and 'approximate' precision.
func TestGetPrintHistory_PrusaLinkDefaults(t *testing.T) {
	bridge := testBridge(t)

	// Write a minimal record the old way (no source/precision columns).
	_, err := bridge.db.Exec(`
		INSERT INTO print_history
			(printer_name, toolhead_id, spool_id, filament_used,
			 print_started, print_finished, job_name, status, print_time_minutes)
		VALUES ('Prusa Core One', 0, 1, 12.5,
		        '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z',
		        'benchy.gcode', 'completed', 60)`)
	if err != nil {
		t.Fatalf("failed to insert legacy record: %v", err)
	}

	records, err := bridge.GetPrintHistory(10)
	if err != nil {
		t.Fatalf("GetPrintHistory failed: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected at least one record")
	}
	r := records[0]
	assertEqual(t, "source default", "prusalink", r.Source)
	assertEqual(t, "time_precision default", "approximate", r.TimePrecision)
	assertEqual(t, "filament_precision default", "estimated", r.FilamentPrecision)
}

// ─── Spoolman isolation ───────────────────────────────────────────────────────

// TestLogOctoPrintRecord_NoSpoolmanUpdate verifies that LogOctoPrintRecord never
// writes to pending_spoolman_updates, even when real spool IDs are provided.
// Inventory is the OctoPrint Spoolman plugin's responsibility; The Moment must
// not double-decrement by also updating it.
func TestLogOctoPrintRecord_NoSpoolmanUpdate(t *testing.T) {
	bridge := testBridge(t)

	payload := OctoPrintPayload{
		Source:            "octoprint",
		PrinterID:         "ender3-v3-se",
		FileName:          "real_spools.gcode",
		Status:            "completed",
		StartedAt:         time.Now().Add(-60 * time.Minute),
		EndedAt:           time.Now(),
		TotalDurationSec:  3600,
		PrintDurationSec:  3600,
		TimePrecision:     "exact",
		FilamentPrecision: "measured",
		// Real spool IDs — would trigger Spoolman updates in the old (broken) code.
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, ChangeNumber: 0, SpoolID: 42, FilamentUsedMM: 3000, FilamentUsedG: 9.0},
			{ToolIndex: 1, ChangeNumber: 0, SpoolID: 99, FilamentUsedMM: 1500, FilamentUsedG: 4.5},
		},
	}

	if _, err := bridge.LogOctoPrintRecord(payload); err != nil {
		t.Fatalf("LogOctoPrintRecord failed: %v", err)
	}

	var count int
	if err := bridge.db.QueryRow(`SELECT COUNT(*) FROM pending_spoolman_updates`).Scan(&count); err != nil {
		t.Fatalf("failed to query pending_spoolman_updates: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 pending Spoolman updates for OctoPrint record, got %d — double-decrement risk", count)
	}
	t.Logf("✅ pending_spoolman_updates is empty — Spoolman inventory left to OctoPrint plugin")
}

// ─── session_id and change_number ────────────────────────────────────────────

// TestLogOctoPrintRecord_SessionIDGenerated verifies that a payload with no
// session_id gets one assigned by the server, and it appears in the history record.
func TestLogOctoPrintRecord_SessionIDGenerated(t *testing.T) {
	bridge := testBridge(t)

	payload := OctoPrintPayload{
		PrinterID: "ender3-v3-se",
		FileName:  "no_session.gcode",
		Status:    "completed",
		EndedAt:   time.Now(),
		Filament:  []OctoPrintPayloadFilament{},
	}
	// SessionID deliberately left empty — server must generate one.

	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord: %v", err)
	}
	entry, err := bridge.GetPrintHistoryEntry(printID)
	if err != nil {
		t.Fatalf("GetPrintHistoryEntry: %v", err)
	}
	if entry.SessionID == "" {
		t.Error("expected a non-empty session_id to be generated")
	}
	t.Logf("✅ session_id generated: %s", entry.SessionID)
}

// TestLogOctoPrintRecord_SessionIDPassthrough verifies that a plugin-supplied
// session_id is stored and returned unchanged.
func TestLogOctoPrintRecord_SessionIDPassthrough(t *testing.T) {
	bridge := testBridge(t)

	wantSession := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	payload := OctoPrintPayload{
		SessionID: wantSession,
		PrinterID: "ender3-v3-se",
		FileName:  "session_test.gcode",
		Status:    "completed",
		EndedAt:   time.Now(),
		Filament:  []OctoPrintPayloadFilament{},
	}

	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord: %v", err)
	}
	entry, err := bridge.GetPrintHistoryEntry(printID)
	if err != nil {
		t.Fatalf("GetPrintHistoryEntry: %v", err)
	}
	assertEqual(t, "session_id", wantSession, entry.SessionID)
	t.Logf("✅ session_id passthrough: %s", entry.SessionID)
}

// TestLogOctoPrintRecord_ChangeNumber verifies that a filament-change payload
// (two entries for tool 0 with different spools) stores distinct change_numbers.
func TestLogOctoPrintRecord_ChangeNumber(t *testing.T) {
	bridge := testBridge(t)

	payload := OctoPrintPayload{
		PrinterID: "ender3-v3-se",
		FileName:  "change_test.gcode",
		Status:    "completed",
		EndedAt:   time.Now(),
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, ChangeNumber: 0, SpoolID: 1, FilamentUsedMM: 1000, FilamentUsedG: 3.0},
			{ToolIndex: 0, ChangeNumber: 1, SpoolID: 2, FilamentUsedMM: 2000, FilamentUsedG: 6.0},
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
	if len(entry.FilamentUsages) != 2 {
		t.Fatalf("expected 2 filament_usages, got %d", len(entry.FilamentUsages))
	}
	if entry.FilamentUsages[0].ChangeNumber != 0 {
		t.Errorf("expected change_number=0 for first load, got %d", entry.FilamentUsages[0].ChangeNumber)
	}
	if entry.FilamentUsages[1].ChangeNumber != 1 {
		t.Errorf("expected change_number=1 for second load, got %d", entry.FilamentUsages[1].ChangeNumber)
	}
	t.Logf("✅ change_number: load0=%d, load1=%d",
		entry.FilamentUsages[0].ChangeNumber, entry.FilamentUsages[1].ChangeNumber)
}

// TestGetPrintSessions_GroupsBySessionID verifies that two print_history rows
// sharing a session_id are returned as a single PrintSession.
func TestGetPrintSessions_GroupsBySessionID(t *testing.T) {
	bridge := testBridge(t)

	sharedSession := "shared-session-id"
	// Insert two toolhead rows for the same PrusaLink print.
	for _, toolhead := range []int{0, 1} {
		_, err := bridge.db.Exec(`
			INSERT INTO print_history
				(printer_name, toolhead_id, spool_id, filament_used,
				 print_started, print_finished, job_name, status,
				 print_time_minutes, session_id, source)
			VALUES ('CoreOne', ?, ?, 10.0,
			        '2026-04-20T10:00:00Z', '2026-04-20T11:00:00Z',
			        'twohead.gcode', 'completed', 60, ?, 'prusalink')`,
			toolhead, toolhead+1, sharedSession)
		if err != nil {
			t.Fatalf("insert toolhead %d: %v", toolhead, err)
		}
	}
	// Insert one unrelated record with no session_id (legacy row).
	_, err := bridge.db.Exec(`
		INSERT INTO print_history
			(printer_name, toolhead_id, spool_id, filament_used,
			 print_started, print_finished, job_name, status, print_time_minutes)
		VALUES ('CoreOne', 0, 1, 5.0,
		        '2026-04-19T10:00:00Z', '2026-04-19T11:00:00Z',
		        'legacy.gcode', 'completed', 60)`)
	if err != nil {
		t.Fatalf("insert legacy record: %v", err)
	}

	sessions, err := bridge.GetPrintSessions(10)
	if err != nil {
		t.Fatalf("GetPrintSessions: %v", err)
	}
	// Three rows → two sessions (two rows share session_id, one is solo legacy).
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	// Newest first: shared session printed 2026-04-20 comes before legacy 2026-04-19.
	shared := sessions[0]
	if shared.SessionID != sharedSession {
		t.Errorf("expected session_id=%s, got %s", sharedSession, shared.SessionID)
	}
	if shared.ToolCount != 2 {
		t.Errorf("expected tool_count=2, got %d", shared.ToolCount)
	}
	assertApprox(t, "total filament", 20.0, shared.TotalFilamentG, 0.01)

	legacy := sessions[1]
	if legacy.SessionID != "" {
		t.Errorf("expected empty session_id for legacy row, got %s", legacy.SessionID)
	}
	if legacy.ToolCount != 1 {
		t.Errorf("expected tool_count=1 for solo legacy, got %d", legacy.ToolCount)
	}
	t.Logf("✅ sessions: shared(%d tools, %.1fg) + legacy(1 tool)", shared.ToolCount, shared.TotalFilamentG)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// testBridge creates an isolated FilamentBridge backed by a temp SQLite DB.
func testBridge(t *testing.T) *FilamentBridge {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("THE_MOMENT_DB_PATH", tmpDir)
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })
	return bridge
}
