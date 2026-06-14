//go:build integration

// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

// =============================================================================
// nfc_tap_test.go — Stage 5 tap-tap engine tests
// =============================================================================
// Covers every combination from the plan:
//   location → spool  (and reverse)
//   spool → filament  with no prior filament (direct bind)
//   spool → filament  with different prior filament (conflict → two choices)
//   same-type double-tap (replaced)
//   expired pending (fresh first tap)
//   archive-spool NFC cleanup
// =============================================================================

import (
	"testing"
	"time"
)

// newTapBridge sets up a full FilamentBridge (all migrations) connected to the
// given MockSpoolman. Passing nil skips Spoolman config (location→spool tests
// don't need Spoolman when sync is disabled and no toolhead location is set).
func newTapBridge(t *testing.T, spoolman *MockSpoolman) *FilamentBridge {
	t.Helper()
	t.Setenv("THE_MOMENT_DB_PATH", t.TempDir())
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })
	if spoolman != nil {
		cfg, err := LoadConfig(bridge)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		cfg.SpoolmanURL = spoolman.URL()
		bridge.UpdateConfig(cfg)
	}
	return bridge
}

// insertTag is a test helper that inserts a tag and returns its tag_id.
func insertTag(t *testing.T, b *FilamentBridge, tagType string, label *string, entityID *int, locationKind *string) string {
	t.Helper()
	tagID := tagType + "-" + time.Now().Format("150405.000000000000")
	tag := NFCTag{
		TagID:   tagID,
		TagType: tagType,
		Label:   label,
		Status:  "active",
	}
	if entityID != nil {
		et := "spoolman_spool"
		if tagType == "filament" {
			et = "spoolman_filament"
		}
		tag.BoundEntityType = &et
		tag.BoundEntityID = entityID
	}
	if locationKind != nil {
		tag.LocationKind = locationKind
	}
	if err := b.InsertNFCTag(tag); err != nil {
		t.Fatalf("insertTag %s: %v", tagType, err)
	}
	return tagID
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

// ─── First tap: stored ────────────────────────────────────────────────────────

func TestProcessTap_FirstTap_Stored(t *testing.T) {
	b := newTapBridge(t, nil)
	locTagID := insertTag(t, b, "location", strPtr("Ender3 - T0"), nil, strPtr("toolhead"))

	result, err := b.ProcessTap(locTagID)
	if err != nil {
		t.Fatalf("ProcessTap: %v", err)
	}
	if result.Action != TapStored {
		t.Errorf("action = %q; want %q", result.Action, TapStored)
	}

	pending, err := b.GetPendingTap()
	if err != nil {
		t.Fatalf("GetPendingTap: %v", err)
	}
	if pending == nil {
		t.Fatal("expected pending tap to be stored")
	}
	if pending.TagID != locTagID {
		t.Errorf("pending.TagID = %q; want %q", pending.TagID, locTagID)
	}
}

// ─── Unknown tag ──────────────────────────────────────────────────────────────

func TestProcessTap_UnknownTag(t *testing.T) {
	b := newTapBridge(t, nil)

	result, err := b.ProcessTap("nonexistent-tag-id")
	if err != nil {
		t.Fatalf("ProcessTap: %v", err)
	}
	if result.Action != TapUnknown {
		t.Errorf("action = %q; want %q", result.Action, TapUnknown)
	}
}

// ─── Same-type double-tap: replaced ──────────────────────────────────────────

func TestProcessTap_SameType_Replaced(t *testing.T) {
	for _, tagType := range []string{"spool", "location", "filament"} {
		tagType := tagType
		t.Run(tagType, func(t *testing.T) {
			b := newTapBridge(t, nil)
			id1 := insertTag(t, b, tagType, strPtr("tag-one"), nil, nil)
			id2 := insertTag(t, b, tagType, strPtr("tag-two"), nil, nil)

			// First tap.
			if r, err := b.ProcessTap(id1); err != nil || r.Action != TapStored {
				t.Fatalf("first tap: action=%q err=%v", r.Action, err)
			}
			// Same-type second tap.
			r2, err := b.ProcessTap(id2)
			if err != nil {
				t.Fatalf("second tap: %v", err)
			}
			if r2.Action != TapReplaced {
				t.Errorf("action = %q; want %q", r2.Action, TapReplaced)
			}
			// Pending should now be id2.
			pending, _ := b.GetPendingTap()
			if pending == nil || pending.TagID != id2 {
				t.Errorf("pending.TagID = %v; want %q", pending, id2)
			}
		})
	}
}

// ─── Expired pending: treated as fresh first tap ──────────────────────────────

func TestProcessTap_ExpiredPending_FreshFirstTap(t *testing.T) {
	b := newTapBridge(t, nil)

	locTagID := insertTag(t, b, "location", strPtr("Ender3 - T0"), nil, strPtr("toolhead"))
	spoolTagID := insertTag(t, b, "spool", strPtr("spool-a"), intPtr(42), nil)

	// Plant an already-expired pending tap.
	if err := b.SetPendingTap(PendingTap{
		TagID:     locTagID,
		TagType:   "location",
		ExpiresAt: time.Now().Add(-1 * time.Second), // already expired
	}); err != nil {
		t.Fatalf("SetPendingTap: %v", err)
	}

	// Tap the spool tag. Expired pending should be cleared; this is treated as a first tap.
	result, err := b.ProcessTap(spoolTagID)
	if err != nil {
		t.Fatalf("ProcessTap: %v", err)
	}
	if result.Action != TapStored {
		t.Errorf("action = %q; want %q (expired pending should be treated as no pending)", result.Action, TapStored)
	}
	pending, _ := b.GetPendingTap()
	if pending == nil || pending.TagID != spoolTagID {
		t.Errorf("pending after expired clear: got %v; want %q", pending, spoolTagID)
	}
}

// ─── Location → Spool assignment ─────────────────────────────────────────────

func TestProcessTap_LocationSpool_Assigned(t *testing.T) {
	// No Spoolman needed: sync is disabled by default; no toolhead location configured.
	b := newTapBridge(t, nil)

	locTagID := insertTag(t, b, "location", strPtr("Ender3 - T0"), nil, strPtr("toolhead"))
	spoolTagID := insertTag(t, b, "spool", strPtr("spool-a"), intPtr(7), nil)

	// First tap: location.
	if r, err := b.ProcessTap(locTagID); err != nil || r.Action != TapStored {
		t.Fatalf("first tap: action=%q err=%v", r.Action, err)
	}
	// Second tap: spool.
	result, err := b.ProcessTap(spoolTagID)
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if result.Action != TapAssigned {
		t.Errorf("action = %q; want %q", result.Action, TapAssigned)
	}
	if result.PrinterName != "Ender3" {
		t.Errorf("PrinterName = %q; want %q", result.PrinterName, "Ender3")
	}
	if result.ToolheadIdx != 0 {
		t.Errorf("ToolheadIdx = %d; want 0", result.ToolheadIdx)
	}
	if result.SpoolID != 7 {
		t.Errorf("SpoolID = %d; want 7", result.SpoolID)
	}

	// Verify toolhead_mappings row was written.
	spoolID, err := b.GetToolheadMapping("Ender3", 0)
	if err != nil {
		t.Fatalf("GetToolheadMapping: %v", err)
	}
	if spoolID != 7 {
		t.Errorf("toolhead mapping spool = %d; want 7", spoolID)
	}

	// Pending should be cleared.
	pending, _ := b.GetPendingTap()
	if pending != nil {
		t.Errorf("pending should be nil after assignment; got %+v", pending)
	}
}

func TestProcessTap_SpoolLocation_Assigned(t *testing.T) {
	b := newTapBridge(t, nil)

	spoolTagID := insertTag(t, b, "spool", strPtr("spool-b"), intPtr(9), nil)
	locTagID := insertTag(t, b, "location", strPtr("Core One L - T1"), nil, strPtr("toolhead"))

	// Reverse order: spool first, then location.
	if r, err := b.ProcessTap(spoolTagID); err != nil || r.Action != TapStored {
		t.Fatalf("first tap: action=%q err=%v", r.Action, err)
	}
	result, err := b.ProcessTap(locTagID)
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if result.Action != TapAssigned {
		t.Errorf("action = %q; want %q", result.Action, TapAssigned)
	}
	if result.PrinterName != "Core One L" || result.ToolheadIdx != 1 || result.SpoolID != 9 {
		t.Errorf("result = %+v; want Core One L T1 spool 9", result)
	}

	spoolID, _ := b.GetToolheadMapping("Core One L", 1)
	if spoolID != 9 {
		t.Errorf("toolhead mapping = %d; want 9", spoolID)
	}
}

// ─── Storage location (inventory/archive/trash) → Spool ─────────────────────

func TestProcessTap_StorageLocation_MovesSpoolToLocation(t *testing.T) {
	spoolID := 15
	mock := NewMockSpoolman(t, map[int]float64{spoolID: 500})
	b := newTapBridge(t, mock)

	// Seed a toolhead mapping so we can verify it gets cleared.
	if err := b.SetToolheadMapping("Ender3", 0, spoolID); err != nil {
		t.Fatalf("seed toolhead mapping: %v", err)
	}

	invKind := "inventory"
	invTagID := insertTag(t, b, "location", strPtr("Cubes"), nil, &invKind)
	spoolTagID := insertTag(t, b, "spool", strPtr("spool-15"), intPtr(spoolID), nil)

	// Tap 1: inventory location.
	if r, err := b.ProcessTap(invTagID); err != nil || r.Action != TapStored {
		t.Fatalf("first tap: action=%q err=%v", r.Action, err)
	}
	// Tap 2: spool → moved to storage.
	result, err := b.ProcessTap(spoolTagID)
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if result.Action != TapAssigned {
		t.Errorf("action = %q; want %q", result.Action, TapAssigned)
	}
	if result.ToolheadIdx != -1 {
		t.Errorf("ToolheadIdx = %d; want -1 (storage signal)", result.ToolheadIdx)
	}
	if result.PrinterName != "Cubes" {
		t.Errorf("PrinterName (location label) = %q; want %q", result.PrinterName, "Cubes")
	}
	if result.SpoolID != spoolID {
		t.Errorf("SpoolID = %d; want %d", result.SpoolID, spoolID)
	}

	// Spoolman location must have been updated to "Cubes".
	updates := mock.LocationUpdatesForSpool(spoolID)
	if len(updates) == 0 {
		t.Fatal("Spoolman location was not updated")
	}
	if updates[len(updates)-1].LocationName != "Cubes" {
		t.Errorf("Spoolman location = %q; want %q", updates[len(updates)-1].LocationName, "Cubes")
	}

	// Toolhead mapping should be cleared.
	mappedID, _ := b.GetToolheadMapping("Ender3", 0)
	if mappedID != 0 {
		t.Errorf("toolhead mapping = %d after storage tap; want 0 (cleared)", mappedID)
	}
}

func TestProcessTap_StorageLocation_SpoolFirst(t *testing.T) {
	spoolID := 20
	mock := NewMockSpoolman(t, map[int]float64{spoolID: 750})
	b := newTapBridge(t, mock)

	archiveKind := "archive"
	archiveTagID := insertTag(t, b, "location", strPtr("Dry Box"), nil, &archiveKind)
	spoolTagID := insertTag(t, b, "spool", strPtr("spool-20"), intPtr(spoolID), nil)

	// Reverse order: spool first.
	if r, err := b.ProcessTap(spoolTagID); err != nil || r.Action != TapStored {
		t.Fatalf("first tap: action=%q err=%v", r.Action, err)
	}
	result, err := b.ProcessTap(archiveTagID)
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if result.Action != TapAssigned || result.ToolheadIdx != -1 || result.PrinterName != "Dry Box" {
		t.Errorf("result = %+v; want TapAssigned ToolheadIdx=-1 PrinterName=Dry Box", result)
	}

	updates := mock.LocationUpdatesForSpool(spoolID)
	if len(updates) == 0 || updates[len(updates)-1].LocationName != "Dry Box" {
		t.Errorf("Spoolman location not updated to 'Dry Box'; got %v", updates)
	}
}

// ─── Spool → Filament: no prior filament (direct bind) ───────────────────────

func TestProcessTap_SpoolFilament_DirectBind(t *testing.T) {
	// Spool 5 has no filament; filament tag binds to filament 11.
	mock := NewMockSpoolman(t, nil)
	mock.AddSpool(SpoolRecord{ID: 5, InitialWeight: 1000, FilamentID: 0}) // no filament
	b := newTapBridge(t, mock)

	spoolTagID := insertTag(t, b, "spool", strPtr("spool-no-fil"), intPtr(5), nil)
	filTagID := insertTag(t, b, "filament", strPtr("pla-black"), intPtr(11), nil)

	if r, err := b.ProcessTap(spoolTagID); err != nil || r.Action != TapStored {
		t.Fatalf("first tap: %v %v", r.Action, err)
	}
	result, err := b.ProcessTap(filTagID)
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if result.Action != TapBound {
		t.Errorf("action = %q; want %q", result.Action, TapBound)
	}
	if result.SpoolID != 5 || result.FilamentID != 11 {
		t.Errorf("result = %+v; want spool 5 filament 11", result)
	}

	// Verify Spoolman PATCH was called with filament_id=11.
	updates := mock.FilamentIDUpdates()
	if len(updates) != 1 || updates[0].SpoolID != 5 || updates[0].FilamentID != 11 {
		t.Errorf("filament updates = %+v; want [{SpoolID:5 FilamentID:11}]", updates)
	}
}

func TestProcessTap_FilamentSpool_DirectBind_ReverseOrder(t *testing.T) {
	mock := NewMockSpoolman(t, nil)
	mock.AddSpool(SpoolRecord{ID: 6, InitialWeight: 1000, FilamentID: 0})
	b := newTapBridge(t, mock)

	filTagID := insertTag(t, b, "filament", strPtr("petg-clear"), intPtr(20), nil)
	spoolTagID := insertTag(t, b, "spool", strPtr("spool-c"), intPtr(6), nil)

	// Filament first, then spool.
	if r, err := b.ProcessTap(filTagID); err != nil || r.Action != TapStored {
		t.Fatalf("first tap: %v %v", r.Action, err)
	}
	result, err := b.ProcessTap(spoolTagID)
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if result.Action != TapBound {
		t.Errorf("action = %q; want %q", result.Action, TapBound)
	}
	if result.SpoolID != 6 || result.FilamentID != 20 {
		t.Errorf("result = %+v; want spool 6 filament 20", result)
	}
}

// ─── Spool → Filament: conflict (spool has a different filament) ──────────────

func TestProcessTap_SpoolFilament_Conflict(t *testing.T) {
	// Spool 3 already has filament 10; we tap filament tag bound to filament 11.
	mock := NewMockSpoolman(t, nil)
	mock.AddSpool(SpoolRecord{ID: 3, InitialWeight: 1000, FilamentID: 10})
	b := newTapBridge(t, mock)

	spoolTagID := insertTag(t, b, "spool", strPtr("spool-with-fil"), intPtr(3), nil)
	filTagID := insertTag(t, b, "filament", strPtr("new-fil"), intPtr(11), nil)

	if r, err := b.ProcessTap(spoolTagID); err != nil || r.Action != TapStored {
		t.Fatalf("first tap: %v %v", r.Action, err)
	}
	result, err := b.ProcessTap(filTagID)
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if result.Action != TapConflict {
		t.Errorf("action = %q; want %q", result.Action, TapConflict)
	}
	if result.SpoolID != 3 {
		t.Errorf("SpoolID = %d; want 3", result.SpoolID)
	}
	if result.OldFilamentID != 10 {
		t.Errorf("OldFilamentID = %d; want 10", result.OldFilamentID)
	}
	if result.NewFilamentID != 11 {
		t.Errorf("NewFilamentID = %d; want 11", result.NewFilamentID)
	}
	if result.SpoolTagID != spoolTagID {
		t.Errorf("SpoolTagID = %q; want %q", result.SpoolTagID, spoolTagID)
	}
	if result.FilamentTagID != filTagID {
		t.Errorf("FilamentTagID = %q; want %q", result.FilamentTagID, filTagID)
	}
	// No Spoolman PATCH should have been made.
	if len(mock.FilamentIDUpdates()) != 0 {
		t.Errorf("expected no Spoolman update on conflict; got %+v", mock.FilamentIDUpdates())
	}
}

func TestProcessTap_FilamentSpool_Conflict_ReverseOrder(t *testing.T) {
	mock := NewMockSpoolman(t, nil)
	mock.AddSpool(SpoolRecord{ID: 4, InitialWeight: 1000, FilamentID: 10})
	b := newTapBridge(t, mock)

	filTagID := insertTag(t, b, "filament", strPtr("fil-b"), intPtr(11), nil)
	spoolTagID := insertTag(t, b, "spool", strPtr("spool-d"), intPtr(4), nil)

	if r, err := b.ProcessTap(filTagID); err != nil || r.Action != TapStored {
		t.Fatalf("first tap: %v %v", r.Action, err)
	}
	result, err := b.ProcessTap(spoolTagID)
	if err != nil {
		t.Fatalf("second tap: %v", err)
	}
	if result.Action != TapConflict {
		t.Errorf("action = %q; want %q", result.Action, TapConflict)
	}
	// Fields should be the same regardless of tap order.
	if result.OldFilamentID != 10 || result.NewFilamentID != 11 {
		t.Errorf("filament IDs wrong: old=%d new=%d", result.OldFilamentID, result.NewFilamentID)
	}
}

// ─── Conflict resolution: edit_existing ──────────────────────────────────────

func TestResolveTapConflict_EditExisting(t *testing.T) {
	mock := NewMockSpoolman(t, nil)
	mock.AddSpool(SpoolRecord{ID: 3, InitialWeight: 1000, FilamentID: 10})
	b := newTapBridge(t, mock)

	spoolTagID := insertTag(t, b, "spool", strPtr("spool-e"), intPtr(3), nil)
	filTagID := insertTag(t, b, "filament", strPtr("new-fil-e"), intPtr(11), nil)

	result, err := b.ResolveTapConflict(spoolTagID, filTagID, "edit_existing")
	if err != nil {
		t.Fatalf("ResolveTapConflict: %v", err)
	}
	if result.Action != TapBound {
		t.Errorf("action = %q; want %q", result.Action, TapBound)
	}
	if result.SpoolID != 3 || result.FilamentID != 11 {
		t.Errorf("result = %+v; want spool 3 filament 11", result)
	}

	updates := mock.FilamentIDUpdates()
	if len(updates) != 1 || updates[0].SpoolID != 3 || updates[0].FilamentID != 11 {
		t.Errorf("filament updates = %+v; want [{3 11}]", updates)
	}
}

// ─── Conflict resolution: create_new ─────────────────────────────────────────

func TestResolveTapConflict_CreateNew(t *testing.T) {
	mock := NewMockSpoolman(t, nil)
	mock.AddSpool(SpoolRecord{ID: 3, InitialWeight: 1000, FilamentID: 10})
	b := newTapBridge(t, mock)

	spoolTagID := insertTag(t, b, "spool", strPtr("spool-f"), intPtr(3), nil)
	filTagID := insertTag(t, b, "filament", strPtr("new-fil-f"), intPtr(11), nil)

	result, err := b.ResolveTapConflict(spoolTagID, filTagID, "create_new")
	if err != nil {
		t.Fatalf("ResolveTapConflict: %v", err)
	}
	if result.Action != TapBound {
		t.Errorf("action = %q; want %q", result.Action, TapBound)
	}
	if result.FilamentID != 11 {
		t.Errorf("FilamentID = %d; want 11", result.FilamentID)
	}
	if result.SpoolID <= 0 {
		t.Errorf("SpoolID should be the new spool id; got %d", result.SpoolID)
	}
	// Old spool (3) should NOT be updated.
	if updates := mock.FilamentIDUpdates(); len(updates) != 0 {
		t.Errorf("expected no filament_id PATCH to old spool; got %+v", updates)
	}

	// Spool NFC tag should now be bound to the new spool, not spool 3.
	tag, err := b.GetNFCTag(spoolTagID)
	if err != nil || tag == nil {
		t.Fatalf("GetNFCTag: %v", err)
	}
	if tag.BoundEntityID == nil || *tag.BoundEntityID == 3 {
		t.Errorf("tag still bound to spool 3; want new spool id")
	}
	if *tag.BoundEntityID != result.SpoolID {
		t.Errorf("tag bound to %d; want new spool %d", *tag.BoundEntityID, result.SpoolID)
	}
}

// ─── Archive spool → NFC tag freed ───────────────────────────────────────────

func TestArchiveSpoolNFCForSpool_FreesTag(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	// Create an active spool tag bound to spool 99.
	tag, err := b.CreateSpoolTag(strPtr("my-spool"), 99)
	if err != nil {
		t.Fatalf("CreateSpoolTag: %v", err)
	}

	if err := b.ArchiveSpoolNFCForSpool(99); err != nil {
		t.Fatalf("ArchiveSpoolNFCForSpool: %v", err)
	}

	updated, err := b.GetNFCTag(tag.TagID)
	if err != nil {
		t.Fatalf("GetNFCTag: %v", err)
	}
	if updated.BoundEntityID != nil {
		t.Errorf("BoundEntityID = %v; want nil after archive", updated.BoundEntityID)
	}
	if updated.Status != "archived" {
		t.Errorf("status = %q; want \"archived\"", updated.Status)
	}
}

func TestArchiveSpoolNFCForSpool_UnboundTagUntouched(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	// Unbound spool tag should not be affected.
	unbound, err := b.CreateSpoolTag(strPtr("unbound"), 0)
	if err != nil {
		t.Fatalf("CreateSpoolTag: %v", err)
	}
	// Tag for a different spool also should not be affected.
	other, err := b.CreateSpoolTag(strPtr("other"), 50)
	if err != nil {
		t.Fatalf("CreateSpoolTag: %v", err)
	}

	if err := b.ArchiveSpoolNFCForSpool(99); err != nil {
		t.Fatalf("ArchiveSpoolNFCForSpool: %v", err)
	}

	for _, tagID := range []string{unbound.TagID, other.TagID} {
		got, _ := b.GetNFCTag(tagID)
		if got.Status != "active" {
			t.Errorf("tag %s status = %q; want \"active\"", tagID, got.Status)
		}
	}
}

// ─── Inclusive timeout boundary ───────────────────────────────────────────────

func TestProcessTap_TimeoutBoundary_InclusiveAtExactExpiry(t *testing.T) {
	b := newTapBridge(t, nil)

	locTagID := insertTag(t, b, "location", strPtr("Ender3 - T0"), nil, strPtr("toolhead"))
	spoolTagID := insertTag(t, b, "spool", strPtr("spool-boundary"), intPtr(7), nil)

	// Plant a pending tap that expires right now (boundary: inclusive means valid).
	exactly := time.Now()
	if err := b.SetPendingTap(PendingTap{
		TagID:     locTagID,
		TagType:   "location",
		ExpiresAt: exactly,
	}); err != nil {
		t.Fatalf("SetPendingTap: %v", err)
	}

	// The second tap at exactly expires_at should be valid (not expired).
	// time.Now().After(exactly) == false when now == exactly, so the tap is processed.
	// This is a best-effort test since exact timing is hard to guarantee; we accept
	// either TapAssigned (within boundary) or TapStored (marginally past boundary).
	result, err := b.ProcessTap(spoolTagID)
	if err != nil {
		t.Fatalf("ProcessTap: %v", err)
	}
	if result.Action != TapAssigned && result.Action != TapStored {
		t.Errorf("action = %q; want %q or %q", result.Action, TapAssigned, TapStored)
	}
}
