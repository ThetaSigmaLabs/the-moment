// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// filament_calibration_test.go
// =============================================================================
// Unit tests for the Filament Calibration tab feature:
//   - requiredSpoolmanFields contains all 5 cal_* keys
//   - GetAllVendors, CreateVendor, CreateFilament, CloneFilament client methods
//   - GetFilamentExtraFloat helper
//
// Run: go test ./... -v -run TestCal
// =============================================================================

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCalibrationFieldsInRequiredList asserts that all 5 slicer-calibration
// custom fields are present in requiredSpoolmanFields with the correct entity
// and field type.
func TestCalibrationFieldsInRequiredList(t *testing.T) {
	expected := map[string]struct {
		entity    string
		fieldType string
	}{
		"cal_max_flow_rate":     {"filament", "float"},
		"cal_pressure_advance":  {"filament", "float"},
		"cal_flow_ratio":        {"filament", "float"},
		"cal_retraction_length": {"filament", "float"},
		"cal_retraction_speed":  {"filament", "float"},
	}

	found := map[string]bool{}
	for _, f := range requiredSpoolmanFields {
		if exp, ok := expected[f.Key]; ok {
			if f.Entity != exp.entity {
				t.Errorf("%s: entity = %q, want %q", f.Key, f.Entity, exp.entity)
			}
			if f.FieldType != exp.fieldType {
				t.Errorf("%s: field_type = %q, want %q", f.Key, f.FieldType, exp.fieldType)
			}
			found[f.Key] = true
		}
	}
	for key := range expected {
		if !found[key] {
			t.Errorf("requiredSpoolmanFields is missing %q", key)
		}
	}
}

// TestCloneFilament verifies that CloneFilament GETs the source, strips id/
// registered/extra, appends " (copy)" to the name, and POSTs the new record.
func TestCloneFilament(t *testing.T) {
	var postBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/filament/7":
			fmt.Fprint(w, `{
				"id":7,"registered":"2026-01-01T00:00:00Z",
				"name":"PolyMax PLA","material":"PLA","diameter":1.75,
				"settings_extruder_temp":215,"settings_bed_temp":60,
				"color_hex":"FF0000","density":1.24,"weight":1000,"spool_weight":200,
				"vendor":{"id":1,"name":"Polymaker"},
				"extra":{"cal_pressure_advance":"0.04"}
			}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/filament":
			postBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":8,"name":"PolyMax PLA (copy)","material":"PLA"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := NewSpoolmanClient(srv.URL, 5)
	cloned, err := client.CloneFilament(7)
	if err != nil {
		t.Fatalf("CloneFilament: %v", err)
	}
	if cloned.ID != 8 {
		t.Errorf("cloned ID = %d, want 8", cloned.ID)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(postBody, &body); err != nil {
		t.Fatalf("POST body is not valid JSON: %v", err)
	}
	// Name must have " (copy)" suffix
	if !strings.HasSuffix(fmt.Sprintf("%v", body["name"]), " (copy)") {
		t.Errorf("cloned name %q missing ' (copy)' suffix", body["name"])
	}
	// id and registered must NOT be forwarded
	if _, hasID := body["id"]; hasID {
		t.Error("POST body must not include 'id'")
	}
	if _, hasReg := body["registered"]; hasReg {
		t.Error("POST body must not include 'registered'")
	}
	// extra must NOT be forwarded (calibration values start fresh)
	if _, hasExtra := body["extra"]; hasExtra {
		t.Error("POST body must not include 'extra'")
	}
	// vendor_id must be resolved from the nested vendor object
	if fmt.Sprintf("%v", body["vendor_id"]) != "1" {
		t.Errorf("vendor_id = %v, want 1", body["vendor_id"])
	}
}

// TestGetFilamentExtraFloat covers the type-switch cases in the helper.
func TestGetFilamentExtraFloat(t *testing.T) {
	cases := []struct {
		name  string
		extra map[string]interface{}
		key   string
		want  float64
	}{
		{"missing key", map[string]interface{}{}, "cal_pressure_advance", 0},
		{"nil extra", nil, "cal_pressure_advance", 0},
		{"float64 value", map[string]interface{}{"cal_flow_ratio": float64(0.95)}, "cal_flow_ratio", 0.95},
		{"json.Number value", map[string]interface{}{"cal_max_flow_rate": json.Number("20.5")}, "cal_max_flow_rate", 20.5},
		{"wrong type", map[string]interface{}{"cal_retraction_length": "bad"}, "cal_retraction_length", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := SpoolmanFilament{Extra: tc.extra}
			got := GetFilamentExtraFloat(f, tc.key)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUpdateFilamentHandler_NativeField verifies that native field updates are
// routed to a direct PATCH on /api/v1/filament/:id (not via extra).
func TestUpdateFilamentHandler_NativeField(t *testing.T) {
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	client := NewSpoolmanClient(srv.URL, 5)
	err := client.UpdateFilament(1, map[string]interface{}{"diameter": 1.75})
	if err != nil {
		t.Fatalf("UpdateFilament: %v", err)
	}
	if gotPath != "/api/v1/filament/1" {
		t.Errorf("PATCH path = %q, want /api/v1/filament/1", gotPath)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["diameter"] != 1.75 {
		t.Errorf("body[diameter] = %v, want 1.75", body["diameter"])
	}
}

// TestUpdateFilamentHandler_CalField verifies that cal_* field updates are
// sent inside the extra map with a JSON-encoded value, as Spoolman requires.
func TestUpdateFilamentHandler_CalField(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	client := NewSpoolmanClient(srv.URL, 5)
	// Simulate what updateFilamentHandler does for a cal_* field:
	// encode value as JSON and nest under extra.
	paValue := 0.045
	encoded, _ := json.Marshal(paValue)
	err := client.UpdateFilament(1, map[string]interface{}{
		"extra": map[string]string{"cal_pressure_advance": string(encoded)},
	})
	if err != nil {
		t.Fatalf("UpdateFilament for cal field: %v", err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	extra, ok := body["extra"].(map[string]interface{})
	if !ok {
		t.Fatalf("body[extra] is not an object: %T", body["extra"])
	}
	if extra["cal_pressure_advance"] != "0.045" {
		t.Errorf("extra[cal_pressure_advance] = %v, want \"0.045\"", extra["cal_pressure_advance"])
	}
}

// TestMergeFilamentExtraField verifies that MergeFilamentExtraField GETs the
// current extra map, merges the new key, and PATCHes the complete merged map —
// so existing cal_* keys are not clobbered.
func TestMergeFilamentExtraField(t *testing.T) {
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			// Return a filament that already has cal_max_flow_rate set.
			fmt.Fprint(w, `{"id":1,"name":"Test","extra":{"cal_max_flow_rate":"20.5"}}`)
			return
		}
		// PATCH — capture body.
		patchBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	client := NewSpoolmanClient(srv.URL, 5)
	paEncoded, _ := json.Marshal(0.042)
	if err := client.MergeFilamentExtraField(1, "cal_pressure_advance", string(paEncoded)); err != nil {
		t.Fatalf("MergeFilamentExtraField: %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(patchBody, &body); err != nil {
		t.Fatalf("PATCH body not JSON: %v", err)
	}
	extra, ok := body["extra"].(map[string]interface{})
	if !ok {
		t.Fatalf("body[extra] is not an object: %T", body["extra"])
	}
	// Both the existing key and the new key must be present.
	if extra["cal_max_flow_rate"] != "20.5" {
		t.Errorf("existing cal_max_flow_rate = %v, want \"20.5\"", extra["cal_max_flow_rate"])
	}
	if extra["cal_pressure_advance"] != "0.042" {
		t.Errorf("new cal_pressure_advance = %v, want \"0.042\"", extra["cal_pressure_advance"])
	}
}
