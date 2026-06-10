package main

import (
	"encoding/json"
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

// ─── APIShapeMonitor ──────────────────────────────────────────────────────────

func TestAPIShapeMonitor_FirstCall_NoChange(t *testing.T) {
	m := NewAPIShapeMonitor()
	body := []byte(`{"printer":{"state":"IDLE"}}`)
	added, removed, changed := m.Check("p1", "status", body)
	if changed {
		t.Error("first call should never report changed")
	}
	if len(added) != 0 || len(removed) != 0 {
		t.Errorf("first call should return no diff, got added=%v removed=%v", added, removed)
	}
}

func TestAPIShapeMonitor_NoChange(t *testing.T) {
	m := NewAPIShapeMonitor()
	body := []byte(`{"printer":{"state":"IDLE","temp":0}}`)
	m.Check("p1", "status", body)
	added, removed, changed := m.Check("p1", "status", body)
	if changed {
		t.Error("same JSON twice should not report changed")
	}
	if len(added) != 0 || len(removed) != 0 {
		t.Errorf("expected empty diff, got added=%v removed=%v", added, removed)
	}
}

func TestAPIShapeMonitor_ValueChangeNoAlert(t *testing.T) {
	m := NewAPIShapeMonitor()
	m.Check("p1", "status", []byte(`{"printer":{"state":"IDLE"}}`))
	added, removed, changed := m.Check("p1", "status", []byte(`{"printer":{"state":"PRINTING"}}`))
	if changed {
		t.Error("value-only change should not trigger shape change")
	}
	_ = added
	_ = removed
}

func TestAPIShapeMonitor_AddedField(t *testing.T) {
	m := NewAPIShapeMonitor()
	m.Check("p1", "status", []byte(`{"printer":{"state":"IDLE"}}`))
	added, removed, changed := m.Check("p1", "status", []byte(`{"printer":{"state":"IDLE","new_field":42}}`))
	if !changed {
		t.Fatal("expected changed=true when a field is added")
	}
	if len(removed) != 0 {
		t.Errorf("expected no removed fields, got %v", removed)
	}
	found := false
	for _, p := range added {
		if p == "/printer/new_field" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /printer/new_field in added, got %v", added)
	}
}

func TestAPIShapeMonitor_RemovedField(t *testing.T) {
	m := NewAPIShapeMonitor()
	m.Check("p1", "status", []byte(`{"printer":{"state":"IDLE","old_field":99}}`))
	added, removed, changed := m.Check("p1", "status", []byte(`{"printer":{"state":"IDLE"}}`))
	if !changed {
		t.Fatal("expected changed=true when a field is removed")
	}
	if len(added) != 0 {
		t.Errorf("expected no added fields, got %v", added)
	}
	found := false
	for _, p := range removed {
		if p == "/printer/old_field" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /printer/old_field in removed, got %v", removed)
	}
}

func TestAPIShapeMonitor_MultiPrinterIndependent(t *testing.T) {
	m := NewAPIShapeMonitor()
	base := []byte(`{"printer":{"state":"IDLE"}}`)
	m.Check("printer-a", "status", base)
	m.Check("printer-b", "status", base)

	// Change printer-a's shape
	added, _, changed := m.Check("printer-a", "status", []byte(`{"printer":{"state":"IDLE","extra":1}}`))
	if !changed {
		t.Error("printer-a should detect change")
	}
	_ = added

	// printer-b should not be affected
	_, _, changedB := m.Check("printer-b", "status", base)
	if changedB {
		t.Error("printer-b shape should be unchanged")
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
	// Unmute via new print
	m.UnmuteOnPrint("p1")
	if !m.ShouldAlert("p1") {
		t.Error("should alert again after UnmuteOnPrint")
	}
}

func TestAPIShapeMonitor_MuteAlsoClearsPending(t *testing.T) {
	m := NewAPIShapeMonitor()
	m.SetAlertPending("p1")
	m.Mute("p1") // mute should also clear pending so unmute starts fresh
	m.UnmuteOnPrint("p1")
	if !m.ShouldAlert("p1") {
		t.Error("after mute+unmute, ShouldAlert should be true")
	}
}

// ─── stripJSONKey ─────────────────────────────────────────────────────────────

func TestStripJSONKey_RemovesKey(t *testing.T) {
	body := []byte(`{"job":{"id":1},"printer":{"state":"PRINTING"}}`)
	out := stripJSONKey(body, "job")
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["job"]; ok {
		t.Error("expected job key to be removed")
	}
	if _, ok := m["printer"]; !ok {
		t.Error("expected printer key to remain")
	}
}

func TestStripJSONKey_AbsentKey_Unchanged(t *testing.T) {
	body := []byte(`{"printer":{"state":"IDLE"}}`)
	out := stripJSONKey(body, "job")
	var m map[string]interface{}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["printer"]; !ok {
		t.Error("expected printer key to remain")
	}
}

// TestAPIShapeMonitor_StatusJobSubkeyNoFalsePositive verifies that a PRINTING→FINISHED
// state transition (where the "job" sub-key disappears from /api/v1/status) does NOT
// trigger a shape-change alert after normalization.
func TestAPIShapeMonitor_StatusJobSubkeyNoFalsePositive(t *testing.T) {
	printingBody := []byte(`{"job":{"id":62,"progress":8},"printer":{"state":"PRINTING","temp_nozzle":230}}`)
	finishedBody := []byte(`{"printer":{"state":"FINISHED","temp_nozzle":26}}`)

	m := NewAPIShapeMonitor()
	m.Check("core-one", "status", normalizePrusaLinkStatusForMonitor(printingBody))
	_, _, changed := m.Check("core-one", "status", normalizePrusaLinkStatusForMonitor(finishedBody))
	if changed {
		t.Error("job sub-key disappearing on print completion should not trigger shape change after normalization")
	}
}

// TestAPIShapeMonitor_AxisXYDisappearsOnPrint_NoFalsePositive verifies that
// axis_x/axis_y disappearing when a print starts (Core One L behaviour) does
// NOT trigger a shape-change alert after normalization.
func TestAPIShapeMonitor_AxisXYDisappearsOnPrint_NoFalsePositive(t *testing.T) {
	idleBody  := []byte(`{"printer":{"state":"IDLE","temp_nozzle":26,"axis_x":0,"axis_y":0,"axis_z":10}}`)
	printBody := []byte(`{"job":{"id":1,"progress":5,"time_printing":60},"printer":{"state":"PRINTING","temp_nozzle":230,"axis_z":10}}`)

	m := NewAPIShapeMonitor()
	m.Check("core-one", "status", normalizePrusaLinkStatusForMonitor(idleBody))
	_, _, changed := m.Check("core-one", "status", normalizePrusaLinkStatusForMonitor(printBody))
	if changed {
		t.Error("axis_x/axis_y disappearing on print start should not trigger shape change after normalization")
	}
}

// TestAPIShapeMonitor_TransferFieldNoFalsePositive verifies that the /transfer
// object appearing and disappearing (present only during file uploads) does NOT
// trigger a shape-change alert after normalization.
func TestAPIShapeMonitor_TransferFieldNoFalsePositive(t *testing.T) {
	idleBody     := []byte(`{"printer":{"state":"IDLE","temp_nozzle":26}}`)
	transferBody := []byte(`{"printer":{"state":"IDLE","temp_nozzle":26},"transfer":{"id":1,"progress":50,"time_transferring":10,"transferred":512000}}`)

	m := NewAPIShapeMonitor()
	m.Check("core-one", "status", normalizePrusaLinkStatusForMonitor(idleBody))
	_, _, changed := m.Check("core-one", "status", normalizePrusaLinkStatusForMonitor(transferBody))
	if changed {
		t.Error("transfer object appearing should not trigger shape change after normalization")
	}
}

// ─── Fixture stability ────────────────────────────────────────────────────────

func TestAPIShapeMonitor_LoadStatusFixture_Stable(t *testing.T) {
	data, err := os.ReadFile("testdata/prusalink_status.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	m := NewAPIShapeMonitor()
	m.Check("core-one", "status", data)
	_, _, changed := m.Check("core-one", "status", data)
	if changed {
		t.Error("same fixture twice should not report changed")
	}
}

func TestAPIShapeMonitor_LoadJobFixture_Stable(t *testing.T) {
	data, err := os.ReadFile("testdata/prusalink_job.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	m := NewAPIShapeMonitor()
	m.Check("core-one", "job", data)
	_, _, changed := m.Check("core-one", "job", data)
	if changed {
		t.Error("same fixture twice should not report changed")
	}
}
