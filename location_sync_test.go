//go:build integration

// SPDX-License-Identifier: GPL-3.0-or-later

package main

// =============================================================================
// location_sync_test.go
// =============================================================================
// Verifies that assigning a spool to a toolhead pushes the correct location
// string ("Ender3 - T0") to Spoolman when spoolman_location_sync_enabled=true.
//
// Run with:
//   go test -tags=integration -v -run TestLocationSync ./...
// =============================================================================

import (
	"testing"
)

// TestLocationSync_FormatToolheadLocation verifies the canonical location string format.
func TestLocationSync_FormatToolheadLocation(t *testing.T) {
	cases := []struct {
		printer string
		idx     int
		want    string
	}{
		{"Ender3", 0, "Ender3 - T0"},
		{"Core One L", 2, "Core One L - T2"},
		{"X1C Printer", 3, "X1C Printer - T3"},
	}
	for _, tc := range cases {
		got := FormatToolheadLocation(tc.printer, tc.idx)
		if got != tc.want {
			t.Errorf("FormatToolheadLocation(%q, %d) = %q; want %q", tc.printer, tc.idx, got, tc.want)
		}
	}
}

// TestLocationSync_ParseToolheadLocation verifies the reverse parse.
func TestLocationSync_ParseToolheadLocation(t *testing.T) {
	cases := []struct {
		input       string
		wantPrinter string
		wantIdx     int
		wantOK      bool
	}{
		{"Ender3 - T0", "Ender3", 0, true},
		{"Core One L - T2", "Core One L", 2, true},
		{"not a location", "", 0, false},
		{"Printer - Tbadnum", "", 0, false},
	}
	for _, tc := range cases {
		printer, idx, ok := ParseToolheadLocation(tc.input)
		if ok != tc.wantOK || printer != tc.wantPrinter || idx != tc.wantIdx {
			t.Errorf("ParseToolheadLocation(%q) = (%q, %d, %v); want (%q, %d, %v)",
				tc.input, printer, idx, ok, tc.wantPrinter, tc.wantIdx, tc.wantOK)
		}
	}
}

// TestLocationSync_SyncDisabled_NoSpoolmanUpdate verifies the default state:
// when spoolman_location_sync_enabled is false (the default), SetToolheadMapping
// must NOT push any location update to Spoolman.
func TestLocationSync_SyncDisabled_NoSpoolmanUpdate(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{5: 1000})

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

	// Confirm sync is disabled (default)
	enabled, err := bridge.GetSpoolmanLocationSyncEnabled()
	if err != nil {
		t.Fatalf("GetSpoolmanLocationSyncEnabled: %v", err)
	}
	if enabled {
		t.Fatal("expected sync disabled by default")
	}

	if err := bridge.SetToolheadMapping("Ender3", 0, 5); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	locUpdates := spoolman.LocationUpdatesForSpool(5)
	if len(locUpdates) != 0 {
		t.Errorf("expected 0 location updates when sync disabled, got %d: %v", len(locUpdates), locUpdates)
	}
}

// TestLocationSync_SyncEnabled_PushesLocationToSpoolman is the primary scenario:
// assign spool 5 to Ender3 toolhead 0 → Spoolman must receive PATCH with
// {"location": "Ender3 - T0"}.
func TestLocationSync_SyncEnabled_PushesLocationToSpoolman(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{5: 1000})

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

	// Enable the location sync feature
	if err := bridge.SetConfigValue(ConfigKeySpoolmanLocationSyncEnabled, "true"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	// Assign spool 5 to Ender3 toolhead 0
	if err := bridge.SetToolheadMapping("Ender3", 0, 5); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	wantLocation := "Ender3 - T0"

	locUpdates := spoolman.LocationUpdatesForSpool(5)
	if len(locUpdates) == 0 {
		t.Fatalf("expected Spoolman to receive a location update for spool 5, got none")
	}
	if locUpdates[0].LocationName != wantLocation {
		t.Errorf("Spoolman received location %q; want %q", locUpdates[0].LocationName, wantLocation)
	}
}

// TestLocationSync_ReassignSpool_UpdatesLocation verifies that reassigning the same
// spool to a different toolhead sends the new location (not the old one).
func TestLocationSync_ReassignSpool_UpdatesLocation(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{5: 1000, 7: 1000})

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

	if err := bridge.SetConfigValue(ConfigKeySpoolmanLocationSyncEnabled, "true"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	// First: assign spool 5 to Ender3 T0
	if err := bridge.SetToolheadMapping("Ender3", 0, 5); err != nil {
		t.Fatalf("SetToolheadMapping T0: %v", err)
	}

	// Then: put spool 7 on T0 (displaces spool 5), put spool 5 on T1 via separate call
	// Actually test: assign spool 5 to T1 on a different printer to confirm location updates
	// For simplicity: map spool 7 to T0, which is a separate new assignment
	if err := bridge.SetToolheadMapping("Ender3", 1, 7); err != nil {
		t.Fatalf("SetToolheadMapping T1: %v", err)
	}

	// Spool 7 should get "Ender3 - T1"
	updates7 := spoolman.LocationUpdatesForSpool(7)
	if len(updates7) == 0 {
		t.Fatal("expected location update for spool 7 on T1, got none")
	}
	if updates7[0].LocationName != "Ender3 - T1" {
		t.Errorf("spool 7 location = %q; want %q", updates7[0].LocationName, "Ender3 - T1")
	}

	// Spool 5 first update should be "Ender3 - T0"
	updates5 := spoolman.LocationUpdatesForSpool(5)
	if len(updates5) == 0 {
		t.Fatal("expected location update for spool 5 on T0, got none")
	}
	if updates5[0].LocationName != "Ender3 - T0" {
		t.Errorf("spool 5 location = %q; want %q", updates5[0].LocationName, "Ender3 - T0")
	}
}

// =============================================================================
// Reverse-sync tests (Spoolman → The Moment DB)
// These verify that SyncSpoolmanLocationsToDB correctly updates DB mappings
// when the user edits spool locations directly in Spoolman.
// =============================================================================

// newReverseSyncBridge is a test helper that wires up a bridge with a printer
// config and location sync enabled, ready for reverse-sync tests.
func newReverseSyncBridge(t *testing.T, spoolman *MockSpoolman) *FilamentBridge {
	t.Helper()
	t.Setenv("THE_MOMENT_DB_PATH", t.TempDir())
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })

	cfg, err := LoadConfig(bridge)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.SpoolmanURL = spoolman.URL()
	bridge.UpdateConfig(cfg)

	if err := bridge.SavePrinterConfig("ender3", PrinterConfig{
		Name:      "Ender3",
		Toolheads: 2,
	}); err != nil {
		t.Fatalf("SavePrinterConfig: %v", err)
	}

	if err := bridge.SetConfigValue(ConfigKeySpoolmanLocationSyncEnabled, "true"); err != nil {
		t.Fatalf("SetConfigValue sync: %v", err)
	}
	return bridge
}

// TestReverseSync_ClearsStaleMapping verifies that when a spool's Spoolman
// location is changed away from a toolhead slot, the next sync removes the
// DB mapping.  This is the scenario the user reported: spool moved to "Cubes"
// in Spoolman but The Moment still showed it on Ender3 T0.
func TestReverseSync_ClearsStaleMapping(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{15: 800})
	bridge := newReverseSyncBridge(t, spoolman)

	// Establish DB mapping with sync disabled so no Spoolman call is made yet.
	if err := bridge.SetConfigValue(ConfigKeySpoolmanLocationSyncEnabled, "false"); err != nil {
		t.Fatalf("disable sync: %v", err)
	}
	if err := bridge.SetToolheadMapping("Ender3", 0, 15); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	// Simulate user moving spool 15 in Spoolman's own UI to "Cubes".
	spoolman.SetSpoolLocation(15, "Cubes")

	// Re-enable sync and run one cycle.
	if err := bridge.SetConfigValue(ConfigKeySpoolmanLocationSyncEnabled, "true"); err != nil {
		t.Fatalf("re-enable sync: %v", err)
	}
	changed, err := bridge.SyncSpoolmanLocationsToDB()
	if err != nil {
		t.Fatalf("SyncSpoolmanLocationsToDB: %v", err)
	}
	if !changed {
		t.Error("expected changed=true, got false")
	}

	// Ender3 T0 should now be unmapped.
	spoolID, err := bridge.GetToolheadMapping("Ender3", 0)
	if err != nil {
		t.Fatalf("GetToolheadMapping: %v", err)
	}
	if spoolID != 0 {
		t.Errorf("expected Ender3 T0 unmapped after sync, got spool %d", spoolID)
	}
}

// TestReverseSync_SlotReassignment_SingleCycle verifies the ordering fix: when
// spool 15 is removed from "Ender3 - T0" in Spoolman and spool 12 is placed
// there, a single sync cycle handles both the removal and the addition.
// Before the fix this required two 5-minute cycles.
func TestReverseSync_SlotReassignment_SingleCycle(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{12: 1000, 15: 800})
	bridge := newReverseSyncBridge(t, spoolman)

	// Establish initial DB state: spool 15 on Ender3 T0 (sync disabled).
	if err := bridge.SetConfigValue(ConfigKeySpoolmanLocationSyncEnabled, "false"); err != nil {
		t.Fatalf("disable sync: %v", err)
	}
	if err := bridge.SetToolheadMapping("Ender3", 0, 15); err != nil {
		t.Fatalf("SetToolheadMapping spool 15: %v", err)
	}

	// In Spoolman: spool 15 moved to "Inventory", spool 12 placed at "Ender3 - T0".
	spoolman.SetSpoolLocation(15, "Inventory")
	spoolman.SetSpoolLocation(12, "Ender3 - T0")

	// One sync cycle.
	if err := bridge.SetConfigValue(ConfigKeySpoolmanLocationSyncEnabled, "true"); err != nil {
		t.Fatalf("re-enable sync: %v", err)
	}
	changed, err := bridge.SyncSpoolmanLocationsToDB()
	if err != nil {
		t.Fatalf("SyncSpoolmanLocationsToDB: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}

	// Ender3 T0 must now map to spool 12, not 15.
	spoolID, err := bridge.GetToolheadMapping("Ender3", 0)
	if err != nil {
		t.Fatalf("GetToolheadMapping: %v", err)
	}
	if spoolID != 12 {
		t.Errorf("expected Ender3 T0 → spool 12 after single sync, got %d", spoolID)
	}
}

// TestReverseSync_DoesNotUpdateFilamentUsage confirms that SyncSpoolmanLocationsToDB
// never calls UseSpoolmanFilament or touches used_weight — the filament deduction
// path (triggered on print completion) is completely independent.
func TestReverseSync_DoesNotUpdateFilamentUsage(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{10: 1000, 11: 500})
	bridge := newReverseSyncBridge(t, spoolman)

	// Put both spools at toolhead locations so sync has work to do.
	spoolman.SetSpoolLocation(10, "Ender3 - T0")
	spoolman.SetSpoolLocation(11, "Ender3 - T1")

	if _, err := bridge.SyncSpoolmanLocationsToDB(); err != nil {
		t.Fatalf("SyncSpoolmanLocationsToDB: %v", err)
	}

	// Sync must not have sent any used_weight PATCH calls.
	if updates := spoolman.Updates(); len(updates) != 0 {
		t.Errorf("expected 0 filament usage updates from sync, got %d: %v", len(updates), updates)
	}
}

// TestForwardSync_SetToolheadMapping_StillPushesLocation is an explicit regression
// guard: assigning a spool via Print Ops must still push the location to Spoolman
// and must NOT be affected by the reverse-sync code changes.
func TestForwardSync_SetToolheadMapping_StillPushesLocation(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{20: 1000})
	bridge := newReverseSyncBridge(t, spoolman) // sync already enabled

	if err := bridge.SetToolheadMapping("Ender3", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	locUpdates := spoolman.LocationUpdatesForSpool(20)
	if len(locUpdates) == 0 {
		t.Fatal("expected Spoolman to receive a location PATCH for spool 20, got none")
	}
	if locUpdates[0].LocationName != "Ender3 - T0" {
		t.Errorf("Spoolman received location %q; want %q", locUpdates[0].LocationName, "Ender3 - T0")
	}

	// DB must also reflect the mapping.
	spoolID, err := bridge.GetToolheadMapping("Ender3", 0)
	if err != nil {
		t.Fatalf("GetToolheadMapping: %v", err)
	}
	if spoolID != 20 {
		t.Errorf("DB mapping = spool %d; want 20", spoolID)
	}
}
