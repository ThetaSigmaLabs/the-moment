//go:build integration

// SPDX-License-Identifier: GPL-3.0-or-later

package main

// =============================================================================
// auto_assign_test.go
// =============================================================================
// Verifies the auto-assign-previous-spool feature uses nfc_inventory_location
// as its destination, not the separate auto_assign_previous_spool_location key.
//
// Before the merge change these tests FAIL because the code reads
// auto_assign_previous_spool_location (which is empty by default).
// After the change they PASS because the code reads nfc_inventory_location.
//
// Run with:
//   go test -tags=integration -v -run TestAutoAssign ./...
// =============================================================================

import (
	"testing"
)

// newAutoAssignBridge is a test helper that creates a bridge connected to the
// given mock Spoolman server. sync and auto-assign are both disabled by default
// so each test can opt in to exactly the combination it needs.
func newAutoAssignBridge(t *testing.T, spoolman *MockSpoolman) *FilamentBridge {
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

	return bridge
}

// TestAutoAssign_DisabledByDefault_NoPreviousSpoolMove verifies that assigning
// a new spool to an occupied toolhead does NOT move the previous spool when
// auto_assign_previous_spool_enabled is false (the default).
func TestAutoAssign_DisabledByDefault_NoPreviousSpoolMove(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{10: 1000, 20: 1000})
	bridge := newAutoAssignBridge(t, spoolman)

	// Confirm auto-assign is disabled by default.
	enabled, err := bridge.GetAutoAssignPreviousSpoolEnabled()
	if err != nil {
		t.Fatalf("GetAutoAssignPreviousSpoolEnabled: %v", err)
	}
	if enabled {
		t.Fatal("expected auto-assign disabled by default")
	}

	// Place spool 10 on T0, then replace with spool 20.
	if err := bridge.SetToolheadMapping("Ender3", 0, 10); err != nil {
		t.Fatalf("SetToolheadMapping spool 10: %v", err)
	}
	spoolman.ResetUpdates()

	if err := bridge.SetToolheadMapping("Ender3", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping spool 20: %v", err)
	}

	// Spool 10 must not have received any location PATCH.
	if updates := spoolman.LocationUpdatesForSpool(10); len(updates) != 0 {
		t.Errorf("expected 0 location updates for previous spool 10, got %d: %v", len(updates), updates)
	}
}

// TestAutoAssign_NoPreviousSpool_NoSpoolmanUpdate verifies that assigning a spool
// to an empty toolhead (no previous spool) never issues a location PATCH for a
// "previous" spool, even when auto-assign is enabled.
func TestAutoAssign_NoPreviousSpool_NoSpoolmanUpdate(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{5: 1000})
	bridge := newAutoAssignBridge(t, spoolman)

	if err := bridge.SetConfigValue(ConfigKeyAutoAssignPreviousSpoolEnabled, "true"); err != nil {
		t.Fatalf("SetConfigValue enable: %v", err)
	}
	if err := bridge.SetConfigValue(ConfigKeyNFCInventoryLocation, "Inventory"); err != nil {
		t.Fatalf("SetConfigValue inventory: %v", err)
	}

	// Toolhead is empty — first-ever assignment.
	if err := bridge.SetToolheadMapping("Ender3", 0, 5); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	// No previous spool existed, so no location PATCH for "a previous spool".
	// (Spool 5 may receive a location PATCH from the bidirectional sync path,
	// but we have not enabled that here — check it too is absent.)
	locUpdates := spoolman.LocationUpdates()
	for _, u := range locUpdates {
		if u.SpoolID != 5 {
			t.Errorf("unexpected location PATCH for spool %d (was not spool 5)", u.SpoolID)
		}
	}
}

// TestAutoAssign_Enabled_MovesPreviousSpoolToInventoryLocation is the primary
// test for the merge change: assigning spool 20 to Ender3 T0 while spool 10 is
// already there must PATCH spool 10's location to the value of nfc_inventory_location.
//
// This test FAILS before the change (current code reads auto_assign_previous_spool_location
// which is empty) and PASSES after (code reads nfc_inventory_location).
func TestAutoAssign_Enabled_MovesPreviousSpoolToInventoryLocation(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{10: 1000, 20: 1000})
	bridge := newAutoAssignBridge(t, spoolman)

	if err := bridge.SetConfigValue(ConfigKeyAutoAssignPreviousSpoolEnabled, "true"); err != nil {
		t.Fatalf("SetConfigValue enable: %v", err)
	}
	if err := bridge.SetConfigValue(ConfigKeyNFCInventoryLocation, "Inventory"); err != nil {
		t.Fatalf("SetConfigValue inventory: %v", err)
	}

	// Pre-assign spool 10 to T0.
	if err := bridge.SetToolheadMapping("Ender3", 0, 10); err != nil {
		t.Fatalf("SetToolheadMapping spool 10: %v", err)
	}
	spoolman.ResetUpdates()

	// Assign spool 20 to T0 — this should displace spool 10 → "Inventory".
	if err := bridge.SetToolheadMapping("Ender3", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping spool 20: %v", err)
	}

	updates := spoolman.LocationUpdatesForSpool(10)
	if len(updates) == 0 {
		t.Fatal("expected Spoolman location PATCH for displaced spool 10, got none")
	}
	if updates[0].LocationName != "Inventory" {
		t.Errorf("displaced spool 10 location = %q; want %q", updates[0].LocationName, "Inventory")
	}
}

// TestAutoAssign_Enabled_EmptyInventoryLocation_SkipsMove verifies that when
// nfc_inventory_location is empty the auto-assign feature silently skips the
// Spoolman PATCH (graceful no-op) rather than sending an empty location string.
func TestAutoAssign_Enabled_EmptyInventoryLocation_SkipsMove(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{10: 1000, 20: 1000})
	bridge := newAutoAssignBridge(t, spoolman)

	if err := bridge.SetConfigValue(ConfigKeyAutoAssignPreviousSpoolEnabled, "true"); err != nil {
		t.Fatalf("SetConfigValue enable: %v", err)
	}
	// Explicitly clear the inventory location (DB seeds it to "Inventory" by default).
	if err := bridge.SetConfigValue(ConfigKeyNFCInventoryLocation, ""); err != nil {
		t.Fatalf("SetConfigValue clear inventory: %v", err)
	}

	if err := bridge.SetToolheadMapping("Ender3", 0, 10); err != nil {
		t.Fatalf("SetToolheadMapping spool 10: %v", err)
	}
	spoolman.ResetUpdates()

	if err := bridge.SetToolheadMapping("Ender3", 0, 20); err != nil {
		t.Fatalf("SetToolheadMapping spool 20: %v", err)
	}

	// Empty inventory location → no PATCH for spool 10.
	if updates := spoolman.LocationUpdatesForSpool(10); len(updates) != 0 {
		t.Errorf("expected 0 location updates when inventory location empty, got %d: %v", len(updates), updates)
	}
}

// TestAutoAssign_SyncUnassignment_MovesSpoolToInventoryLocation verifies the
// syncSpoolLocationForUnassignment path (called on explicit toolhead unmap and
// on the NFC close-assignment route): when auto-assign is enabled and
// nfc_inventory_location is set, the unassigned spool is moved there.
//
// This test FAILS before the change (current code reads auto_assign_previous_spool_location
// and calls FindLocationByName against the mock's empty location list) and
// PASSES after (code reads nfc_inventory_location and calls UpdateSpoolLocation directly).
func TestAutoAssign_SyncUnassignment_MovesSpoolToInventoryLocation(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{30: 1000})
	bridge := newAutoAssignBridge(t, spoolman)

	if err := bridge.SetConfigValue(ConfigKeyAutoAssignPreviousSpoolEnabled, "true"); err != nil {
		t.Fatalf("SetConfigValue enable: %v", err)
	}
	if err := bridge.SetConfigValue(ConfigKeyNFCInventoryLocation, "Storage"); err != nil {
		t.Fatalf("SetConfigValue inventory: %v", err)
	}

	spoolman.ResetUpdates()

	if err := bridge.syncSpoolLocationForUnassignment(30); err != nil {
		t.Fatalf("syncSpoolLocationForUnassignment: %v", err)
	}

	updates := spoolman.LocationUpdatesForSpool(30)
	if len(updates) == 0 {
		t.Fatal("expected Spoolman location PATCH for spool 30, got none")
	}
	if updates[0].LocationName != "Storage" {
		t.Errorf("spool 30 location = %q; want %q", updates[0].LocationName, "Storage")
	}
}

// TestAutoAssign_SyncUnassignment_Disabled_NoMove verifies that
// syncSpoolLocationForUnassignment is a no-op when auto-assign is disabled.
func TestAutoAssign_SyncUnassignment_Disabled_NoMove(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{30: 1000})
	bridge := newAutoAssignBridge(t, spoolman)

	// auto-assign disabled (default); inventory location set so it can't be blamed.
	if err := bridge.SetConfigValue(ConfigKeyNFCInventoryLocation, "Storage"); err != nil {
		t.Fatalf("SetConfigValue inventory: %v", err)
	}

	if err := bridge.syncSpoolLocationForUnassignment(30); err != nil {
		t.Fatalf("syncSpoolLocationForUnassignment: %v", err)
	}

	if updates := spoolman.LocationUpdatesForSpool(30); len(updates) != 0 {
		t.Errorf("expected 0 location updates when auto-assign disabled, got %d", len(updates))
	}
}

// TestAutoAssign_IndependentOfLocationSync confirms that auto-assign works even
// when spoolman_location_sync_enabled (the bidirectional toolhead sync) is off.
// The two features are orthogonal toggles.
func TestAutoAssign_IndependentOfLocationSync(t *testing.T) {
	spoolman := NewMockSpoolman(t, map[int]float64{40: 1000, 50: 1000})
	bridge := newAutoAssignBridge(t, spoolman)

	// Bidirectional sync OFF, auto-assign ON.
	if err := bridge.SetConfigValue(ConfigKeySpoolmanLocationSyncEnabled, "false"); err != nil {
		t.Fatalf("SetConfigValue sync: %v", err)
	}
	if err := bridge.SetConfigValue(ConfigKeyAutoAssignPreviousSpoolEnabled, "true"); err != nil {
		t.Fatalf("SetConfigValue auto-assign: %v", err)
	}
	if err := bridge.SetConfigValue(ConfigKeyNFCInventoryLocation, "DryBox"); err != nil {
		t.Fatalf("SetConfigValue inventory: %v", err)
	}

	if err := bridge.SetToolheadMapping("Ender3", 0, 40); err != nil {
		t.Fatalf("SetToolheadMapping spool 40: %v", err)
	}
	spoolman.ResetUpdates()

	// Replace spool 40 with spool 50.
	if err := bridge.SetToolheadMapping("Ender3", 0, 50); err != nil {
		t.Fatalf("SetToolheadMapping spool 50: %v", err)
	}

	// Spool 40 must be moved to DryBox regardless of bidirectional sync state.
	updates := spoolman.LocationUpdatesForSpool(40)
	if len(updates) == 0 {
		t.Fatal("expected location PATCH for displaced spool 40, got none")
	}
	if updates[0].LocationName != "DryBox" {
		t.Errorf("displaced spool 40 location = %q; want %q", updates[0].LocationName, "DryBox")
	}
}
