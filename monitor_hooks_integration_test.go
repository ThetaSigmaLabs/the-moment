//go:build integration

// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

// =============================================================================
// monitor_hooks_integration_test.go
// =============================================================================
// Integration tests for the NFC print-hook with mock PrusaLink + Spoolman:
//   - printStartTime recorded on first StatePrinting detection
//   - printStartTime not overwritten on subsequent PRINTING polls
//   - PrusaLink print completion creates print_spool_events for NFC assignments
// =============================================================================

import (
	"testing"
	"time"
)

// TestPrintStartTime_RecordedOnFirstDetection verifies that printStartTime is set
// when StatePrinting is first seen (storedJobFile was empty) and is not overwritten
// on subsequent PRINTING polls for the same job.
func TestPrintStartTime_RecordedOnFirstDetection(t *testing.T) {
	printer := NewMockPrusaLink(t)
	spoolman := NewMockSpoolman(t, map[int]float64{1: 1000})

	t.Setenv("THE_MOMENT_DB_PATH", t.TempDir())
	bridge, err := NewFilamentBridge(nil)
	if err != nil {
		t.Fatalf("NewFilamentBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })

	config, err := LoadConfig(bridge)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	config.SpoolmanURL = spoolman.URL()
	config.PrusaLinkTimeout = 5
	config.PrusaLinkFileDownloadTimeout = 10
	bridge.UpdateConfig(config)

	const printerID = "test-printer-id"
	printer.SetGcodeUsage(map[int]float64{0: 10})

	// Before first print: start time must be zero
	bridge.mutex.RLock()
	before := bridge.printStartTime[printerID]
	bridge.mutex.RUnlock()
	if !before.IsZero() {
		t.Error("expected zero printStartTime before any print")
	}

	// Poll 1: IDLE — start time stays zero
	printer.SetState(StateIdle)
	cfg := printer.PrinterConfig("Test Printer", 1)
	if err := bridge.SavePrinterConfig(printerID, cfg); err != nil {
		t.Fatalf("SavePrinterConfig: %v", err)
	}
	if err := bridge.monitorPrusaLink(printerID, cfg); err != nil {
		t.Fatalf("monitorPrusaLink idle: %v", err)
	}
	bridge.mutex.RLock()
	afterIdle := bridge.printStartTime[printerID]
	bridge.mutex.RUnlock()
	if !afterIdle.IsZero() {
		t.Error("printStartTime set during IDLE — should not happen")
	}

	// Poll 2: PRINTING (first detection) — start time must be set
	beforePoll := time.Now()
	printer.SetState(StatePrinting)
	if err := bridge.monitorPrusaLink(printerID, cfg); err != nil {
		t.Fatalf("monitorPrusaLink printing: %v", err)
	}
	afterPoll := time.Now()

	bridge.mutex.RLock()
	startTime := bridge.printStartTime[printerID]
	bridge.mutex.RUnlock()

	if startTime.IsZero() {
		t.Fatal("printStartTime not set after first StatePrinting detection")
	}
	if startTime.Before(beforePoll) || startTime.After(afterPoll) {
		t.Errorf("printStartTime %v outside expected range [%v, %v]", startTime, beforePoll, afterPoll)
	}

	// Poll 3: PRINTING again (second poll, same job) — start time must not change
	printer.SetState(StatePrinting)
	if err := bridge.monitorPrusaLink(printerID, cfg); err != nil {
		t.Fatalf("monitorPrusaLink printing 2: %v", err)
	}

	bridge.mutex.RLock()
	startTime2 := bridge.printStartTime[printerID]
	bridge.mutex.RUnlock()

	if !startTime2.Equal(startTime) {
		t.Errorf("printStartTime changed on second PRINTING poll: was %v, now %v", startTime, startTime2)
	}
}

// TestPrusaLinkPrintHook_SnapshotCreated verifies that completing a PrusaLink
// print lifecycle creates print_spool_events rows for the NFC-assigned spool and
// still updates Spoolman correctly (existing behaviour preserved).
func TestPrusaLinkPrintHook_SnapshotCreated(t *testing.T) {
	const spoolID = 42
	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{spoolID: 500})

	// Old-style toolhead mapping (Spoolman update path)
	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	// NFC assignment for the same toolhead
	const printerID = "test-printer-id"
	if err := bridge.SetAssignment(printerID, 0, spoolID, "manual"); err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}

	printer.SetGcodeUsage(map[int]float64{0: 15.0})

	// IDLE → PRINTING → IDLE
	printer.SetState(StateIdle)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StatePrinting)
	poll(t, bridge, printer, "Core One L", 1)

	printer.SetState(StateIdle)
	poll(t, bridge, printer, "Core One L", 1)

	// Existing behaviour: Spoolman receives the filament deduction
	if len(spoolman.UpdatesForSpool(spoolID)) == 0 {
		t.Fatal("Spoolman was not updated after print completed")
	}

	// New behaviour: the most recent print_history row has a 'start' spool event
	var printID int
	if err := bridge.db.QueryRow(
		`SELECT id FROM print_history ORDER BY id DESC LIMIT 1`,
	).Scan(&printID); err != nil {
		t.Fatalf("no print_history row: %v", err)
	}

	events, err := bridge.GetPrintSpoolEvents(printID)
	if err != nil {
		t.Fatalf("GetPrintSpoolEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no print_spool_events rows created for PrusaLink print")
	}

	found := false
	for _, e := range events {
		if e.NewSpoolmanSpoolID == spoolID && e.EventType == "start" {
			found = true
		}
	}
	if !found {
		t.Errorf("no 'start' event for spool %d; events: %+v", spoolID, events)
	}
}

// TestPrusaLinkPrintHook_NoNFCAssignment verifies that a print completes cleanly
// even when there are no NFC assignments (zero spool events, no error).
func TestPrusaLinkPrintHook_NoNFCAssignment(t *testing.T) {
	const spoolID = 7
	bridge, printer, spoolman := setupBridgeWithMocks(t, map[int]float64{spoolID: 300})

	if err := bridge.SetToolheadMapping("Core One L", 0, spoolID); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	// Intentionally no SetAssignment call — no NFC assignments exist

	printer.SetGcodeUsage(map[int]float64{0: 8.0})
	printer.SetState(StatePrinting)
	poll(t, bridge, printer, "Core One L", 1)
	printer.SetState(StateIdle)
	poll(t, bridge, printer, "Core One L", 1)

	if len(spoolman.UpdatesForSpool(spoolID)) == 0 {
		t.Fatal("Spoolman not updated — base behaviour broken")
	}

	var printID int
	if err := bridge.db.QueryRow(
		`SELECT id FROM print_history ORDER BY id DESC LIMIT 1`,
	).Scan(&printID); err != nil {
		t.Fatalf("no print_history row: %v", err)
	}

	// Zero spool events is fine when no NFC assignments exist
	if n := countSpoolEvents(t, bridge, printID); n != 0 {
		t.Errorf("expected 0 spool events (no NFC assignments), got %d", n)
	}
}
