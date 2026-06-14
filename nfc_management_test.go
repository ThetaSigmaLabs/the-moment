// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newNFCMgmtTestBridge(t *testing.T, spoolmanURL string) *FilamentBridge {
	t.Helper()
	dbFile := filepath.Join(t.TempDir(), "nfc_mgmt_test.db")
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
	if spoolmanURL != "" {
		b.spoolman = NewSpoolmanClient(spoolmanURL, 5)
	}
	return b
}

// TestCreateFilamentTag_Link binds a tag to an existing Spoolman filament. No Spoolman
// call is made (spoolman client left nil — any call would panic).
func TestCreateFilamentTag_Link(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	tag, err := b.CreateFilamentTag(nfcStrPtr("Linked PLA"), 7, nil)
	if err != nil {
		t.Fatalf("CreateFilamentTag link: %v", err)
	}
	if tag.TagType != "filament" {
		t.Errorf("tag_type = %q, want 'filament'", tag.TagType)
	}
	if tag.BoundEntityType == nil || *tag.BoundEntityType != "spoolman_filament" {
		t.Errorf("bound_entity_type = %v, want 'spoolman_filament'", tag.BoundEntityType)
	}
	if tag.BoundEntityID == nil || *tag.BoundEntityID != 7 {
		t.Errorf("bound_entity_id = %v, want 7", tag.BoundEntityID)
	}
	if tag.TagID == "" {
		t.Error("tag_id should be generated")
	}
}

// TestCreateFilamentTag_Unbound creates a filament tag with no Spoolman binding (a filament
// type that may not exist yet). No Spoolman call is made (client left nil — any call panics).
func TestCreateFilamentTag_Unbound(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	tag, err := b.CreateFilamentTag(nfcStrPtr("Future PLA"), 0, nil)
	if err != nil {
		t.Fatalf("CreateFilamentTag unbound: %v", err)
	}
	if tag.TagType != "filament" {
		t.Errorf("tag_type = %q, want 'filament'", tag.TagType)
	}
	if tag.BoundEntityType != nil {
		t.Errorf("bound_entity_type = %v, want nil (unbound)", *tag.BoundEntityType)
	}
	if tag.BoundEntityID != nil {
		t.Errorf("bound_entity_id = %v, want nil (unbound)", *tag.BoundEntityID)
	}
	if tag.TagID == "" {
		t.Error("tag_id should be generated")
	}
}

// TestCreateFilamentTag_AuthorFullSpec authors a new Spoolman filament from a full spec,
// mapping the manufacturer to an existing vendor, then binds the tag and stores the spec.
func TestCreateFilamentTag_AuthorFullSpec(t *testing.T) {
	var postBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/vendor":
			fmt.Fprint(w, `[{"id":1,"name":"Polymaker"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/filament":
			postBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":99,"name":"Black","material":"PLA"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	b := newNFCMgmtTestBridge(t, srv.URL)
	spec := &TagFilamentSpec{
		Manufacturer: "Polymaker", Material: "PLA", ColorName: "Black", ColorHex: "#101010",
		Density: 1.24, DiameterMM: 1.75, DefaultWeightG: 1000, DefaultPrice: 25,
	}
	tag, err := b.CreateFilamentTag(nfcStrPtr("OPT Black"), 0, spec)
	if err != nil {
		t.Fatalf("CreateFilamentTag author: %v", err)
	}
	if tag.BoundEntityID == nil || *tag.BoundEntityID != 99 {
		t.Fatalf("bound_entity_id = %v, want 99", tag.BoundEntityID)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(postBody, &body); err != nil {
		t.Fatalf("POST body not JSON: %v", err)
	}
	if body["material"] != "PLA" {
		t.Errorf("material = %v, want PLA", body["material"])
	}
	if body["color_hex"] != "101010" {
		t.Errorf("color_hex = %v, want 101010 (no #)", body["color_hex"])
	}
	if fmt.Sprintf("%v", body["vendor_id"]) != "1" {
		t.Errorf("vendor_id = %v, want 1", body["vendor_id"])
	}
	if fmt.Sprintf("%v", body["weight"]) != "1000" {
		t.Errorf("weight = %v, want 1000", body["weight"])
	}
	if body["name"] != "Black" {
		t.Errorf("name = %v, want Black", body["name"])
	}

	spec2, err := b.GetTagFilamentSpec(tag.TagID)
	if err != nil || spec2 == nil {
		t.Fatalf("GetTagFilamentSpec: %v", err)
	}
	if spec2.Manufacturer != "Polymaker" {
		t.Errorf("stored manufacturer = %q, want Polymaker", spec2.Manufacturer)
	}
	if spec2.OpenPrintTagJSON == "" {
		t.Error("openprinttag_json should be populated")
	}
}

// TestCreateFilamentTag_AuthorMinSpec authors from the minimum spec (material + color),
// with no matching vendor and no optional fields. Optional Spoolman fields are omitted.
func TestCreateFilamentTag_AuthorMinSpec(t *testing.T) {
	var postBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/vendor":
			fmt.Fprint(w, `[]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/filament":
			postBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":42,"name":"Red","material":"PETG"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	b := newNFCMgmtTestBridge(t, srv.URL)
	spec := &TagFilamentSpec{Manufacturer: "Generic", Material: "PETG", ColorName: "Red"}
	tag, err := b.CreateFilamentTag(nil, 0, spec)
	if err != nil {
		t.Fatalf("CreateFilamentTag min spec: %v", err)
	}
	if tag.BoundEntityID == nil || *tag.BoundEntityID != 42 {
		t.Fatalf("bound_entity_id = %v, want 42", tag.BoundEntityID)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(postBody, &body); err != nil {
		t.Fatalf("POST body not JSON: %v", err)
	}
	if _, has := body["vendor_id"]; has {
		t.Error("vendor_id must be absent when no vendor matches")
	}
	if _, has := body["weight"]; has {
		t.Error("weight must be absent when not provided")
	}
	if _, has := body["price"]; has {
		t.Error("price must be absent when not provided")
	}
	if _, has := body["color_hex"]; has {
		t.Error("color_hex must be absent when not provided")
	}
	if fmt.Sprintf("%v", body["diameter"]) != "1.75" {
		t.Errorf("diameter = %v, want 1.75 default", body["diameter"])
	}
}

// TestFilamentTag_LabelUniqueness enforces label uniqueness within the filament type.
func TestFilamentTag_LabelUniqueness(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	if _, err := b.CreateFilamentTag(nfcStrPtr("Dup"), 7, nil); err != nil {
		t.Fatalf("first tag: %v", err)
	}
	_, err := b.CreateFilamentTag(nfcStrPtr("Dup"), 8, nil)
	if err == nil {
		t.Fatal("expected duplicate label within filament type to error")
	}
	if !isLabelConflict(err) {
		t.Errorf("error should be a label conflict, got: %v", err)
	}
}

// TestFilamentTag_DeleteLeavesSpoolmanUntouched deletes a filament tag and confirms the
// delete path makes no Spoolman call (spoolman left nil — a call would panic).
func TestFilamentTag_DeleteLeavesSpoolmanUntouched(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	tag, err := b.CreateFilamentTag(nfcStrPtr("ToDelete"), 7, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := b.DeleteNFCTag(tag.TagID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := b.GetNFCTag(tag.TagID)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("tag should be deleted, got %+v", got)
	}
}

// TestFilamentTag_PayloadStable confirms the write-to-NFC URL is deterministic for a
// tag_id, so redo/replace yields the same payload without creating a new row.
func TestFilamentTag_PayloadStable(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")
	tag, err := b.CreateFilamentTag(nil, 7, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	u1 := nfcTagURL("host:5000", tag.TagID)
	u2 := nfcTagURL("host:5000", tag.TagID)
	if u1 != u2 {
		t.Errorf("tag URL not stable: %q vs %q", u1, u2)
	}
	want := "http://host:5000/tag/" + tag.TagID
	if u1 != want {
		t.Errorf("tag URL = %q, want %q", u1, want)
	}
	// Redo must not create a duplicate row.
	tags, _ := b.ListNFCTagsByType("filament")
	if len(tags) != 1 {
		t.Errorf("expected exactly 1 filament tag, got %d", len(tags))
	}
}

// TestCreateFilament_Client covers the new Spoolman create-filament method directly.
func TestCreateFilament_Client(t *testing.T) {
	var postBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/filament" {
			postBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":12,"name":"New","material":"ABS"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewSpoolmanClient(srv.URL, 5)
	f, err := client.CreateFilament(map[string]interface{}{"material": "ABS", "name": "New"})
	if err != nil {
		t.Fatalf("CreateFilament: %v", err)
	}
	if f.ID != 12 {
		t.Errorf("created ID = %d, want 12", f.ID)
	}
	var body map[string]interface{}
	json.Unmarshal(postBody, &body)
	if body["material"] != "ABS" {
		t.Errorf("forwarded material = %v, want ABS", body["material"])
	}
}

// ─── Stage 3: spool tags ────────────────────────────────────────────────────────

func TestCreateSpool_Client(t *testing.T) {
	var postBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/spool" {
			postBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":77,"filament":{"id":3,"name":"PLA Black","material":"PLA"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewSpoolmanClient(srv.URL, 5)
	s, err := client.CreateSpool(map[string]interface{}{"filament_id": 3})
	if err != nil {
		t.Fatalf("CreateSpool: %v", err)
	}
	if s.ID != 77 {
		t.Errorf("created spool ID = %d, want 77", s.ID)
	}
	var body map[string]interface{}
	json.Unmarshal(postBody, &body)
	if fmt.Sprintf("%v", body["filament_id"]) != "3" {
		t.Errorf("forwarded filament_id = %v, want 3", body["filament_id"])
	}
}

func TestCreateSpoolTag_UnboundAndLink(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	// Unbound (pool) tag.
	u, err := b.CreateSpoolTag(nfcStrPtr("Reel A"), 0)
	if err != nil {
		t.Fatalf("unbound: %v", err)
	}
	if u.TagType != "spool" || u.BoundEntityID != nil {
		t.Errorf("unbound spool tag wrong: %+v", u)
	}

	// Linked tag.
	l, err := b.CreateSpoolTag(nfcStrPtr("Reel B"), 5)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if l.BoundEntityID == nil || *l.BoundEntityID != 5 || l.BoundEntityType == nil || *l.BoundEntityType != "spoolman_spool" {
		t.Errorf("linked spool tag wrong: %+v", l)
	}
}

func TestListUnboundSpoolTags_FiltersBoundAndArchived(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	u1, _ := b.CreateSpoolTag(nfcStrPtr("free1"), 0)
	_, _ = b.CreateSpoolTag(nfcStrPtr("free2"), 0)
	_, _ = b.CreateSpoolTag(nfcStrPtr("bound"), 9) // bound → excluded
	arch, _ := b.CreateSpoolTag(nfcStrPtr("archived"), 0)
	if err := b.SetNFCTagStatus(arch.TagID, "archived"); err != nil {
		t.Fatalf("archive: %v", err)
	}

	unbound, err := b.ListUnboundSpoolTags()
	if err != nil {
		t.Fatalf("list unbound: %v", err)
	}
	if len(unbound) != 2 {
		t.Fatalf("unbound count = %d, want 2 (free1, free2)", len(unbound))
	}

	// Binding free1 removes it from the available pool.
	if err := b.BindSpoolTag(u1.TagID, 12); err != nil {
		t.Fatalf("bind: %v", err)
	}
	unbound, _ = b.ListUnboundSpoolTags()
	if len(unbound) != 1 {
		t.Errorf("after bind, unbound count = %d, want 1", len(unbound))
	}
	got, _ := b.GetNFCTag(u1.TagID)
	if got.BoundEntityID == nil || *got.BoundEntityID != 12 {
		t.Errorf("bound_entity_id = %v, want 12", got.BoundEntityID)
	}
}

func TestBindSpoolTag_Errors(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	if err := b.BindSpoolTag("nonexistent", 5); err == nil {
		t.Error("binding a nonexistent tag should error")
	}
	// A filament tag cannot be bound as a spool.
	fil, _ := b.CreateFilamentTag(nfcStrPtr("F"), 1, nil)
	if err := b.BindSpoolTag(fil.TagID, 5); err == nil {
		t.Error("binding a filament tag as a spool should error")
	}
	// spool_id must be > 0.
	sp, _ := b.CreateSpoolTag(nfcStrPtr("S"), 0)
	if err := b.BindSpoolTag(sp.TagID, 0); err == nil {
		t.Error("binding with spool_id 0 should error")
	}
}

func TestCreateSpoolFromFilament(t *testing.T) {
	var postBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/spool" {
			postBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":88,"filament":{"id":4,"name":"PETG","material":"PETG"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	b := newNFCMgmtTestBridge(t, srv.URL)
	spool, err := b.CreateSpoolFromFilament(4)
	if err != nil {
		t.Fatalf("CreateSpoolFromFilament: %v", err)
	}
	if spool.ID != 88 {
		t.Errorf("spool ID = %d, want 88", spool.ID)
	}
	var body map[string]interface{}
	json.Unmarshal(postBody, &body)
	if fmt.Sprintf("%v", body["filament_id"]) != "4" {
		t.Errorf("filament_id forwarded = %v, want 4", body["filament_id"])
	}
	if _, err := b.CreateSpoolFromFilament(0); err == nil {
		t.Error("filament_id 0 should error without a Spoolman call")
	}
}

// TestFindVendorByName covers case-insensitive matching and the not-found / empty cases.
func TestFindVendorByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"name":"Polymaker"},{"id":2,"name":"Prusament"}]`)
	}))
	defer srv.Close()
	client := NewSpoolmanClient(srv.URL, 5)

	v, err := client.FindVendorByName("polymaker")
	if err != nil || v == nil || v.ID != 1 {
		t.Fatalf("case-insensitive match failed: v=%v err=%v", v, err)
	}
	v, err = client.FindVendorByName("Unknown")
	if err != nil || v != nil {
		t.Errorf("missing vendor should be (nil,nil), got v=%v err=%v", v, err)
	}
	v, err = client.FindVendorByName("   ")
	if err != nil || v != nil {
		t.Errorf("empty name should be (nil,nil), got v=%v err=%v", v, err)
	}
}

// TestRebindTag verifies bind → rebind → unbind via SetNFCTagBinding directly.
func TestRebindTag(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	// Create a spool tag bound to spool #10.
	tag, err := b.CreateSpoolTag(nfcStrPtr("rebind-me"), 10)
	if err != nil {
		t.Fatalf("CreateSpoolTag: %v", err)
	}
	if tag.BoundEntityID == nil || *tag.BoundEntityID != 10 {
		t.Fatalf("initial bound_entity_id = %v, want 10", tag.BoundEntityID)
	}

	// Rebind to spool #20.
	et := "spoolman_spool"
	id20 := 20
	if err := b.SetNFCTagBinding(tag.TagID, &et, &id20); err != nil {
		t.Fatalf("SetNFCTagBinding to 20: %v", err)
	}
	got, _ := b.GetNFCTag(tag.TagID)
	if got.BoundEntityID == nil || *got.BoundEntityID != 20 {
		t.Errorf("after rebind: bound_entity_id = %v, want 20", got.BoundEntityID)
	}
	if got.BoundEntityType == nil || *got.BoundEntityType != "spoolman_spool" {
		t.Errorf("after rebind: bound_entity_type = %v, want spoolman_spool", got.BoundEntityType)
	}

	// Unbind (nil, nil).
	if err := b.SetNFCTagBinding(tag.TagID, nil, nil); err != nil {
		t.Fatalf("SetNFCTagBinding unbind: %v", err)
	}
	got, _ = b.GetNFCTag(tag.TagID)
	if got.BoundEntityID != nil {
		t.Errorf("after unbind: bound_entity_id = %v, want nil", got.BoundEntityID)
	}
	if got.BoundEntityType != nil {
		t.Errorf("after unbind: bound_entity_type = %v, want nil", got.BoundEntityType)
	}
}

// ─── Stage 4: location tags ────────────────────────────────────────────────────

// TestCreateLocationTag_Kinds creates location tags of each kind and checks tag_type,
// location_kind, and that no Spoolman entity binding is set.
func TestCreateLocationTag_Kinds(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	cases := []struct {
		label string
		kind  string
	}{
		{"Core One L - T0", "toolhead"},
		{"Drybox Shelf A", "inventory"},
		{"Cold storage", "archive"},
		{"Empty bin", "trash"},
	}

	for _, tc := range cases {
		tag, err := b.CreateLocationTag(nfcStrPtr(tc.label), tc.kind)
		if err != nil {
			t.Fatalf("CreateLocationTag(%q, %q): %v", tc.label, tc.kind, err)
		}
		if tag.TagType != "location" {
			t.Errorf("%q: tag_type = %q, want location", tc.label, tag.TagType)
		}
		if tag.LocationKind == nil || *tag.LocationKind != tc.kind {
			t.Errorf("%q: location_kind = %v, want %q", tc.label, tag.LocationKind, tc.kind)
		}
		if tag.BoundEntityID != nil || tag.BoundEntityType != nil {
			t.Errorf("%q: should have no Spoolman binding, got type=%v id=%v", tc.label, tag.BoundEntityType, tag.BoundEntityID)
		}
		if tag.Label == nil || *tag.Label != tc.label {
			t.Errorf("%q: label = %v, want %q", tc.label, tag.Label, tc.label)
		}
	}
}

// TestCreateLocationTag_Unbound creates a location tag with no label (valid).
func TestCreateLocationTag_Unbound(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	tag, err := b.CreateLocationTag(nil, "inventory")
	if err != nil {
		t.Fatalf("CreateLocationTag nil label: %v", err)
	}
	if tag.Label != nil {
		t.Errorf("label = %v, want nil", tag.Label)
	}
	if tag.LocationKind == nil || *tag.LocationKind != "inventory" {
		t.Errorf("location_kind = %v, want inventory", tag.LocationKind)
	}
}

// TestSetLocationKind verifies location_kind is updated and can be cleared.
func TestSetLocationKind(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	tag, _ := b.CreateLocationTag(nfcStrPtr("Storage"), "inventory")

	// Change kind.
	kind := "archive"
	if err := b.SetNFCTagLocationKind(tag.TagID, &kind); err != nil {
		t.Fatalf("SetNFCTagLocationKind: %v", err)
	}
	got, _ := b.GetNFCTag(tag.TagID)
	if got.LocationKind == nil || *got.LocationKind != "archive" {
		t.Errorf("location_kind = %v, want archive", got.LocationKind)
	}

	// Clear kind.
	if err := b.SetNFCTagLocationKind(tag.TagID, nil); err != nil {
		t.Fatalf("SetNFCTagLocationKind nil: %v", err)
	}
	got, _ = b.GetNFCTag(tag.TagID)
	if got.LocationKind != nil {
		t.Errorf("location_kind = %v, want nil after clear", got.LocationKind)
	}
}

// TestListLocationTags_MultipleKinds inserts location tags and verifies listing.
func TestListLocationTags_MultipleKinds(t *testing.T) {
	b := newNFCMgmtTestBridge(t, "")

	b.CreateLocationTag(nfcStrPtr("Toolhead A"), "toolhead")
	b.CreateLocationTag(nfcStrPtr("Shelf B"), "inventory")
	b.CreateLocationTag(nil, "trash") // unlabeled

	tags, err := b.ListNFCTagsByType("location")
	if err != nil {
		t.Fatalf("ListNFCTagsByType: %v", err)
	}
	if len(tags) != 3 {
		t.Fatalf("count = %d, want 3", len(tags))
	}
	for _, tag := range tags {
		if tag.TagType != "location" {
			t.Errorf("tag_type = %q, want location", tag.TagType)
		}
	}
}
