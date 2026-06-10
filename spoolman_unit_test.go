// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// spoolman_unit_test.go
// =============================================================================
// Unit tests for SpoolmanClient behaviour that can run without a real Spoolman
// instance, using httptest.NewServer to serve controlled JSON responses.
//
// Run:
//   go test ./... -v -run TestGetAllSpools
// =============================================================================

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveSpoolJSON starts a test server that returns a fixed JSON spool list and
// returns a SpoolmanClient pointed at it.
func serveSpoolJSON(t *testing.T, spools []map[string]interface{}) *SpoolmanClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(spools)
	}))
	t.Cleanup(srv.Close)
	return NewSpoolmanClient(srv.URL, 5)
}

// spoolJSON builds a minimal spool map for use in test responses.
func spoolJSON(id int, remainingWeight float64, material string) map[string]interface{} {
	return map[string]interface{}{
		"id":               id,
		"remaining_weight": remainingWeight,
		"used_weight":      0,
		"initial_weight":   remainingWeight,
		"filament": map[string]interface{}{
			"id":       id,
			"name":     "Test Spool",
			"material": material,
			"vendor": map[string]interface{}{
				"id":   1,
				"name": "TestBrand",
			},
		},
	}
}

// TestGetAllSpools_IncludesZeroAndNegativeWeight is the regression test for the
// deliberate policy that GetAllSpools must never filter by remaining_weight.
// Negative remaining weight is valid (estimation overshoot, unknown initial
// weight) — those spools must remain selectable in all dropdowns and lookups.
// See CLAUDE.md Architecture Principle 9.
func TestGetAllSpools_IncludesZeroAndNegativeWeight(t *testing.T) {
	client := serveSpoolJSON(t, []map[string]interface{}{
		spoolJSON(1, 250.0, "PLA"),   // normal
		spoolJSON(2, 0.0, "PETG"),    // exactly empty
		spoolJSON(3, -14.7, "PETG"),  // gone negative
	})

	spools, err := client.GetAllSpools()
	if err != nil {
		t.Fatalf("GetAllSpools: %v", err)
	}

	if len(spools) != 3 {
		t.Fatalf("expected 3 spools (including zero and negative weight), got %d", len(spools))
	}

	byID := make(map[int]SpoolmanSpool, len(spools))
	for _, s := range spools {
		byID[s.ID] = s
	}

	cases := []struct {
		id   int
		want float64
	}{
		{1, 250.0},
		{2, 0.0},
		{3, -14.7},
	}
	for _, c := range cases {
		s, ok := byID[c.id]
		if !ok {
			t.Errorf("spool %d missing from GetAllSpools result", c.id)
			continue
		}
		if s.RemainingWeight != c.want {
			t.Errorf("spool %d remaining_weight: got %v, want %v", c.id, s.RemainingWeight, c.want)
		}
	}
}

// TestGetAllSpools_SortOrder verifies that spools are still sorted correctly
// (alphabetically by display name, then ascending by weight) after filter removal.
func TestGetAllSpools_SortOrder(t *testing.T) {
	client := serveSpoolJSON(t, []map[string]interface{}{
		spoolJSON(10, 100.0, "PLA"),
		spoolJSON(20, -5.0, "PLA"),
		spoolJSON(30, 50.0, "PLA"),
	})

	spools, err := client.GetAllSpools()
	if err != nil {
		t.Fatalf("GetAllSpools: %v", err)
	}
	if len(spools) != 3 {
		t.Fatalf("expected 3 spools, got %d", len(spools))
	}

	// All same display name — should be sorted ascending by remaining_weight.
	// Expected order: -5.0, 50.0, 100.0
	if spools[0].RemainingWeight != -5.0 {
		t.Errorf("first spool should have lowest weight (-5.0), got %v", spools[0].RemainingWeight)
	}
	if spools[1].RemainingWeight != 50.0 {
		t.Errorf("second spool should have weight 50.0, got %v", spools[1].RemainingWeight)
	}
	if spools[2].RemainingWeight != 100.0 {
		t.Errorf("third spool should have weight 100.0, got %v", spools[2].RemainingWeight)
	}
}
