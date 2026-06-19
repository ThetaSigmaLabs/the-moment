package main

import (
	"os"
	"testing"
)

// ─── jsonFieldPaths ───────────────────────────────────────────────────────────

func TestJsonFieldPaths_Flat(t *testing.T) {
	v := map[string]interface{}{
		"state": "PRINTING",
		"temp":  215.0,
	}
	paths := jsonFieldPaths(v, "")
	want := map[string]bool{"/state": true, "/temp": true}
	for _, p := range paths {
		if !want[p] {
			t.Errorf("unexpected path %q", p)
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("missing expected path %q", p)
	}
}

func TestJsonFieldPaths_Nested(t *testing.T) {
	v := map[string]interface{}{
		"printer": map[string]interface{}{
			"state": "IDLE",
			"temp":  0.0,
		},
	}
	paths := jsonFieldPaths(v, "")
	want := map[string]bool{
		"/printer":       true,
		"/printer/state": true,
		"/printer/temp":  true,
	}
	for _, p := range paths {
		delete(want, p)
	}
	for p := range want {
		t.Errorf("missing expected path %q", p)
	}
}

func TestJsonFieldPaths_Array(t *testing.T) {
	v := map[string]interface{}{
		"items": []interface{}{
			map[string]interface{}{"id": 1},
		},
	}
	paths := jsonFieldPaths(v, "")
	want := map[string]bool{
		"/items":    true,
		"/items/id": true,
	}
	for _, p := range paths {
		delete(want, p)
	}
	for p := range want {
		t.Errorf("missing expected path %q", p)
	}
}

// ─── APIShapeMonitor — schema-based diff ──────────────────────────────────────

// All-known body should never alert.
func TestAPIShapeMonitor_AllKnown_NoAlert(t *testing.T) {
	m := NewAPIShapeMonitor()
	body := []byte(`{"id":1,"state":"PRINTING","progress":5.0,"time_remaining":1800,"time_printing":60,"file":{"name":"x.gcode"}}`)
	added, _, changed := m.Check("job", body)
	if changed || len(added) > 0 {
		t.Errorf("expected no change, got added=%v changed=%v", added, changed)
	}
}

// Regression: time_remaining appearing on a new print after a FINISHED-shaped
// body must not alert — it's in the schema.
func TestAPIShapeMonitor_JobTimeRemainingAppears_NoAlert(t *testing.T) {
	m := NewAPIShapeMonitor()
	finished := []byte(`{"id":62,"state":"FINISHED","progress":100,"time_printing":3600,"file":{"name":"x.gcode"}}`)
	printing := []byte(`{"id":63,"state":"PRINTING","progress":5,"time_remaining":1800,"time_printing":60,"file":{"name":"y.gcode"}}`)
	m.Check("job", finished)
	added, _, changed := m.Check("job", printing)
	if changed || len(added) > 0 {
		t.Errorf("FINISHED→PRINTING transition should not alert (time_remaining is in schema), got added=%v", added)
	}
}

// Known field disappearing is not an alert — fields may legitimately be
// absent depending on state or firmware version.
func TestAPIShapeMonitor_JobTimeRemainingDisappears_NoAlert(t *testing.T) {
	m := NewAPIShapeMonitor()
	printing := []byte(`{"id":63,"state":"PRINTING","progress":5,"time_remaining":1800,"time_printing":60,"file":{"name":"x.gcode"}}`)
	finished := []byte(`{"id":62,"state":"FINISHED","progress":100,"time_printing":3600,"file":{"name":"x.gcode"}}`)
	m.Check("job", printing)
	added, _, changed := m.Check("job", finished)
	if changed || len(added) > 0 {
		t.Errorf("PRINTING→FINISHED transition should not alert, got added=%v", added)
	}
}

// /api/v1/status: job / storage / transfer / printer.axis_x / printer.axis_y
// all legitimately come and go with state. None should alert.
func TestAPIShapeMonitor_StatusOptionalKeys_NoAlert(t *testing.T) {
	idle := []byte(`{"printer":{"state":"IDLE","temp_nozzle":26,"axis_x":0,"axis_y":0,"axis_z":10}}`)
	printing := []byte(`{"job":{"id":1,"progress":5,"time_printing":60,"time_remaining":1800},"printer":{"state":"PRINTING","temp_nozzle":230,"axis_z":10}}`)
	transfer := []byte(`{"printer":{"state":"IDLE","temp_nozzle":26},"transfer":{"id":1,"progress":50,"time_transferring":10,"transferred":512000}}`)
	storage := []byte(`{"storage":{"path":"/usb/","name":"usb","read_only":false},"printer":{"state":"IDLE","temp_nozzle":26}}`)

	m := NewAPIShapeMonitor()
	cycle := [][]byte{idle, printing, transfer, storage, idle, printing, transfer, storage}
	for _, body := range cycle {
		added, _, changed := m.Check("status", body)
		if changed || len(added) > 0 {
			t.Errorf("status optional keys should not alert, got added=%v", added)
		}
	}
}

// A genuinely unknown top-level field is the case worth alerting on.
func TestAPIShapeMonitor_UnknownTopLevelField_Alerts(t *testing.T) {
	m := NewAPIShapeMonitor()
	body := []byte(`{"id":1,"state":"PRINTING","progress":5,"time_remaining":1800,"time_printing":60,"file":{"name":"x.gcode"},"material_remaining":42}`)
	added, _, changed := m.Check("job", body)
	if !changed {
		t.Fatal("expected changed=true for unknown top-level field")
	}
	found := false
	for _, p := range added {
		if p == "/material_remaining" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /material_remaining in added, got %v", added)
	}
}

// Unknown nested field is also reported.
func TestAPIShapeMonitor_UnknownNestedField_Alerts(t *testing.T) {
	m := NewAPIShapeMonitor()
	body := []byte(`{"file":{"name":"x.gcode","refs":{"download":"/x","new_ref":"y"}}}`)
	added, _, changed := m.Check("job", body)
	if !changed {
		t.Fatal("expected changed=true for unknown nested field")
	}
	found := false
	for _, p := range added {
		if p == "/file/refs/new_ref" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /file/refs/new_ref in added, got %v", added)
	}
}

// Value-only changes never alert.
func TestAPIShapeMonitor_ValueChange_NoAlert(t *testing.T) {
	m := NewAPIShapeMonitor()
	m.Check("job", []byte(`{"id":1,"state":"IDLE","progress":0,"file":{"name":"x.gcode"}}`))
	added, _, changed := m.Check("job", []byte(`{"id":1,"state":"PRINTING","progress":50,"file":{"name":"x.gcode"}}`))
	if changed || len(added) > 0 {
		t.Errorf("value-only change should not alert, got added=%v", added)
	}
}

// Empty body never alerts (204 No Content from /api/v1/job when idle).
func TestAPIShapeMonitor_EmptyBody_NoAlert(t *testing.T) {
	m := NewAPIShapeMonitor()
	added, _, changed := m.Check("job", nil)
	if changed || len(added) > 0 {
		t.Errorf("empty body should not alert, got added=%v", added)
	}
}

// Endpoint with no registered schema is silently ignored — not an alert
// condition. (The monitor only knows status and job today.)
func TestAPIShapeMonitor_UnknownEndpoint_NoAlert(t *testing.T) {
	m := NewAPIShapeMonitor()
	added, _, changed := m.Check("info", []byte(`{"hostname":"x"}`))
	if changed || len(added) > 0 {
		t.Errorf("unknown endpoint should not alert, got added=%v", added)
	}
}

// Unparseable body never alerts.
func TestAPIShapeMonitor_BadJSON_NoAlert(t *testing.T) {
	m := NewAPIShapeMonitor()
	added, _, changed := m.Check("job", []byte(`{not json`))
	if changed || len(added) > 0 {
		t.Errorf("bad JSON should not alert, got added=%v", added)
	}
}

// ─── Muting ───────────────────────────────────────────────────────────────────

func TestAPIShapeMonitor_AlertPending_BlocksNewAlert(t *testing.T) {
	m := NewAPIShapeMonitor()
	if !m.ShouldAlert("p1") {
		t.Error("should alert when no pending alert")
	}
	m.SetAlertPending("p1")
	if m.ShouldAlert("p1") {
		t.Error("should not alert while alert is pending")
	}
	m.ClearAlert("p1")
	if !m.ShouldAlert("p1") {
		t.Error("should alert again after ClearAlert")
	}
}

func TestAPIShapeMonitor_Mute_SuppressesAlerts(t *testing.T) {
	m := NewAPIShapeMonitor()
	m.Mute("p1")
	if m.ShouldAlert("p1") {
		t.Error("should not alert while muted")
	}
	m.UnmuteOnPrint("p1")
	if !m.ShouldAlert("p1") {
		t.Error("should alert again after UnmuteOnPrint")
	}
}

func TestAPIShapeMonitor_MuteAlsoClearsPending(t *testing.T) {
	m := NewAPIShapeMonitor()
	m.SetAlertPending("p1")
	m.Mute("p1")
	m.UnmuteOnPrint("p1")
	if !m.ShouldAlert("p1") {
		t.Error("after mute+unmute, ShouldAlert should be true")
	}
}

// ─── Fixture stability ────────────────────────────────────────────────────────

func TestAPIShapeMonitor_LoadStatusFixture_Stable(t *testing.T) {
	data, err := os.ReadFile("testdata/prusalink_status.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	m := NewAPIShapeMonitor()
	added, _, changed := m.Check("status", data)
	if changed || len(added) > 0 {
		t.Errorf("status fixture should match schema, got added=%v", added)
	}
}

func TestAPIShapeMonitor_LoadJobFixture_Stable(t *testing.T) {
	data, err := os.ReadFile("testdata/prusalink_job.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	m := NewAPIShapeMonitor()
	added, _, changed := m.Check("job", data)
	if changed || len(added) > 0 {
		t.Errorf("job fixture should match schema, got added=%v", added)
	}
}
