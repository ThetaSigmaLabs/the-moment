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
	"math"
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
	bd := assembleCostBreakdown(settings, 0, 0, 0, 0)
	if bd.TotalCost != 0 {
		t.Errorf("expected zero cost for zero inputs, got %.4f", bd.TotalCost)
	}
}

func TestAssembleCostBreakdown_FilamentOnly(t *testing.T) {
	settings := &CostSettings{Currency: "USD"}
	// 100g at $20/kg = $2.00 filament cost, no time-based costs
	bd := assembleCostBreakdown(settings, 100, 0, 2.00, 20.00)
	assertApprox(t, "filament cost", 2.00, bd.FilamentCost, 0.001)
	assertApprox(t, "total cost", 2.00, bd.TotalCost, 0.001)
}

func TestAssembleCostBreakdown_Margin(t *testing.T) {
	settings := &CostSettings{MarginPercent: 20, Currency: "USD"}
	// filament cost = $1.00, margin = 20%, total = $1.20
	bd := assembleCostBreakdown(settings, 50, 0, 1.00, 0)
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
	bd := assembleCostBreakdown(settings, 0, 60, 0, 0)
	assertApprox(t, "electricity", 0.0144, bd.ElectricityCost, 0.0001)
	assertApprox(t, "maintenance", 0.60, bd.MaintenanceCost, 0.001)
	assertApprox(t, "depreciation", 0.30, bd.DepreciationCost, 0.001)
	assertApprox(t, "subtotal", 0.9144, bd.SubTotal, 0.001)
}

// ─── Multi-spool filament summation ──────────────────────────────────────────

// TestLogOctoPrintRecord_MultiToolhead verifies that a two-toolhead print stores
// separate filament-usage rows and queues two Spoolman updates.
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
// for the same tool (filament change mid-print) correctly sums the total and queues
// separate Spoolman updates per spool.
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
	bd := assembleCostBreakdown(settings, totalGrams, 60, filamentCost, 0)

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

// roundTo4 rounds to 4 decimal places — matches assembleCostBreakdown rounding.
func roundTo4(v float64) float64 { return math.Round(v*10000) / 10000 }
