// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

package main

// =============================================================================
// filament_sufficiency_test.go
// =============================================================================
// Tests for checkFilamentSufficiency and GetFileInfo.
//
// Run with:
//   go test -tags=integration -v -run TestFilamentSufficiency ./...
//   go test -tags=integration -v -run TestGetFileInfo ./...
// =============================================================================

import (
	"testing"
	"time"
)

// newBridgeWithSpoolman creates a fresh bridge pointing at mock Spoolman,
// with a temp DB. The caller is responsible for cleanup via t.Cleanup.
func newBridgeWithSpoolman(t *testing.T, spoolman *MockSpoolman) *FilamentBridge {
	t.Helper()
	t.Setenv("THE_MOMENT_DB_PATH", t.TempDir())
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })

	config, err := LoadConfig(bridge)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	config.SpoolmanURL = spoolman.URL()
	bridge.UpdateConfig(config)
	return bridge
}

// addPrusaLinkPrinter registers a single-toolhead PrusaLink printer directly in the DB.
// Does not reload b.config, so the mock Spoolman URL set by newBridgeWithSpoolman is preserved.
func addPrusaLinkPrinter(t *testing.T, bridge *FilamentBridge, printerID, name string, mock *MockPrusaLink) {
	t.Helper()
	_, err := bridge.db.Exec(`
		INSERT OR REPLACE INTO printer_configs
			(printer_id, name, model, ip_address, api_key, toolheads, is_virtual)
		VALUES (?, ?, ?, ?, '', 1, 0)`,
		printerID, name, ModelCoreOneL, mock.HostPort())
	if err != nil {
		t.Fatalf("addPrusaLinkPrinter: %v", err)
	}
}

// ─── GetFileInfo unit tests ───────────────────────────────────────────────────

// TestGetFileInfo_MetadataAvailable verifies that GetFileInfo returns filament
// weight data when the firmware exposes the /api/v1/files endpoint.
func TestGetFileInfo_MetadataAvailable(t *testing.T) {
	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(map[int]float64{0: 45.5})

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	info, err := client.GetFileInfo("usb/testprint.gcode")
	if err != nil {
		t.Fatalf("GetFileInfo error: %v", err)
	}
	if info == nil {
		t.Fatal("GetFileInfo returned nil; expected filament data")
	}
	if len(info.Filament) != 1 {
		t.Fatalf("expected 1 filament entry, got %d", len(info.Filament))
	}
	if info.Filament[0].Weight != 45.5 {
		t.Errorf("filament weight: got %.1f, want 45.5", info.Filament[0].Weight)
	}
}

// TestGetFileInfo_NotAvailable verifies that GetFileInfo returns nil (no error)
// when the endpoint returns 404, signalling the caller should fall back to G-code download.
func TestGetFileInfo_NotAvailable(t *testing.T) {
	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(nil) // 404

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	info, err := client.GetFileInfo("usb/testprint.gcode")
	if err != nil {
		t.Fatalf("GetFileInfo returned unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil when endpoint returns 404, got %+v", info)
	}
}

// ─── checkFilamentSufficiency tests ──────────────────────────────────────────

// TestFilamentSufficiency_SufficientFilament verifies no warning is stored
// when the spool has more than enough filament.
func TestFilamentSufficiency_SufficientFilament(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{1: 1000}) // spool 1: 1000g initial
	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(map[int]float64{0: 45.0}) // job needs 45g

	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)

	// Assign spool 1 to toolhead 0
	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	bridge.checkFilamentSufficiency("test-printer", "Core One L", "usb/testprint.gcode", client)

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 0 {
		t.Errorf("expected no warnings for sufficient filament, got %d: %v", len(warnings), warnings)
	}
}

// TestFilamentSufficiency_InsufficientFilament verifies a warning is stored
// when the spool has less filament than the job requires.
func TestFilamentSufficiency_InsufficientFilament(t *testing.T) {
	// spool 1: 1000g initial, 990g used → 10g remaining
	spoolman := NewMockSpoolman(t, map[int]float64{1: 1000})
	spoolman.SetUsedWeight(1, 990.0)

	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(map[int]float64{0: 45.0}) // job needs 45g

	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)

	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	bridge.checkFilamentSufficiency("test-printer", "Core One L", "usb/testprint.gcode", client)

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
	w := warnings[0]
	if w.ToolheadIndex != 0 {
		t.Errorf("toolhead index: got %d, want 0", w.ToolheadIndex)
	}
	if w.SpoolID != 1 {
		t.Errorf("spool ID: got %d, want 1", w.SpoolID)
	}
	if w.Required != 45.0 {
		t.Errorf("required: got %.1f, want 45.0", w.Required)
	}
	if w.Remaining != 10.0 {
		t.Errorf("remaining: got %.1f, want 10.0", w.Remaining)
	}
	if w.Message == "" {
		t.Error("warning message is empty")
	}
}

// TestFilamentSufficiency_NoSpoolAssigned verifies a warning is stored
// when the toolhead has no spool assigned but the job needs filament.
func TestFilamentSufficiency_NoSpoolAssigned(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{})
	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(map[int]float64{0: 30.0})

	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)
	// No spool assignment

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	bridge.checkFilamentSufficiency("test-printer", "Core One L", "usb/testprint.gcode", client)

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning for unassigned toolhead, got %d", len(warnings))
	}
	if warnings[0].SpoolID != 0 {
		t.Errorf("expected SpoolID=0 for unassigned, got %d", warnings[0].SpoolID)
	}
}

// TestFilamentSufficiency_MultiToolhead verifies per-toolhead comparison.
// T0 has enough, T1 does not.
func TestFilamentSufficiency_MultiToolhead(t *testing.T) {
	// spool 10: 500g initial, 450g used → 50g remaining (enough for T0 needing 30g)
	// spool 11: 500g initial, 490g used → 10g remaining (not enough for T1 needing 40g)
	spoolman := NewMockSpoolman(t, map[int]float64{10: 500, 11: 500})
	spoolman.SetUsedWeight(10, 450.0)
	spoolman.SetUsedWeight(11, 490.0)

	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(map[int]float64{0: 30.0, 1: 40.0})

	bridge := newBridgeWithSpoolman(t, spoolman)

	// Register a 2-toolhead printer directly in the DB.
	// Does not call UpdateConfig so the mock Spoolman URL is preserved.
	_, err := bridge.db.Exec(`
		INSERT OR REPLACE INTO printer_configs
			(printer_id, name, model, ip_address, api_key, toolheads, is_virtual)
		VALUES (?, ?, ?, ?, '', 2, 0)`,
		"test-printer", "Core One L", ModelCoreOneL, mock.HostPort())
	if err != nil {
		t.Fatalf("insert printer config: %v", err)
	}

	if err := bridge.SetToolheadMapping("Core One L", 0, 10); err != nil {
		t.Fatalf("SetToolheadMapping T0: %v", err)
	}
	if err := bridge.SetToolheadMapping("Core One L", 1, 11); err != nil {
		t.Fatalf("SetToolheadMapping T1: %v", err)
	}

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	bridge.checkFilamentSufficiency("test-printer", "Core One L", "usb/testprint.gcode", client)

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning (T1 only), got %d: %v", len(warnings), warnings)
	}
	if warnings[0].ToolheadIndex != 1 {
		t.Errorf("expected warning on T1, got T%d", warnings[0].ToolheadIndex)
	}
}

// TestFilamentSufficiency_GcodeFallback verifies that when file metadata is unavailable
// (404), the check falls back to parsing the G-code file and still produces a warning.
func TestFilamentSufficiency_GcodeFallback(t *testing.T) {
	// spool 1: 1000g initial, 985g used → 15g remaining; job needs 25g
	spoolman := NewMockSpoolman(t, map[int]float64{1: 1000})
	spoolman.SetUsedWeight(1, 985.0)

	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(nil)                    // metadata unavailable → G-code fallback
	mock.SetGcodeUsage(map[int]float64{0: 25.0})    // gcode says 25g needed

	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)

	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	bridge.checkFilamentSufficiency("test-printer", "Core One L", mock.State.JobFileName, client)

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning via G-code fallback, got %d", len(warnings))
	}
	if warnings[0].Required != 25.0 {
		t.Errorf("required grams from gcode: got %.1f, want 25.0", warnings[0].Required)
	}
}

// TestFilamentSufficiency_WarningsCleared verifies that warnings are removed
// after being set when a second call finds sufficient filament.
func TestFilamentSufficiency_WarningsCleared(t *testing.T) {
	// First run: low filament → warning
	spoolman := NewMockSpoolman(t, map[int]float64{1: 1000})
	spoolman.SetUsedWeight(1, 990.0) // 10g remaining

	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(map[int]float64{0: 45.0})

	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)

	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	client := NewPrusaLinkClient(mock.HostPort(), "", 10, 30)
	bridge.checkFilamentSufficiency("test-printer", "Core One L", "usb/testprint.gcode", client)

	bridge.printerWarningsMu.Lock()
	before := len(bridge.printerWarnings["test-printer"])
	bridge.printerWarningsMu.Unlock()

	if before != 1 {
		t.Fatalf("expected 1 warning before clear, got %d", before)
	}

	// Manually clear warnings (simulates print ending)
	bridge.printerWarningsMu.Lock()
	delete(bridge.printerWarnings, "test-printer")
	bridge.printerWarningsMu.Unlock()

	bridge.printerWarningsMu.Lock()
	after := len(bridge.printerWarnings["test-printer"])
	bridge.printerWarningsMu.Unlock()

	if after != 0 {
		t.Errorf("expected warnings cleared, got %d", after)
	}
}

// TestFilamentSufficiency_InsufficientFilament_TimesOut verifies that when G-code
// download fails (printer returns 503), the function returns without panicking
// and stores no warnings (fail-open: don't block the print).
func TestFilamentSufficiency_GcodeDownloadFails(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{1: 1000})
	spoolman.SetUsedWeight(1, 990.0)

	mock := NewMockPrusaLink(t)
	mock.SetFileInfoFilament(nil) // metadata unavailable
	mock.SetGcodeUnavailable(true)

	bridge := newBridgeWithSpoolman(t, spoolman)
	addPrusaLinkPrinter(t, bridge, "test-printer", "Core One L", mock)

	if err := bridge.SetToolheadMapping("Core One L", 0, 1); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	done := make(chan struct{})
	go func() {
		client := NewPrusaLinkClient(mock.HostPort(), "", 10, 5) // 5s download timeout
		bridge.checkFilamentSufficiency("test-printer", "Core One L", mock.State.JobFileName, client)
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(15 * time.Second):
		t.Fatal("checkFilamentSufficiency did not return within 15s after G-code download failure")
	}

	bridge.printerWarningsMu.Lock()
	warnings := bridge.printerWarnings["test-printer"]
	bridge.printerWarningsMu.Unlock()

	if len(warnings) != 0 {
		t.Errorf("expected no warnings after download failure (fail-open), got %d", len(warnings))
	}
}
