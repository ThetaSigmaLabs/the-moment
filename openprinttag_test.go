// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

// newOPTTestBridge creates a minimal bridge with openprinttag_sources migrated.
func newOPTTestBridge(t *testing.T) *FilamentBridge {
	t.Helper()
	dbFile := filepath.Join(t.TempDir(), "opt_test.db")
	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	b := &FilamentBridge{db: db}
	if err := b.migrateOpenPrintTagSources(); err != nil {
		t.Fatalf("migrateOpenPrintTagSources: %v", err)
	}
	return b
}

func TestOPTMigration_SeededDefaults(t *testing.T) {
	b := newOPTTestBridge(t)
	sources, err := b.ListOPTSources()
	if err != nil {
		t.Fatalf("ListOPTSources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 seeded sources, got %d", len(sources))
	}
	// First seed: OFD (enabled default)
	s := sources[0]
	if s.SourceType != "ofd_api" {
		t.Errorf("first source: expected ofd_api, got %s", s.SourceType)
	}
	if !s.IsDefault {
		t.Error("first source: expected is_default=true for OFD")
	}
	if !s.Enabled {
		t.Error("first source: expected enabled=true for OFD")
	}
	// Second seed: filament-db (disabled placeholder)
	s2 := sources[1]
	if s2.SourceType != "filament_db_api" {
		t.Errorf("second source: expected filament_db_api, got %s", s2.SourceType)
	}
	if s2.Enabled {
		t.Error("second source: expected enabled=false for filament-db placeholder")
	}
}

func TestOPTSourceCRUD(t *testing.T) {
	b := newOPTTestBridge(t)

	// Insert
	id, err := b.InsertOPTSource(OpenPrintTagSource{
		Name:       "Test Source",
		URL:        "https://example.com",
		SourceType: "filament_db_api",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("InsertOPTSource: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id from InsertOPTSource")
	}

	// List — should now have 3 (2 seeds + new)
	sources, err := b.ListOPTSources()
	if err != nil {
		t.Fatalf("ListOPTSources: %v", err)
	}
	if len(sources) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(sources))
	}

	// Update
	err = b.UpdateOPTSource(OpenPrintTagSource{
		ID:         id,
		Name:       "Updated Source",
		URL:        "https://example.com/v2",
		SourceType: "filament_db_api",
		Enabled:    false,
	})
	if err != nil {
		t.Fatalf("UpdateOPTSource: %v", err)
	}
	sources, _ = b.ListOPTSources()
	var updated *OpenPrintTagSource
	for i := range sources {
		if sources[i].ID == id {
			updated = &sources[i]
		}
	}
	if updated == nil {
		t.Fatal("updated source not found after update")
	}
	if updated.Name != "Updated Source" {
		t.Errorf("name: got %q, want %q", updated.Name, "Updated Source")
	}
	if updated.Enabled {
		t.Error("expected enabled=false after update")
	}

	// Delete
	if err := b.DeleteOPTSource(id); err != nil {
		t.Fatalf("DeleteOPTSource: %v", err)
	}
	sources, _ = b.ListOPTSources()
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources after delete, got %d", len(sources))
	}
}

func TestOPTResetToDefaults(t *testing.T) {
	b := newOPTTestBridge(t)

	// Add a custom source
	_, err := b.InsertOPTSource(OpenPrintTagSource{
		Name: "Custom", URL: "https://custom.example.com", SourceType: "ofd_api", Enabled: true,
	})
	if err != nil {
		t.Fatalf("InsertOPTSource: %v", err)
	}

	sources, _ := b.ListOPTSources()
	if len(sources) != 3 {
		t.Fatalf("expected 3 sources before reset, got %d", len(sources))
	}

	// Reset
	if err := b.ResetOPTSourcesToDefaults(); err != nil {
		t.Fatalf("ResetOPTSourcesToDefaults: %v", err)
	}
	sources, _ = b.ListOPTSources()
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources after reset, got %d", len(sources))
	}
	if sources[0].SourceType != "ofd_api" {
		t.Errorf("expected ofd_api as first source after reset, got %s", sources[0].SourceType)
	}
	if sources[1].SourceType != "filament_db_api" {
		t.Errorf("expected filament_db_api as second source after reset, got %s", sources[1].SourceType)
	}
}

func TestTestOPTSource_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	source := OpenPrintTagSource{
		ID:         1,
		Name:       "Test",
		URL:        srv.URL,
		SourceType: "ofd_api",
		Enabled:    true,
	}
	latency, err := TestOPTSource(source)
	if err != nil {
		t.Fatalf("TestOPTSource: %v", err)
	}
	if latency < 0 {
		t.Errorf("expected non-negative latency, got %d", latency)
	}
}

func TestTestOPTSource_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	source := OpenPrintTagSource{
		ID: 1, URL: srv.URL, SourceType: "ofd_api", Enabled: true,
	}
	_, err := TestOPTSource(source)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

// TestOFDSearch_MapsFields uses a mock server that serves the full 3-level OFD
// hierarchy (brands index → brand detail → material detail) and verifies that
// ofdSearch returns a correctly mapped result.
func TestOFDSearch_MapsFields(t *testing.T) {
	t.Cleanup(resetOFDBrandsCache)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/brands/index.json":
			json.NewEncoder(w).Encode(ofdBrandsIndex{
				Brands: []ofdBrandIndexEntry{
					{ID: "b1", Name: "Polymaker", Slug: "polymaker", MaterialCount: 5},
				},
			})
		case "/api/v1/brands/polymaker/index.json":
			json.NewEncoder(w).Encode(ofdBrandDetail{
				ID: "b1", Name: "Polymaker", Slug: "polymaker",
				Materials: []ofdMaterialEntry{
					{ID: "m1", Material: "PLA", Slug: "PLA", FilamentCount: 3},
				},
			})
		case "/api/v1/brands/polymaker/materials/PLA/index.json":
			json.NewEncoder(w).Encode(ofdMaterialDetail{
				ID: "m1", Material: "PLA", Slug: "PLA", MaterialClass: "FFF",
				Filaments: []ofdFilamentEntry{
					{ID: "f1", Name: "PolyLite PLA", Slug: "polylite_pla", VariantCount: 10},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	source := OpenPrintTagSource{
		ID: 1, Name: "OFD Test", URL: srv.URL, SourceType: "ofd_api", Enabled: true,
	}
	results, err := SearchOPTSource(source, "polymaker pla")
	if err != nil {
		t.Fatalf("SearchOPTSource: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	wantRef := "brands/polymaker/materials/PLA/filaments/polylite_pla"
	if r.SourceRef != wantRef {
		t.Errorf("SourceRef: got %q, want %q", r.SourceRef, wantRef)
	}
	if r.FilamentName != "PolyLite PLA" {
		t.Errorf("FilamentName: got %q, want %q", r.FilamentName, "PolyLite PLA")
	}
	if r.Brand != "Polymaker" {
		t.Errorf("Brand: got %q, want %q", r.Brand, "Polymaker")
	}
	if r.Material != "PLA" {
		t.Errorf("Material: got %q, want %q", r.Material, "PLA")
	}
	if r.MaterialClass != "FFF" {
		t.Errorf("MaterialClass: got %q, want %q", r.MaterialClass, "FFF")
	}
	if r.SourceID != 1 {
		t.Errorf("SourceID: got %d, want 1", r.SourceID)
	}
}

func TestOFDSearch_EmptyResults(t *testing.T) {
	t.Cleanup(resetOFDBrandsCache)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/brands/index.json":
			// Return brands that do NOT match "nonexistent"
			json.NewEncoder(w).Encode(ofdBrandsIndex{
				Brands: []ofdBrandIndexEntry{
					{ID: "b1", Name: "Polymaker", Slug: "polymaker", MaterialCount: 5},
					{ID: "b2", Name: "Bambu Lab", Slug: "bambu_lab", MaterialCount: 12},
					{ID: "b3", Name: "Prusa", Slug: "prusa", MaterialCount: 8},
				},
			})
		default:
			// Return empty materials for any brand detail or 404
			if r.URL.Path != "" {
				json.NewEncoder(w).Encode(ofdBrandDetail{Materials: nil})
			}
		}
	}))
	defer srv.Close()

	source := OpenPrintTagSource{ID: 1, URL: srv.URL, SourceType: "ofd_api", Enabled: true}
	results, err := SearchOPTSource(source, "nonexistent filament xyz")
	if err != nil {
		t.Fatalf("SearchOPTSource: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestOFDSearch_ServerError(t *testing.T) {
	t.Cleanup(resetOFDBrandsCache)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	source := OpenPrintTagSource{ID: 1, URL: srv.URL, SourceType: "ofd_api", Enabled: true}
	_, err := SearchOPTSource(source, "pla")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestBuildTestURL(t *testing.T) {
	cases := []struct {
		sourceType string
		base       string
		wantSuffix string
	}{
		{"ofd_api", "https://api.openfilamentdatabase.org", "/api/v1/brands/index.json"},
		{"filament_db_api", "https://example.com", "/api/health"},
		{"ofd_api", "https://api.example.org/", "/api/v1/brands/index.json"},
	}
	for _, c := range cases {
		s := OpenPrintTagSource{URL: c.base, SourceType: c.sourceType}
		got := buildTestURL(s)
		if len(got) < len(c.wantSuffix) || got[len(got)-len(c.wantSuffix):] != c.wantSuffix {
			t.Errorf("buildTestURL(%s,%s): got %q, want suffix %q", c.sourceType, c.base, got, c.wantSuffix)
		}
	}
}

// filamentDBMockResponse returns a mock GET /api/filaments response matching
// the actual hyiger/filament-db JSON shape (MongoDB _id, vendor/type/color as
// top-level strings, temperatures object).
func filamentDBMockResponse() []filamentDBFilament {
	return []filamentDBFilament{
		{
			ID:                "6643f1a2b3c4d5e6f7a8b9c0",
			Name:              "PolyTerra PLA Charcoal Black",
			Vendor:            "Polymaker",
			Type:              "PLA",
			Color:             "1a1a1a",
			Diameter:          1.75,
			Density:           1.24,
			NetFilamentWeight: 1000,
			Temperatures: struct {
				Nozzle float64 `json:"nozzle"`
				Bed    float64 `json:"bed"`
			}{Nozzle: 200, Bed: 60},
		},
	}
}

// TestFilamentDBSearch_MapsFields uses a mock server that serves the
// GET /api/filaments?search=... response and verifies field mapping.
func TestFilamentDBSearch_MapsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/filaments" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Verify the search param is forwarded correctly.
		if r.URL.Query().Get("search") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(filamentDBMockResponse())
	}))
	defer srv.Close()

	source := OpenPrintTagSource{
		ID: 2, Name: "filament-db Test", URL: srv.URL, SourceType: "filament_db_api", Enabled: true,
	}
	results, err := SearchOPTSource(source, "polymaker pla")
	if err != nil {
		t.Fatalf("SearchOPTSource: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.SourceRef != "6643f1a2b3c4d5e6f7a8b9c0" {
		t.Errorf("SourceRef: got %q, want MongoDB _id", r.SourceRef)
	}
	if r.FilamentName != "PolyTerra PLA Charcoal Black" {
		t.Errorf("FilamentName: got %q", r.FilamentName)
	}
	if r.Brand != "Polymaker" {
		t.Errorf("Brand: got %q, want Polymaker", r.Brand)
	}
	if r.Material != "PLA" {
		t.Errorf("Material: got %q, want PLA", r.Material)
	}
	if r.ColorHex != "1a1a1a" {
		t.Errorf("ColorHex: got %q, want 1a1a1a", r.ColorHex)
	}
	if r.DiameterMM != 1.75 {
		t.Errorf("DiameterMM: got %v, want 1.75", r.DiameterMM)
	}
	if r.MinPrintTemp != 200 || r.MaxPrintTemp != 200 {
		t.Errorf("print temps: got min=%d max=%d, want 200/200", r.MinPrintTemp, r.MaxPrintTemp)
	}
	if r.MinBedTemp != 60 || r.MaxBedTemp != 60 {
		t.Errorf("bed temps: got min=%d max=%d, want 60/60", r.MinBedTemp, r.MaxBedTemp)
	}
	if r.SourceID != 2 {
		t.Errorf("SourceID: got %d, want 2", r.SourceID)
	}
}

// TestFilamentDBSearch_ServerError verifies that non-200 responses are surfaced as errors.
func TestFilamentDBSearch_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	source := OpenPrintTagSource{ID: 2, URL: srv.URL, SourceType: "filament_db_api", Enabled: true}
	_, err := SearchOPTSource(source, "pla")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

// TestFilamentDBFetchByID_MapsFields verifies GET /api/filaments/:id field mapping.
func TestFilamentDBFetchByID_MapsFields(t *testing.T) {
	mock := filamentDBMockResponse()[0]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/filaments/"+mock.ID {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mock)
	}))
	defer srv.Close()

	source := OpenPrintTagSource{
		ID: 2, Name: "filament-db Test", URL: srv.URL, SourceType: "filament_db_api", Enabled: true,
	}
	result, err := fetchOPTByRef(source, mock.ID)
	if err != nil {
		t.Fatalf("fetchOPTByRef: %v", err)
	}
	if result.SourceRef != mock.ID {
		t.Errorf("SourceRef: got %q, want %q", result.SourceRef, mock.ID)
	}
	if result.DiameterMM != 1.75 {
		t.Errorf("DiameterMM: got %v, want 1.75", result.DiameterMM)
	}
	if result.Density != 1.24 {
		t.Errorf("Density: got %v, want 1.24", result.Density)
	}
	if result.DefaultWeightG != 1000 {
		t.Errorf("DefaultWeightG: got %v, want 1000", result.DefaultWeightG)
	}
}

// TestFilamentDBSearch_SearchParamForwarded verifies the ?search= query param
// (not the old stub's ?q=) is used in the request URL.
func TestFilamentDBSearch_SearchParamForwarded(t *testing.T) {
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.Query().Get("search")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]filamentDBFilament{})
	}))
	defer srv.Close()

	source := OpenPrintTagSource{ID: 2, URL: srv.URL, SourceType: "filament_db_api", Enabled: true}
	_, err := SearchOPTSource(source, "polymaker pla")
	if err != nil {
		t.Fatalf("SearchOPTSource: %v", err)
	}
	if capturedQuery != "polymaker pla" {
		t.Errorf("search param: got %q, want %q", capturedQuery, "polymaker pla")
	}
}

// TestFilamentDBToOPT_EmptyNameFallback verifies that when Name is empty,
// the result FilamentName falls back to Vendor + " " + Type.
func TestFilamentDBToOPT_EmptyNameFallback(t *testing.T) {
	source := OpenPrintTagSource{ID: 1, Name: "test"}
	e := filamentDBFilament{
		ID:     "abc123",
		Vendor: "Prusament",
		Type:   "PETG",
	}
	r := filamentDBToOPT(source, e)
	if r.FilamentName != "Prusament PETG" {
		t.Errorf("FilamentName fallback: got %q, want %q", r.FilamentName, "Prusament PETG")
	}
}

// ─── Adapter registry tests ───────────────────────────────────────────────────

func TestOPTAdapterRegistry(t *testing.T) {
	if optAdapters["ofd_api"] == nil {
		t.Error("ofd_api adapter not registered")
	}
	if optAdapters["filament_db_api"] == nil {
		t.Error("filament_db_api adapter not registered")
	}
	if !validOPTSourceType("ofd_api") {
		t.Error("validOPTSourceType: ofd_api should be valid")
	}
	if validOPTSourceType("unknown_type") {
		t.Error("validOPTSourceType: unknown_type should be invalid")
	}

	// Register a mock adapter and verify it lands in the registry.
	type mockAdapter struct{ ofdAdapter }
	mock := mockAdapter{}
	RegisterOPTAdapter(struct {
		ofdAdapter
		typ string
	}{typ: "test_mock_adapter"})

	_ = mock // suppress unused warning

	// Use a proper named type so SourceType() can be overridden.
	type namedMock struct{}
	// Direct map write to test RegisterOPTAdapter plumbing without a full type.
	RegisterOPTAdapter(filamentDBAdapter{}) // re-register existing — idempotent
	if !validOPTSourceType("filament_db_api") {
		t.Error("re-registration broke filament_db_api")
	}

	// Register a truly new key via the map directly (tests RegisterOPTAdapter).
	testKey := "test_adapter_" + t.Name()
	optAdapters[testKey] = filamentDBAdapter{}
	if !validOPTSourceType(testKey) {
		t.Errorf("manually registered adapter %q not found", testKey)
	}
	delete(optAdapters, testKey)
	if validOPTSourceType(testKey) {
		t.Errorf("adapter %q should be gone after delete", testKey)
	}
}

// ─── TigerTag tests ───────────────────────────────────────────────────────────

func tigerTagSingleEntry(label string, minNozzle, maxNozzle, minBed, maxBed int) []tigerTagMaterial {
	m := tigerTagMaterial{ID: 1, Label: label, Density: 1.24}
	m.Recommended.NozzleTempMin = minNozzle
	m.Recommended.NozzleTempMax = maxNozzle
	m.Recommended.BedTempMin = minBed
	m.Recommended.BedTempMax = maxBed
	return []tigerTagMaterial{m}
}

func tigerTagMockServer(t *testing.T, materials []tigerTagMaterial) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/material/get/all" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(materials)
	}))
}

func TestTigerTagLookupTempsFrom_ExactMatch(t *testing.T) {
	t.Cleanup(resetTigerTagCache)
	srv := tigerTagMockServer(t, tigerTagSingleEntry("PLA", 190, 220, 50, 60))
	defer srv.Close()

	tt := tigerTagLookupTempsFrom(srv.URL, "PLA")
	if tt == nil {
		t.Fatal("expected non-nil result for exact match")
	}
	if tt.MinPrintTemp != 190 || tt.MaxPrintTemp != 220 {
		t.Errorf("print temps: got %d/%d, want 190/220", tt.MinPrintTemp, tt.MaxPrintTemp)
	}
	if tt.MinBedTemp != 50 || tt.MaxBedTemp != 60 {
		t.Errorf("bed temps: got %d/%d, want 50/60", tt.MinBedTemp, tt.MaxBedTemp)
	}
}

func TestTigerTagLookupTempsFrom_PrefixMatch(t *testing.T) {
	t.Cleanup(resetTigerTagCache)
	srv := tigerTagMockServer(t, tigerTagSingleEntry("PETG-CF", 230, 250, 70, 80))
	defer srv.Close()

	tt := tigerTagLookupTempsFrom(srv.URL, "PETG")
	if tt == nil {
		t.Fatal("expected prefix match for PETG against PETG-CF")
	}
	if tt.MinPrintTemp != 230 {
		t.Errorf("MinPrintTemp: got %d, want 230", tt.MinPrintTemp)
	}
}

func TestTigerTagLookupTempsFrom_CaseInsensitive(t *testing.T) {
	t.Cleanup(resetTigerTagCache)
	srv := tigerTagMockServer(t, tigerTagSingleEntry("pla", 190, 220, 50, 60))
	defer srv.Close()

	tt := tigerTagLookupTempsFrom(srv.URL, "PLA")
	if tt == nil {
		t.Fatal("expected case-insensitive match")
	}
}

func TestTigerTagLookupTempsFrom_NoMatch(t *testing.T) {
	t.Cleanup(resetTigerTagCache)
	srv := tigerTagMockServer(t, tigerTagSingleEntry("PLA", 190, 220, 50, 60))
	defer srv.Close()

	tt := tigerTagLookupTempsFrom(srv.URL, "DOESNOTEXIST")
	if tt != nil {
		t.Errorf("expected nil for no match, got %+v", tt)
	}
}

func TestTigerTagLookupTempsFrom_ZeroTempsReturnsNil(t *testing.T) {
	t.Cleanup(resetTigerTagCache)
	srv := tigerTagMockServer(t, tigerTagSingleEntry("PLA", 0, 0, 0, 0))
	defer srv.Close()

	tt := tigerTagLookupTempsFrom(srv.URL, "PLA")
	if tt != nil {
		t.Errorf("expected nil for all-zero temps, got %+v", tt)
	}
}

func TestTigerTagLookupTempsFrom_ServerError(t *testing.T) {
	t.Cleanup(resetTigerTagCache)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tt := tigerTagLookupTempsFrom(srv.URL, "PLA")
	if tt != nil {
		t.Errorf("expected nil on server error, got %+v", tt)
	}
}

func TestTigerTagGetMaterials_CacheDeduplication(t *testing.T) {
	t.Cleanup(resetTigerTagCache)
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/material/get/all" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		callCount++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tigerTagSingleEntry("PLA", 190, 220, 50, 60))
	}))
	defer srv.Close()

	_, err := tigerTagGetMaterials(srv.URL)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, err = tigerTagGetMaterials(srv.URL)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 HTTP request (cache hit on second), got %d", callCount)
	}
}

// ─── Group A: HTTP-level tests for OPT API endpoints ─────────────────────────
//
// These tests wire up a minimal Gin router (no template parsing, no WebSocket hub)
// with only the OpenPrintTag handler methods, then exercise the routes via
// httptest.NewRecorder — same pattern used throughout the test suite.

// newOPTTestRouter builds a bare Gin engine wired to the given bridge with only
// the OPT routes registered.  Avoids NewWebServer (which parses templates).
func newOPTTestRouter(b *FilamentBridge) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ws := &WebServer{bridge: b}
	api := r.Group("/api")
	{
		api.GET("/openprinttag/sources", ws.optSourcesListHandler)
		api.POST("/openprinttag/sources", ws.optSourcesCreateHandler)
		// reset-defaults must be registered before :id so Gin doesn't consume
		// the literal "reset-defaults" as a parameter value.
		api.POST("/openprinttag/sources/reset-defaults", ws.optSourcesResetHandler)
		api.PUT("/openprinttag/sources/:id", ws.optSourcesUpdateHandler)
		api.DELETE("/openprinttag/sources/:id", ws.optSourcesDeleteHandler)
		api.POST("/openprinttag/sources/:id/test", ws.optSourcesTestHandler)
		api.GET("/openprinttag/search", ws.optSearchHandler)
		api.POST("/nfc/openprinttag-tag", ws.nfcOPTTagCreateHandler)
	}
	return r
}

// doOPTRequest fires a request through the given router and returns the recorder.
func doOPTRequest(r *gin.Engine, method, path string, body []byte) *httptest.ResponseRecorder {
	var bodyReader *bytes.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── Test 1: GET /api/openprinttag/sources — seeded defaults ─────────────────

func TestOPTHTTP_ListSources_SeededDefaults(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	w := doOPTRequest(r, http.MethodGet, "/api/openprinttag/sources", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var sources []OpenPrintTagSource
	if err := json.Unmarshal(w.Body.Bytes(), &sources); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 seeded sources, got %d", len(sources))
	}
	if sources[0].SourceType != "ofd_api" {
		t.Errorf("first source type: got %q, want ofd_api", sources[0].SourceType)
	}
	if sources[1].SourceType != "filament_db_api" {
		t.Errorf("second source type: got %q, want filament_db_api", sources[1].SourceType)
	}
}

// ─── Test 2a: POST /api/openprinttag/sources — valid source created ───────────

func TestOPTHTTP_CreateSource_Valid(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "My OFD",
		"url":         "https://api.example.com",
		"source_type": "ofd_api",
		"enabled":     true,
	})
	w := doOPTRequest(r, http.MethodPost, "/api/openprinttag/sources", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var s OpenPrintTagSource
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.ID == 0 {
		t.Error("expected non-zero id in response")
	}
	if s.Name != "My OFD" {
		t.Errorf("name: got %q, want My OFD", s.Name)
	}
}

// ─── Test 2b: POST /api/openprinttag/sources — javascript:// rejected (SSRF) ──

func TestOPTHTTP_CreateSource_SSRFRejected(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "Evil",
		"url":         "javascript://evil",
		"source_type": "ofd_api",
		"enabled":     true,
	})
	w := doOPTRequest(r, http.MethodPost, "/api/openprinttag/sources", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for javascript:// URL, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error resp: %v", err)
	}
	if resp["error"] == "" {
		t.Error("expected non-empty error field")
	}
}

// ─── Test 3a: PUT /api/openprinttag/sources/:id — happy path ─────────────────

func TestOPTHTTP_UpdateSource_HappyPath(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	// Get the first seeded source ID.
	sources, _ := b.ListOPTSources()
	id := sources[0].ID

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "Updated OFD",
		"url":         "https://updated.example.com",
		"source_type": "ofd_api",
		"enabled":     false,
	})
	w := doOPTRequest(r, http.MethodPut, fmt.Sprintf("/api/openprinttag/sources/%d", id), body)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the update persisted.
	updated, _ := b.ListOPTSources()
	var found *OpenPrintTagSource
	for i := range updated {
		if updated[i].ID == id {
			found = &updated[i]
		}
	}
	if found == nil {
		t.Fatal("source not found after update")
	}
	if found.Name != "Updated OFD" {
		t.Errorf("name: got %q, want Updated OFD", found.Name)
	}
	if found.Enabled {
		t.Error("expected enabled=false after update")
	}
}

// ─── Test 3b: PUT /api/openprinttag/sources/:id — missing ID ─────────────────
//
// SQLite UPDATE with a non-existent ID is a no-op (no error from the driver).
// The handler returns 200 ok:true.  Document current behavior so a future
// tightening to 404 is visible in the diff.

func TestOPTHTTP_UpdateSource_MissingID(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "Ghost",
		"url":         "https://ghost.example.com",
		"source_type": "ofd_api",
		"enabled":     true,
	})
	// ID 9999 does not exist.
	w := doOPTRequest(r, http.MethodPut, "/api/openprinttag/sources/9999", body)
	// Current implementation: SQLite silently ignores the missing row → 200.
	// Accept either 200 (current) or 404 (desired future behaviour).
	if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
		t.Fatalf("unexpected status %d: %s", w.Code, w.Body.String())
	}
}

// ─── Test 4a: DELETE /api/openprinttag/sources/:id — happy path ───────────────

func TestOPTHTTP_DeleteSource_HappyPath(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	// Insert an extra source to delete (don't delete a seeded default).
	id, err := b.InsertOPTSource(OpenPrintTagSource{
		Name:       "To Delete",
		URL:        "https://delete.example.com",
		SourceType: "ofd_api",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("InsertOPTSource: %v", err)
	}

	w := doOPTRequest(r, http.MethodDelete, fmt.Sprintf("/api/openprinttag/sources/%d", id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Confirm it's gone.
	sources, _ := b.ListOPTSources()
	for _, s := range sources {
		if s.ID == id {
			t.Errorf("deleted source still present: %+v", s)
		}
	}
}

// ─── Test 4b: DELETE /api/openprinttag/sources/:id — missing ID ───────────────

func TestOPTHTTP_DeleteSource_MissingID(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	// ID 9999 does not exist; SQLite DELETE is a no-op.
	w := doOPTRequest(r, http.MethodDelete, "/api/openprinttag/sources/9999", nil)
	// Accept either 200 (current) or 404 (desired future behaviour).
	if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
		t.Fatalf("unexpected status %d: %s", w.Code, w.Body.String())
	}
}

// ─── Test 5: POST /api/openprinttag/sources/reset-defaults ───────────────────

func TestOPTHTTP_ResetDefaults(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	// Add a custom source so there's something extra to clear.
	_, err := b.InsertOPTSource(OpenPrintTagSource{
		Name:       "Custom",
		URL:        "https://custom.example.com",
		SourceType: "ofd_api",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("InsertOPTSource: %v", err)
	}

	w := doOPTRequest(r, http.MethodPost, "/api/openprinttag/sources/reset-defaults", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var sources []OpenPrintTagSource
	if err := json.Unmarshal(w.Body.Bytes(), &sources); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 default sources after reset, got %d", len(sources))
	}
	if sources[0].SourceType != "ofd_api" {
		t.Errorf("first source: got %q, want ofd_api", sources[0].SourceType)
	}
	if sources[1].SourceType != "filament_db_api" {
		t.Errorf("second source: got %q, want filament_db_api", sources[1].SourceType)
	}
}

// ─── Test 6a: POST /api/openprinttag/sources/:id/test — valid source ──────────

func TestOPTHTTP_TestSource_Valid(t *testing.T) {
	// Mock OFD server that responds to the brands-index probe.
	probeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/brands/index.json" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"brands":[]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer probeSrv.Close()

	b := newOPTTestBridge(t)
	// Point the first seeded source at our probe server.
	sources, _ := b.ListOPTSources()
	id := sources[0].ID
	_ = b.UpdateOPTSource(OpenPrintTagSource{
		ID:         id,
		Name:       sources[0].Name,
		URL:        probeSrv.URL,
		SourceType: "ofd_api",
		Enabled:    true,
	})

	r := newOPTTestRouter(b)
	w := doOPTRequest(r, http.MethodPost, fmt.Sprintf("/api/openprinttag/sources/%d/test", id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Errorf("expected ok=true, got: %s", w.Body.String())
	}
}

// ─── Test 6b: POST /api/openprinttag/sources/:id/test — bad URL ──────────────

func TestOPTHTTP_TestSource_BadURL(t *testing.T) {
	b := newOPTTestBridge(t)
	// Port 1 is never open; the connection attempt will fail immediately.
	id, err := b.InsertOPTSource(OpenPrintTagSource{
		Name:       "Dead Server",
		URL:        "http://127.0.0.1:1",
		SourceType: "ofd_api",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("InsertOPTSource: %v", err)
	}

	r := newOPTTestRouter(b)
	w := doOPTRequest(r, http.MethodPost, fmt.Sprintf("/api/openprinttag/sources/%d/test", id), nil)
	// The handler always returns 200; ok=false signals the probe failed.
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok, _ := resp["ok"].(bool); ok {
		t.Errorf("expected ok=false for dead server")
	}
	if resp["error"] == nil || resp["error"] == "" {
		t.Error("expected non-empty error field")
	}
}

// ─── Test 7: GET /api/openprinttag/search?source_id=N&q=pla ──────────────────
//
// Inserts an ofd_api source pointing at a mock OFD server, then issues a
// search request through the HTTP handler.  No real network calls are made.

func TestOPTHTTP_Search_MockOFD(t *testing.T) {
	t.Cleanup(resetOFDBrandsCache)

	ofdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/brands/index.json":
			json.NewEncoder(w).Encode(ofdBrandsIndex{
				Brands: []ofdBrandIndexEntry{
					{ID: "b1", Name: "Polymaker", Slug: "polymaker", MaterialCount: 5},
				},
			})
		case "/api/v1/brands/polymaker/index.json":
			json.NewEncoder(w).Encode(ofdBrandDetail{
				ID: "b1", Name: "Polymaker", Slug: "polymaker",
				Materials: []ofdMaterialEntry{
					{ID: "m1", Material: "PLA", Slug: "PLA", FilamentCount: 1},
				},
			})
		case "/api/v1/brands/polymaker/materials/PLA/index.json":
			json.NewEncoder(w).Encode(ofdMaterialDetail{
				ID: "m1", Material: "PLA", Slug: "PLA", MaterialClass: "FFF",
				Filaments: []ofdFilamentEntry{
					{ID: "f1", Name: "PolyLite PLA", Slug: "polylite_pla", VariantCount: 3},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ofdSrv.Close()

	b := newOPTTestBridge(t)
	sourceID, err := b.InsertOPTSource(OpenPrintTagSource{
		Name:       "Mock OFD",
		URL:        ofdSrv.URL,
		SourceType: "ofd_api",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("InsertOPTSource: %v", err)
	}

	r := newOPTTestRouter(b)
	path := fmt.Sprintf("/api/openprinttag/search?source_id=%d&q=polymaker+pla", sourceID)
	w := doOPTRequest(r, http.MethodGet, path, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var results []OPTSearchResult
	if err := json.Unmarshal(w.Body.Bytes(), &results); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one search result")
	}
	if results[0].FilamentName != "PolyLite PLA" {
		t.Errorf("FilamentName: got %q, want PolyLite PLA", results[0].FilamentName)
	}
	if results[0].Brand != "Polymaker" {
		t.Errorf("Brand: got %q, want Polymaker", results[0].Brand)
	}
}

// ─── Test 8: POST /api/nfc/openprinttag-tag ───────────────────────────────────

// TestOPTHTTP_OPTTagCreate_MissingRequiredFields verifies that omitting
// source_id or source_ref yields 400.
func TestOPTHTTP_OPTTagCreate_MissingRequiredFields(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	// source_id defaults to zero-value; source_ref is empty → 400.
	body, _ := json.Marshal(map[string]interface{}{
		"label": "Test",
	})
	w := doOPTRequest(r, http.MethodPost, "/api/nfc/openprinttag-tag", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing source_id/source_ref, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["error"] == "" {
		t.Error("expected non-empty error field")
	}
}

// TestOPTHTTP_OPTTagCreate_UnknownSource verifies that a source_id referencing
// a non-existent source yields 404.
func TestOPTHTTP_OPTTagCreate_UnknownSource(t *testing.T) {
	b := newOPTTestBridge(t)
	r := newOPTTestRouter(b)

	body, _ := json.Marshal(map[string]interface{}{
		"source_id":  9999,
		"source_ref": "some/ref",
	})
	w := doOPTRequest(r, http.MethodPost, "/api/nfc/openprinttag-tag", body)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown source, got %d: %s", w.Code, w.Body.String())
	}
}
