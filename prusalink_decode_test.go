package main

import (
	"encoding/json"
	"os"
	"testing"
)

// TestDecodeStatus verifies PrusaLinkStatus correctly parses the real API response
// captured from a Core One L. If this test fails the struct has drifted from
// the real API format and monitorPrusaLink will silently get wrong values.
func TestDecodeStatus_AllFieldsPopulated(t *testing.T) {
	data, err := os.ReadFile("testdata/prusalink_status.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	var s PrusaLinkStatus
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Printer block
	if s.Printer.State != "PRINTING" {
		t.Errorf("Printer.State: got %q, want %q", s.Printer.State, "PRINTING")
	}
	assertFloat(t, "Printer.TempNozzle", s.Printer.TempNozzle, 230.3)
	assertFloat(t, "Printer.TargetNozzle", s.Printer.TargetNozzle, 230.0)
	assertFloat(t, "Printer.TempBed", s.Printer.TempBed, 59.9)
	assertFloat(t, "Printer.TargetBed", s.Printer.TargetBed, 60.0)
	assertFloat(t, "Printer.AxisZ", s.Printer.AxisZ, 32.6)
	assertInt(t, "Printer.Flow", s.Printer.Flow, 100)
	assertInt(t, "Printer.Speed", s.Printer.Speed, 100)
	assertInt(t, "Printer.FanHotend", s.Printer.FanHotend, 8384)
	assertInt(t, "Printer.FanPrint", s.Printer.FanPrint, 5065)

	// Job block (root level, not nested under Printer)
	assertInt(t, "Job.ID", s.Job.ID, 62)
	assertFloat(t, "Job.Progress", s.Job.Progress, 8.0)
	assertInt(t, "Job.TimeRemaining", s.Job.TimeRemaining, 100260)
	assertInt(t, "Job.TimePrinting", s.Job.TimePrinting, 10398)
}

// TestDecodeJob verifies PrusaLinkJob correctly parses the real API response.
func TestDecodeJob_AllFieldsPopulated(t *testing.T) {
	data, err := os.ReadFile("testdata/prusalink_job.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	var j PrusaLinkJob
	if err := json.Unmarshal(data, &j); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	assertInt(t, "ID", j.ID, 62)
	if j.State != "PRINTING" {
		t.Errorf("State: got %q, want %q", j.State, "PRINTING")
	}
	assertFloat(t, "Progress", j.Progress, 9.0)
	assertInt(t, "TimeRemaining", j.TimeRemaining, 100260)
	assertInt(t, "TimePrinting", j.TimePrinting, 10428)

	// File block
	if j.File.Name != "KNIGH~12.BGC" {
		t.Errorf("File.Name: got %q, want %q", j.File.Name, "KNIGH~12.BGC")
	}
	wantDisplay := "Knight_Helmet_0.4n_0.2mm_PLA_COREONEL_1d6h37m.bgcode"
	if j.File.DisplayName != wantDisplay {
		t.Errorf("File.DisplayName: got %q, want %q", j.File.DisplayName, wantDisplay)
	}
	if j.File.Path != "/usb/PRUSA" {
		t.Errorf("File.Path: got %q, want %q", j.File.Path, "/usb/PRUSA")
	}
	assertInt64(t, "File.Size", int64(j.File.Size), 88316222)
	assertInt64(t, "File.MTimestamp", j.File.MTimestamp, 1779907696)

	// Refs
	if j.File.Refs.Download != "/usb/PRUSA/KNIGH~12.BGC" {
		t.Errorf("File.Refs.Download: got %q, want %q", j.File.Refs.Download, "/usb/PRUSA/KNIGH~12.BGC")
	}
	if j.File.Refs.Thumbnail == "" {
		t.Error("File.Refs.Thumbnail should be non-empty")
	}
	if j.File.Refs.Icon == "" {
		t.Error("File.Refs.Icon should be non-empty")
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func assertFloat(t *testing.T, field string, got, want float64) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}

func assertInt(t *testing.T, field string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}

func assertInt64(t *testing.T, field string, got, want int64) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", field, got, want)
	}
}
