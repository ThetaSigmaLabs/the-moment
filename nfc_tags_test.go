// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func nfcStrPtr(s string) *string { return &s }
func nfcIntPtr(i int) *int       { return &i }

// newNFCTagTestBridge builds a minimal FilamentBridge backed by a temp SQLite DB with
// only the NFC tags schema migrated. No network, no Spoolman, no full init chain.
func newNFCTagTestBridge(t *testing.T) *FilamentBridge {
	t.Helper()
	dbFile := filepath.Join(t.TempDir(), "nfc_tags_test.db")
	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	b := &FilamentBridge{db: db}
	if err := b.migrateNFCTags(); err != nil {
		t.Fatalf("migrateNFCTags: %v", err)
	}
	return b
}

func TestNFCTags_InsertAndGetEachType(t *testing.T) {
	b := newNFCTagTestBridge(t)

	cases := []struct{ id, typ string }{
		{"spool-1", "spool"},
		{"loc-1", "location"},
		{"fil-1", "filament"},
	}
	for _, c := range cases {
		if err := b.InsertNFCTag(NFCTag{TagID: c.id, TagType: c.typ}); err != nil {
			t.Fatalf("insert %s: %v", c.typ, err)
		}
		got, err := b.GetNFCTag(c.id)
		if err != nil {
			t.Fatalf("get %s: %v", c.id, err)
		}
		if got == nil {
			t.Fatalf("get %s: expected row, got nil", c.id)
		}
		if got.TagType != c.typ {
			t.Errorf("%s: tag_type = %q, want %q", c.id, got.TagType, c.typ)
		}
		if got.Status != "active" {
			t.Errorf("%s: status = %q, want default 'active'", c.id, got.Status)
		}
		if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
			t.Errorf("%s: created_at/updated_at should be populated by default", c.id)
		}
	}
}

func TestNFCTags_GetMissingReturnsCleanNone(t *testing.T) {
	b := newNFCTagTestBridge(t)
	got, err := b.GetNFCTag("does-not-exist")
	if err != nil {
		t.Fatalf("expected no error for missing tag, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing tag, got %+v", got)
	}
}

func TestNFCTags_TagIDUniqueness(t *testing.T) {
	b := newNFCTagTestBridge(t)

	if err := b.InsertNFCTag(NFCTag{TagID: "dup", TagType: "spool"}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// Same tag_id, different type — must error, not silently overwrite.
	if err := b.InsertNFCTag(NFCTag{TagID: "dup", TagType: "location"}); err == nil {
		t.Fatal("expected duplicate tag_id insert to error")
	}
	got, err := b.GetNFCTag("dup")
	if err != nil || got == nil {
		t.Fatalf("get dup: %v", err)
	}
	if got.TagType != "spool" {
		t.Errorf("original row overwritten: tag_type = %q, want 'spool'", got.TagType)
	}
}

func TestNFCTags_LabelUniqueWithinTypeNonNull(t *testing.T) {
	b := newNFCTagTestBridge(t)

	lbl := "Black PLA"
	if err := b.InsertNFCTag(NFCTag{TagID: "s1", TagType: "spool", Label: nfcStrPtr(lbl)}); err != nil {
		t.Fatalf("insert s1: %v", err)
	}
	// Same label, same type — must error.
	if err := b.InsertNFCTag(NFCTag{TagID: "s2", TagType: "spool", Label: nfcStrPtr(lbl)}); err == nil {
		t.Fatal("expected duplicate label within same type to error")
	}
	// Same label, different type — allowed.
	if err := b.InsertNFCTag(NFCTag{TagID: "f1", TagType: "filament", Label: nfcStrPtr(lbl)}); err != nil {
		t.Fatalf("same label different type should be allowed: %v", err)
	}
	// SetNFCTagLabel onto an existing label within the type — must error.
	if err := b.InsertNFCTag(NFCTag{TagID: "s3", TagType: "spool"}); err != nil {
		t.Fatalf("insert s3: %v", err)
	}
	if err := b.SetNFCTagLabel("s3", nfcStrPtr(lbl)); err == nil {
		t.Fatal("expected SetNFCTagLabel to a duplicate label to error")
	}
}

func TestNFCTags_DuplicateNullLabelsAllowed(t *testing.T) {
	b := newNFCTagTestBridge(t)

	if err := b.InsertNFCTag(NFCTag{TagID: "n1", TagType: "spool"}); err != nil {
		t.Fatalf("insert n1: %v", err)
	}
	if err := b.InsertNFCTag(NFCTag{TagID: "n2", TagType: "spool"}); err != nil {
		t.Fatalf("duplicate null labels within a type must be allowed: %v", err)
	}
}

func TestNFCTags_StatusConstraint(t *testing.T) {
	b := newNFCTagTestBridge(t)

	// Invalid status on insert — rejected by CHECK.
	if err := b.InsertNFCTag(NFCTag{TagID: "bad", TagType: "spool", Status: "weird"}); err == nil {
		t.Fatal("expected invalid status on insert to error")
	}
	// Valid 'archived' on insert.
	if err := b.InsertNFCTag(NFCTag{TagID: "arch", TagType: "spool", Status: "archived"}); err != nil {
		t.Fatalf("archived status should be accepted: %v", err)
	}
	// Invalid status via update — rejected.
	if err := b.InsertNFCTag(NFCTag{TagID: "st", TagType: "spool"}); err != nil {
		t.Fatalf("insert st: %v", err)
	}
	if err := b.SetNFCTagStatus("st", "nope"); err == nil {
		t.Fatal("expected invalid status update to error")
	}
	if err := b.SetNFCTagStatus("st", "archived"); err != nil {
		t.Fatalf("valid status update failed: %v", err)
	}
	got, err := b.GetNFCTag("st")
	if err != nil || got == nil {
		t.Fatalf("get st: %v", err)
	}
	if got.Status != "archived" {
		t.Errorf("status = %q, want 'archived'", got.Status)
	}
}

func TestNFCTags_NullableFieldsRoundTrip(t *testing.T) {
	b := newNFCTagTestBridge(t)

	// All nullable fields nil.
	if err := b.InsertNFCTag(NFCTag{TagID: "x1", TagType: "location"}); err != nil {
		t.Fatalf("insert x1: %v", err)
	}
	got, err := b.GetNFCTag("x1")
	if err != nil || got == nil {
		t.Fatalf("get x1: %v", err)
	}
	if got.Label != nil || got.BoundEntityType != nil || got.BoundEntityID != nil || got.LocationKind != nil {
		t.Errorf("nullable fields should read back nil, got %+v", got)
	}

	// Set a binding, read it back.
	if err := b.SetNFCTagBinding("x1", nfcStrPtr("spoolman_spool"), nfcIntPtr(42)); err != nil {
		t.Fatalf("set binding: %v", err)
	}
	got, _ = b.GetNFCTag("x1")
	if got.BoundEntityType == nil || *got.BoundEntityType != "spoolman_spool" {
		t.Errorf("bound_entity_type = %v, want 'spoolman_spool'", got.BoundEntityType)
	}
	if got.BoundEntityID == nil || *got.BoundEntityID != 42 {
		t.Errorf("bound_entity_id = %v, want 42", got.BoundEntityID)
	}

	// Clear the binding back to null.
	if err := b.SetNFCTagBinding("x1", nil, nil); err != nil {
		t.Fatalf("clear binding: %v", err)
	}
	got, _ = b.GetNFCTag("x1")
	if got.BoundEntityType != nil || got.BoundEntityID != nil {
		t.Errorf("binding should be cleared to nil, got %+v", got)
	}
}

func TestNFCTags_ListByTypeAndDelete(t *testing.T) {
	b := newNFCTagTestBridge(t)

	for _, id := range []string{"a", "b", "c"} {
		if err := b.InsertNFCTag(NFCTag{TagID: id, TagType: "spool"}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	if err := b.InsertNFCTag(NFCTag{TagID: "loc", TagType: "location"}); err != nil {
		t.Fatalf("insert loc: %v", err)
	}

	spools, err := b.ListNFCTagsByType("spool")
	if err != nil {
		t.Fatalf("list spool: %v", err)
	}
	if len(spools) != 3 {
		t.Errorf("listed %d spool tags, want 3", len(spools))
	}

	if err := b.DeleteNFCTag("a"); err != nil {
		t.Fatalf("delete a: %v", err)
	}
	got, _ := b.GetNFCTag("a")
	if got != nil {
		t.Errorf("deleted tag should be gone, got %+v", got)
	}
	spools, _ = b.ListNFCTagsByType("spool")
	if len(spools) != 2 {
		t.Errorf("after delete, listed %d spool tags, want 2", len(spools))
	}
}

func TestNFCTags_PendingTapLifecycle(t *testing.T) {
	b := newNFCTagTestBridge(t)

	// Empty pending → clean none, not an error.
	got, err := b.GetPendingTap()
	if err != nil {
		t.Fatalf("get empty pending: %v", err)
	}
	if got != nil {
		t.Fatalf("empty pending should return nil, got %+v", got)
	}

	exp := time.Now().Add(15 * time.Second)
	if err := b.SetPendingTap(PendingTap{TagID: "t1", TagType: "spool", EntityID: nfcIntPtr(7), ExpiresAt: exp}); err != nil {
		t.Fatalf("set pending: %v", err)
	}
	got, err = b.GetPendingTap()
	if err != nil || got == nil {
		t.Fatalf("get pending after set: %v", err)
	}
	if got.TagID != "t1" || got.TagType != "spool" || got.EntityID == nil || *got.EntityID != 7 {
		t.Errorf("pending round-trip mismatch: %+v", got)
	}
	if d := got.ExpiresAt.Sub(exp); d > time.Second || d < -time.Second {
		t.Errorf("expires_at drifted by %v", d)
	}

	// Setting again replaces the single row; nil entity id round-trips as nil.
	if err := b.SetPendingTap(PendingTap{TagID: "t2", TagType: "filament", ExpiresAt: exp}); err != nil {
		t.Fatalf("replace pending: %v", err)
	}
	got, _ = b.GetPendingTap()
	if got == nil || got.TagID != "t2" || got.EntityID != nil {
		t.Errorf("pending replace mismatch: %+v", got)
	}

	// Clear → none. Clearing again is a no-op.
	if err := b.ClearPendingTap(); err != nil {
		t.Fatalf("clear pending: %v", err)
	}
	got, err = b.GetPendingTap()
	if err != nil || got != nil {
		t.Fatalf("after clear, expected clean none, got %+v err %v", got, err)
	}
	if err := b.ClearPendingTap(); err != nil {
		t.Fatalf("clear on empty should be no-op: %v", err)
	}
}
