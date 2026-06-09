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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cbor "github.com/fxamacker/cbor/v2"
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

// TestGetAllVendors verifies that GetAllVendors returns non-archived vendors sorted by ID.
func TestGetAllVendors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/vendor" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[
			{"id":3,"name":"Polymaker","archived":false},
			{"id":1,"name":"Bambu Lab","archived":false},
			{"id":2,"name":"Old Brand","archived":true}
		]`)
	}))
	defer srv.Close()

	client := NewSpoolmanClient(srv.URL, 5)
	vendors, err := client.GetAllVendors()
	if err != nil {
		t.Fatalf("GetAllVendors: %v", err)
	}
	// Archived vendor must be filtered out
	if len(vendors) != 2 {
		t.Fatalf("got %d vendors, want 2 (archived excluded)", len(vendors))
	}
	// Must be sorted by ID
	if vendors[0].ID != 1 || vendors[1].ID != 3 {
		t.Errorf("vendors not sorted by ID: got %d, %d", vendors[0].ID, vendors[1].ID)
	}
	if vendors[0].Name != "Bambu Lab" {
		t.Errorf("vendors[0].Name = %q, want %q", vendors[0].Name, "Bambu Lab")
	}
}

// TestUpdateFilamentHandler_NewNativeFields verifies that new native fields (name,
// color_hex, multi_color_hexes, weight, etc.) are sent directly via UpdateFilament.
func TestUpdateFilamentHandler_NewNativeFields(t *testing.T) {
	for _, tc := range []struct {
		field string
		value interface{}
	}{
		{"name", "PolyMax PLA"},
		{"color_hex", "FF0000"},
		{"multi_color_hexes", "FF0000,00FF00"},
		{"weight", 1000.0},
		{"spool_weight", 200.0},
		{"price", 24.99},
		{"density", 1.24},
	} {
		t.Run(tc.field, func(t *testing.T) {
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"id":1}`))
			}))
			defer srv.Close()
			client := NewSpoolmanClient(srv.URL, 5)
			err := client.UpdateFilament(1, map[string]interface{}{tc.field: tc.value})
			if err != nil {
				t.Fatalf("UpdateFilament(%s): %v", tc.field, err)
			}
			var body map[string]interface{}
			if err := json.Unmarshal(gotBody, &body); err != nil {
				t.Fatalf("body not JSON: %v", err)
			}
			if _, ok := body[tc.field]; !ok {
				t.Errorf("field %q missing from PATCH body", tc.field)
			}
		})
	}
}

// TestUpdateFilamentHandler_NfcField verifies that nfc_* fields are merged into
// the extra map (not sent as top-level keys), and do not clobber existing extras.
func TestUpdateFilamentHandler_NfcField(t *testing.T) {
	var patchBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"id":1,"name":"Test","extra":{"cal_pressure_advance":"0.04"}}`)
			return
		}
		patchBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	client := NewSpoolmanClient(srv.URL, 5)
	encoded, _ := json.Marshal(220)
	if err := client.MergeFilamentExtraField(1, "nfc_max_print_temp", string(encoded)); err != nil {
		t.Fatalf("MergeFilamentExtraField nfc: %v", err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(patchBody, &body); err != nil {
		t.Fatalf("PATCH body not JSON: %v", err)
	}
	extra, ok := body["extra"].(map[string]interface{})
	if !ok {
		t.Fatalf("body[extra] is not an object: %T", body["extra"])
	}
	if extra["nfc_max_print_temp"] != "220" {
		t.Errorf("extra[nfc_max_print_temp] = %v, want \"220\"", extra["nfc_max_print_temp"])
	}
	// Existing cal_* key must not be clobbered
	if extra["cal_pressure_advance"] != "0.04" {
		t.Errorf("cal_pressure_advance clobbered: got %v, want \"0.04\"", extra["cal_pressure_advance"])
	}
}

// decodeCBORPayloadMainMap parses a 3-section OPT CBOR payload (meta|main|aux)
// and returns the main section decoded as map[uint64]interface{}.
func decodeCBORPayloadMainMap(t *testing.T, payload []byte) map[uint64]interface{} {
	t.Helper()
	dec := cbor.NewDecoder(bytes.NewReader(payload))

	var meta interface{}
	if err := dec.Decode(&meta); err != nil {
		t.Fatalf("CBOR meta decode: %v", err)
	}
	var mainRaw interface{}
	if err := dec.Decode(&mainRaw); err != nil {
		t.Fatalf("CBOR main decode: %v", err)
	}
	rawMap, ok := mainRaw.(map[interface{}]interface{})
	if !ok {
		t.Fatalf("CBOR main is not a map: %T", mainRaw)
	}
	out := make(map[uint64]interface{}, len(rawMap))
	for k, v := range rawMap {
		switch kv := k.(type) {
		case uint64:
			out[kv] = v
		case int64:
			out[uint64(kv)] = v
		default:
			t.Fatalf("unexpected CBOR key type %T (%v)", k, k)
		}
	}
	return out
}

func makeTestSpool(fil *SpoolmanFilament) SpoolmanSpool {
	return SpoolmanSpool{Filament: fil}
}

// TestBuildOpenPrintTagPayload_MaterialClass verifies that nfc_material_class
// is encoded as CBOR key 8 with integer value 0 (FFF) or 1 (SLA).
func TestBuildOpenPrintTagPayload_MaterialClass(t *testing.T) {
	for _, tc := range []struct{ class string; want int }{ {"FFF", 0}, {"SLA", 1} } {
		t.Run(tc.class, func(t *testing.T) {
			fil := &SpoolmanFilament{
				Name:     "Test",
				Material: "PLA",
				Extra:    map[string]interface{}{"nfc_material_class": tc.class},
			}
			payload, err := buildOpenPrintTagPayload(makeTestSpool(fil))
			if err != nil {
				t.Fatalf("buildOpenPrintTagPayload: %v", err)
			}
			m := decodeCBORPayloadMainMap(t, payload)
			v, ok := m[8]
			if !ok {
				t.Fatalf("CBOR key 8 (material_class) missing from payload")
			}
			var got int
			switch n := v.(type) {
			case uint64:
				got = int(n)
			case int64:
				got = int(n)
			default:
				t.Fatalf("key 8 unexpected type %T", v)
			}
			if got != tc.want {
				t.Errorf("material_class = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestBuildOpenPrintTagPayload_MaterialProperties verifies that nfc_material_properties
// is encoded as CBOR key 28 with an integer array of enum values.
func TestBuildOpenPrintTagPayload_MaterialProperties(t *testing.T) {
	fil := &SpoolmanFilament{
		Name:     "Test",
		Material: "PLA",
		Extra: map[string]interface{}{
			"nfc_material_properties": `["abrasive","matte"]`,
		},
	}
	payload, err := buildOpenPrintTagPayload(makeTestSpool(fil))
	if err != nil {
		t.Fatalf("buildOpenPrintTagPayload: %v", err)
	}
	m := decodeCBORPayloadMainMap(t, payload)
	v, ok := m[28]
	if !ok {
		t.Fatalf("CBOR key 28 (tags / material_properties) missing from payload")
	}
	arr, ok := v.([]interface{})
	if !ok {
		t.Fatalf("key 28 expected []interface{}, got %T", v)
	}
	if len(arr) != 2 {
		t.Fatalf("key 28 length = %d, want 2", len(arr))
	}
	toInt := func(x interface{}) int {
		switch n := x.(type) {
		case uint64:
			return int(n)
		case int64:
			return int(n)
		}
		return -1
	}
	if toInt(arr[0]) != 4 { // abrasive
		t.Errorf("arr[0] = %v, want 4 (abrasive)", arr[0])
	}
	if toInt(arr[1]) != 16 { // matte
		t.Errorf("arr[1] = %v, want 16 (matte)", arr[1])
	}
}

// TestBuildOpenPrintTagPayload_SecondaryColors verifies that multi_color_hexes
// is encoded as CBOR keys 20 and 21 (secondary_color_0, secondary_color_1).
func TestBuildOpenPrintTagPayload_SecondaryColors(t *testing.T) {
	fil := &SpoolmanFilament{
		Name:            "Test",
		Material:        "PLA",
		MultiColorHexes: "FF0000,00FF00",
	}
	payload, err := buildOpenPrintTagPayload(makeTestSpool(fil))
	if err != nil {
		t.Fatalf("buildOpenPrintTagPayload: %v", err)
	}
	m := decodeCBORPayloadMainMap(t, payload)

	checkColor := func(key uint64, wantR, wantG, wantB byte) {
		t.Helper()
		v, ok := m[key]
		if !ok {
			t.Fatalf("CBOR key %d missing from payload", key)
		}
		rgb, ok := v.([]byte)
		if !ok {
			t.Fatalf("key %d: expected []byte, got %T", key, v)
		}
		if len(rgb) != 3 || rgb[0] != wantR || rgb[1] != wantG || rgb[2] != wantB {
			t.Errorf("key %d = %v, want [%d %d %d]", key, rgb, wantR, wantG, wantB)
		}
	}
	checkColor(20, 0xFF, 0x00, 0x00) // FF0000 → secondary_color_0
	checkColor(21, 0x00, 0xFF, 0x00) // 00FF00 → secondary_color_1

	if _, ok := m[22]; ok {
		t.Error("CBOR key 22 present but no third color was provided")
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
