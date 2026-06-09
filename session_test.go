// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

// =============================================================================
// session_test.go
// =============================================================================
// Unit tests verifying that print sessions are correctly grouped across all three
// source paths: virtual printer, PrusaLink, and OctoPrint.
//
// Run with:
//   go test ./... -v -run TestSession
// =============================================================================

import (
	"testing"
	"time"
)

// ─── Virtual printer ─────────────────────────────────────────────────────────

// TestSession_VirtualTwoToolheads simulates a two-toolhead virtual printer print:
// two LogPrintUsageFull calls sharing the same session ID (source="virtual").
// GetPrintSessions must return exactly one session with tool_count=2.
func TestSession_VirtualTwoToolheads(t *testing.T) {
	bridge := testBridge(t)

	sid := newSessionID()
	for toolhead := 0; toolhead < 2; toolhead++ {
		if _, err := bridge.LogPrintUsageFull(
			"CoreXL", toolhead, toolhead+1, 9.5,
			"vase_mode.gcode", 55, "completed", "", sid, "virtual",
		); err != nil {
			t.Fatalf("LogPrintUsageFull toolhead %d: %v", toolhead, err)
		}
	}

	sessions, err := bridge.GetPrintSessions(10)
	if err != nil {
		t.Fatalf("GetPrintSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session for virtual 2-toolhead print, got %d", len(sessions))
	}

	s := sessions[0]
	if s.ToolCount != 2 {
		t.Errorf("expected tool_count=2, got %d", s.ToolCount)
	}
	if s.SessionID != sid {
		t.Errorf("expected session_id=%s, got %s", sid, s.SessionID)
	}
	if s.Source != "virtual" {
		t.Errorf("expected source=virtual, got %s", s.Source)
	}
	assertApprox(t, "total filament", 19.0, s.TotalFilamentG, 0.01)

	// Records must all carry the same session_id.
	for i, r := range s.Records {
		if r.SessionID != sid {
			t.Errorf("record[%d] session_id=%s, want %s", i, r.SessionID, sid)
		}
		if r.Source != "virtual" {
			t.Errorf("record[%d] source=%s, want virtual", i, r.Source)
		}
	}
	t.Logf("✅ Virtual 2-toolhead: 1 session, tool_count=%d, %.1fg", s.ToolCount, s.TotalFilamentG)
}

// TestSession_VirtualThreeToolheads verifies a three-toolhead virtual print
// collapses to one session.
func TestSession_VirtualThreeToolheads(t *testing.T) {
	bridge := testBridge(t)

	sid := newSessionID()
	grams := []float64{5.1, 7.3, 4.8}
	for i, g := range grams {
		if _, err := bridge.LogPrintUsageFull(
			"XL-5head", i, i+10, g, "multicolor.gcode", 120, "completed", "", sid, "virtual",
		); err != nil {
			t.Fatalf("LogPrintUsageFull T%d: %v", i, err)
		}
	}

	sessions, err := bridge.GetPrintSessions(10)
	if err != nil {
		t.Fatalf("GetPrintSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ToolCount != 3 {
		t.Errorf("expected tool_count=3, got %d", sessions[0].ToolCount)
	}
	assertApprox(t, "total filament", 17.2, sessions[0].TotalFilamentG, 0.01)
	t.Logf("✅ Virtual 3-toolhead: tool_count=%d, %.2fg", sessions[0].ToolCount, sessions[0].TotalFilamentG)
}

// ─── PrusaLink ────────────────────────────────────────────────────────────────

// TestSession_PrusaLinkTwoToolheads simulates how handlePrusaLinkPrintFinished
// logs a two-toolhead Prusa print: two rows sharing one session_id, source="prusalink".
func TestSession_PrusaLinkTwoToolheads(t *testing.T) {
	bridge := testBridge(t)

	sid := newSessionID()
	for toolhead := 0; toolhead < 2; toolhead++ {
		if _, err := bridge.LogPrintUsageFull(
			"Prusa Core One", toolhead, toolhead+1, 12.0,
			"benchy.gcode", 60, "completed", "", sid, "prusalink",
		); err != nil {
			t.Fatalf("LogPrintUsageFull T%d: %v", toolhead, err)
		}
	}

	sessions, err := bridge.GetPrintSessions(10)
	if err != nil {
		t.Fatalf("GetPrintSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session for PrusaLink 2-toolhead, got %d", len(sessions))
	}

	s := sessions[0]
	if s.ToolCount != 2 {
		t.Errorf("expected tool_count=2, got %d", s.ToolCount)
	}
	if s.Source != "prusalink" {
		t.Errorf("expected source=prusalink, got %s", s.Source)
	}
	assertApprox(t, "total filament", 24.0, s.TotalFilamentG, 0.01)
	t.Logf("✅ PrusaLink 2-toolhead: 1 session, tool_count=%d, %.1fg", s.ToolCount, s.TotalFilamentG)
}

// TestSession_PrusaLinkSingleToolhead verifies a standard single-toolhead
// PrusaLink print produces one session with tool_count=1.
func TestSession_PrusaLinkSingleToolhead(t *testing.T) {
	bridge := testBridge(t)

	sid := newSessionID()
	if _, err := bridge.LogPrintUsageFull(
		"MK4S", 0, 3, 8.0, "cube.gcode", 30, "completed", "", sid, "prusalink",
	); err != nil {
		t.Fatalf("LogPrintUsageFull: %v", err)
	}

	sessions, err := bridge.GetPrintSessions(10)
	if err != nil {
		t.Fatalf("GetPrintSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ToolCount != 1 {
		t.Errorf("expected tool_count=1, got %d", sessions[0].ToolCount)
	}
	if sessions[0].Source != "prusalink" {
		t.Errorf("expected source=prusalink, got %s", sessions[0].Source)
	}
	t.Logf("✅ PrusaLink single-toolhead: 1 session, tool_count=1")
}

// ─── OctoPrint ────────────────────────────────────────────────────────────────

// TestSession_OctoPrintSingleRecord verifies that LogOctoPrintRecord produces
// exactly one session with tool_count=1 and source="octoprint".
func TestSession_OctoPrintSingleRecord(t *testing.T) {
	bridge := testBridge(t)

	sid := "octo-session-1234"
	payload := OctoPrintPayload{
		SessionID: sid,
		PrinterID: "ender3-v3-se",
		FileName:  "tree_support.gcode",
		Status:    "completed",
		StartedAt: time.Now().Add(-2 * time.Hour),
		EndedAt:   time.Now(),
		TotalDurationSec: 7200,
		PrintDurationSec: 7000,
		TimePrecision:    "exact",
		FilamentPrecision: "measured",
		Filament: []OctoPrintPayloadFilament{
			{ToolIndex: 0, ChangeNumber: 0, SpoolID: 5, FilamentUsedMM: 3000, FilamentUsedG: 8.9},
		},
	}
	printID, err := bridge.LogOctoPrintRecord(payload)
	if err != nil {
		t.Fatalf("LogOctoPrintRecord: %v", err)
	}
	if printID <= 0 {
		t.Fatalf("expected positive printID, got %d", printID)
	}

	sessions, err := bridge.GetPrintSessions(10)
	if err != nil {
		t.Fatalf("GetPrintSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	s := sessions[0]
	if s.ToolCount != 1 {
		t.Errorf("expected tool_count=1, got %d", s.ToolCount)
	}
	if s.Source != "octoprint" {
		t.Errorf("expected source=octoprint, got %s", s.Source)
	}
	if s.SessionID != sid {
		t.Errorf("expected session_id=%s, got %s", sid, s.SessionID)
	}
	assertApprox(t, "total filament", 8.9, s.TotalFilamentG, 0.01)
	t.Logf("✅ OctoPrint single record: 1 session, source=%s, %.2fg", s.Source, s.TotalFilamentG)
}

// ─── Mixed sources ────────────────────────────────────────────────────────────

// TestSession_MixedSources verifies that three distinct print jobs from different
// sources each produce their own session, ordered newest-first.
func TestSession_MixedSources(t *testing.T) {
	bridge := testBridge(t)

	// Virtual 2-toolhead print (newest)
	vSID := newSessionID()
	for th := 0; th < 2; th++ {
		bridge.LogPrintUsageFull("CoreXL", th, th+1, 6.0, "virt.gcode", 40, "completed", "", vSID, "virtual")
	}

	// PrusaLink single-toolhead print (middle)
	pSID := newSessionID()
	bridge.LogPrintUsageFull("MK4S", 0, 5, 10.0, "prusa.gcode", 50, "completed", "", pSID, "prusalink")

	// OctoPrint print (oldest — use explicit past time via payload)
	oSID := "octo-mixed-session"
	bridge.LogOctoPrintRecord(OctoPrintPayload{
		SessionID: oSID,
		PrinterID: "ender3",
		FileName:  "octo.gcode",
		Status:    "completed",
		EndedAt:   time.Now().Add(-2 * time.Hour),
		Filament:  []OctoPrintPayloadFilament{
			{ToolIndex: 0, FilamentUsedMM: 1000, FilamentUsedG: 3.0},
		},
	})

	sessions, err := bridge.GetPrintSessions(10)
	if err != nil {
		t.Fatalf("GetPrintSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	sources := map[string]bool{}
	for _, s := range sessions {
		sources[s.Source] = true
	}
	for _, src := range []string{"virtual", "prusalink", "octoprint"} {
		if !sources[src] {
			t.Errorf("expected a session with source=%s", src)
		}
	}

	// Virtual session must have tool_count=2
	for _, s := range sessions {
		if s.Source == "virtual" && s.ToolCount != 2 {
			t.Errorf("virtual session: expected tool_count=2, got %d", s.ToolCount)
		}
	}
	t.Logf("✅ Mixed sources: %d sessions — virtual, prusalink, octoprint", len(sessions))
}

// TestSession_LegacyRowsEachFormOwnSession verifies that legacy print_history rows
// (no session_id) each appear as a separate session, not grouped together.
func TestSession_LegacyRowsEachFormOwnSession(t *testing.T) {
	bridge := testBridge(t)

	for i := 0; i < 3; i++ {
		_, err := bridge.db.Exec(`
			INSERT INTO print_history
				(printer_name, toolhead_id, spool_id, filament_used,
				 print_started, print_finished, job_name, status, print_time_minutes)
			VALUES ('MK3S+', 0, 1, 8.0,
			        '2026-01-01T10:00:00Z', '2026-01-01T11:00:00Z',
			        'legacy.gcode', 'completed', 60)`)
		if err != nil {
			t.Fatalf("insert legacy row %d: %v", i, err)
		}
	}

	sessions, err := bridge.GetPrintSessions(10)
	if err != nil {
		t.Fatalf("GetPrintSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Errorf("expected 3 separate sessions for 3 legacy rows, got %d", len(sessions))
	}
	for _, s := range sessions {
		if s.ToolCount != 1 {
			t.Errorf("legacy session should have tool_count=1, got %d", s.ToolCount)
		}
		if s.SessionID != "" {
			t.Errorf("legacy session should have empty session_id, got %s", s.SessionID)
		}
	}
	t.Logf("✅ Legacy rows: %d sessions, each tool_count=1, each session_id empty", len(sessions))
}
