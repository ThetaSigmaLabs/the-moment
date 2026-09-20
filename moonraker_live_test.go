// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// moonraker_live_test.go
// =============================================================================
// Opt-in test against a real Moonraker printer. Skipped unless MOONRAKER_LIVE_HOST
// is set, so it never runs in CI or for contributors without Klipper hardware.
//
//   MOONRAKER_LIVE_HOST=10.49.9.130 go test -run TestMoonrakerLive -v
//
// It is read-only: it subscribes and observes. It never assigns a spool to the
// tracker, so nothing can be billed and no write to Spoolman is possible.
//
// This exists because the mock proves we parse the shapes we *expect*. Only a
// real printer proves those are the shapes Klipper actually sends.
// =============================================================================

import (
	"os"
	"testing"
	"time"
)

func TestMoonrakerLiveConnect(t *testing.T) {
	host := os.Getenv("MOONRAKER_LIVE_HOST")
	if host == "" {
		t.Skip("set MOONRAKER_LIVE_HOST to run against a real printer")
	}

	c := NewMoonrakerClient(host, false)
	defer c.Close()

	// Wait for the subscribe snapshot to land.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c.Tracker().Ready() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !c.Tracker().Ready() {
		t.Fatalf("no baseline established from %s within 15s", host)
	}

	s, err := c.GetCurrentStatus()
	if err != nil {
		t.Fatalf("GetCurrentStatus: %v", err)
	}

	t.Logf("connected=%v klippy_ready=%v", s.Connected, s.KlippyReady)
	t.Logf("state=%q filename=%q", s.State, s.Filename)
	t.Logf("progress=%.4f filament_used=%.2fmm", s.Progress, s.FilamentUsed)
	t.Logf("layers=%d/%d", s.CurrentLayer, s.TotalLayer)
	t.Logf("nozzle=%.1f/%.1f bed=%.1f/%.1f",
		s.NozzleTemp, s.NozzleTarget, s.BedTemp, s.BedTarget)
	t.Logf("print_duration=%.0fs total_duration=%.0fs", s.PrintDuration, s.TotalDuration)

	if !s.Connected {
		t.Error("status reports not connected after a successful subscribe")
	}

	// Observe for a few seconds. With no active spool nothing is billed; this
	// only confirms the stream stays alive and the snapshot keeps updating.
	before := s.LastUpdate
	time.Sleep(5 * time.Second)

	after, err := c.GetCurrentStatus()
	if err != nil {
		t.Fatalf("GetCurrentStatus after observation: %v", err)
	}
	if !after.LastUpdate.After(before) {
		t.Logf("warning: no status updates in 5s (printer may be fully idle)")
	}

	if got := c.Tracker().ActiveSpool(); got != nil {
		t.Errorf("live test must never assign a spool; got %d", *got)
	}
}
