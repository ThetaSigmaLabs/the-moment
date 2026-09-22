// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

//go:build integration

package main

// =============================================================================
// filament_warning_integration_test.go
// =============================================================================
// Integration tests for the two-tier filament warning system:
// severity tiers (critical/warning), buffer percentage, mid-print rescaling,
// and severity escalation flood prevention.
//
// Run with:
//   go test -tags=integration -v -run TestCheckFilamentSufficiency ./...
//   go test -tags=integration -v -run TestRecheckFilament ./...
//   go test -tags=integration -v -run TestSeverityEscalation ./...
// =============================================================================

import "testing"

// TestCheckFilamentSufficiency_Critical verifies that a spool with far less
// filament than required generates a "critical" severity warning.
func TestCheckFilamentSufficiency_Critical(t *testing.T) {
	// spool 1: 1000g initial, 990g used → 10g remaining; job needs 45g
	spoolman := NewMockSpoolman(t, map[int]float64{1: 1000})
	spoolman.SetUsedWeight(1, 990.0)

	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(map[int]float64{0: 45.0})

	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)
	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	bridge.checkFilamentSufficiency("test-printer", "Core One L", "usb/test.gcode", client)

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 1 {
		t.Fatalf("expected 1 critical warning, got %d: %v", len(warnings), warnings)
	}
	if warnings[0].Severity != "critical" {
		t.Errorf("severity: got %q, want %q", warnings[0].Severity, "critical")
	}
}

// TestCheckFilamentSufficiency_Warning verifies that a spool with more filament
// than required but within the buffer threshold generates a "warning" severity.
// Default buffer is 20%: 50g remaining vs 45g required → 45×1.20=54 → warning.
func TestCheckFilamentSufficiency_Warning(t *testing.T) {
	// spool 1: 1000g initial, 950g used → 50g remaining; job needs 45g
	spoolman := NewMockSpoolman(t, map[int]float64{1: 1000})
	spoolman.SetUsedWeight(1, 950.0)

	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(map[int]float64{0: 45.0})

	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)
	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	// Explicitly set the buffer to 20% (same as default, but explicit for clarity).
	if err := bridge.SetConfigValue(ConfigKeyFilamentWarnBufferPct, "20"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	bridge.checkFilamentSufficiency("test-printer", "Core One L", "usb/test.gcode", client)

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
	if warnings[0].Severity != "warning" {
		t.Errorf("severity: got %q, want %q", warnings[0].Severity, "warning")
	}
}

// TestCheckFilamentSufficiency_SufficientWithBuffer verifies that a spool with
// filament above the buffer threshold produces no warnings.
// 60g remaining vs 45g required with 20% buffer → threshold 54g → no warning.
func TestCheckFilamentSufficiency_SufficientWithBuffer(t *testing.T) {
	// spool 1: 1000g initial, 940g used → 60g remaining; job needs 45g
	spoolman := NewMockSpoolman(t, map[int]float64{1: 1000})
	spoolman.SetUsedWeight(1, 940.0)

	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(map[int]float64{0: 45.0})

	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)
	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	if err := bridge.SetConfigValue(ConfigKeyFilamentWarnBufferPct, "20"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	bridge.checkFilamentSufficiency("test-printer", "Core One L", "usb/test.gcode", client)

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 0 {
		t.Errorf("expected no warnings for sufficient filament with buffer, got %d: %v", len(warnings), warnings)
	}
}

// TestRecheckFilamentSufficiency_ProgressScaling verifies that recheckFilamentSufficiency
// correctly scales the original G-code requirements by the remaining progress fraction.
// 100g original at 50% remaining → 50g needed; spool has 55g → low (warning, not critical).
func TestRecheckFilamentSufficiency_ProgressScaling(t *testing.T) {
	// spool 1: 200g initial, 145g used → 55g remaining
	spoolman := NewMockSpoolman(t, map[int]float64{1: 200})
	spoolman.SetUsedWeight(1, 145.0)

	mock := NewMockPrusaLink(t)
	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)
	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	if err := bridge.SetConfigValue(ConfigKeyFilamentWarnBufferPct, "20"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	// Simulate print having been running: cache requirements as if checkFilamentSufficiency ran.
	bridge.printerWarningsMu.Lock()
	bridge.printerRequirements["test-printer"] = map[int]float64{0: 100.0}
	bridge.printerWarningsMu.Unlock()

	// At 50% progress: 50% remaining → scaled requirement = 100 × 0.5 = 50g.
	// 55g remaining > 50g required (not critical), 55g < 50×1.20=60g (warning).
	bridge.recheckFilamentSufficiency("test-printer", "Core One L", 0.5)

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	severity := bridge.lastNotifiedSeverity["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning at 50%% progress, got %d: %v", len(warnings), warnings)
	}
	if warnings[0].Severity != "warning" {
		t.Errorf("severity: got %q, want %q (55g > 50g scaled requirement, but within 20%% buffer)", warnings[0].Severity, "warning")
	}
	if severity != "warning" {
		t.Errorf("lastNotifiedSeverity: got %q, want %q", severity, "warning")
	}
}

// TestSeverityEscalation_NoFlood verifies that recheckFilamentSufficiency:
//  1. Does not re-send notifications at the same severity (flood prevention).
//  2. Does escalate from "warning" to "critical" when conditions worsen.
func TestSeverityEscalation_NoFlood(t *testing.T) {
	// spool 1: 200g initial, 145g used → 55g remaining
	spoolman := NewMockSpoolman(t, map[int]float64{1: 200})
	spoolman.SetUsedWeight(1, 145.0)

	mock := NewMockPrusaLink(t)
	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)
	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	if err := bridge.SetConfigValue(ConfigKeyFilamentWarnBufferPct, "20"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	bridge.printerWarningsMu.Lock()
	bridge.printerRequirements["test-printer"] = map[int]float64{0: 100.0}
	bridge.printerWarningsMu.Unlock()

	// First recheck: 55g remaining, scaled 50g needed → warning.
	bridge.recheckFilamentSufficiency("test-printer", "Core One L", 0.5)

	bridge.printerWarningsMu.Lock()
	afterFirst := bridge.lastNotifiedSeverity["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if afterFirst != "warning" {
		t.Fatalf("after first recheck: lastNotifiedSeverity = %q, want %q", afterFirst, "warning")
	}

	// Second recheck with identical conditions: severity is still "warning" → no escalation.
	// The lastNotifiedSeverity must remain "warning" (not reset or doubled).
	bridge.recheckFilamentSufficiency("test-printer", "Core One L", 0.5)

	bridge.printerWarningsMu.Lock()
	afterSecond := bridge.lastNotifiedSeverity["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if afterSecond != "warning" {
		t.Errorf("after second recheck (same conditions): lastNotifiedSeverity = %q, want %q", afterSecond, "warning")
	}

	// Worsen spool: 200g initial, 165g used → 35g remaining.
	// Scaled 50g needed, 35g remaining → critical.
	spoolman.SetUsedWeight(1, 165.0)

	bridge.recheckFilamentSufficiency("test-printer", "Core One L", 0.5)

	bridge.printerWarningsMu.Lock()
	afterEscalation := bridge.lastNotifiedSeverity["test-printer"]
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if afterEscalation != "critical" {
		t.Errorf("after escalation: lastNotifiedSeverity = %q, want %q", afterEscalation, "critical")
	}
	if len(warnings) == 0 || warnings[0].Severity != "critical" {
		t.Errorf("after escalation: expected critical warning, got %v", warnings)
	}
}
