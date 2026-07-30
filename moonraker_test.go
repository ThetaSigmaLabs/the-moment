// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// moonraker_test.go
// =============================================================================
// Tests for the Moonraker WebSocket client, using a fake Moonraker built on
// httptest and the gorilla upgrader already vendored for the web UI. No real
// printer is involved.
//
// The client's job is to turn a JSON-RPC stream into (a) a dashboard snapshot
// and (b) correct input to ExtrusionTracker. The tests below pin the parts that
// are easy to get subtly wrong: baseline handling on (re)subscribe, and partial
// status updates.
//
// Run:
//   go test ./... -v -run TestMoonraker
// =============================================================================

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeMoonraker is a minimal Moonraker stand-in. It answers
// printer.objects.subscribe with a supplied snapshot, then forwards whatever
// notifications a test pushes onto its channel.
type fakeMoonraker struct {
	srv      *httptest.Server
	notify   chan map[string]interface{}
	snapshot map[string]interface{}
	hostPort string
}

func newFakeMoonraker(t *testing.T, snapshot map[string]interface{}) *fakeMoonraker {
	t.Helper()

	f := &fakeMoonraker{
		notify:   make(chan map[string]interface{}, 32),
		snapshot: snapshot,
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Await the subscribe request and reply with the snapshot.
		var req struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		if err := conn.ReadJSON(&req); err != nil {
			return
		}
		if req.Method != "printer.objects.subscribe" {
			return
		}
		reply := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"eventtime": 1234.5,
				"status":    f.snapshot,
			},
		}
		if err := conn.WriteJSON(reply); err != nil {
			return
		}

		// Forward notifications until the test finishes.
		for objects := range f.notify {
			msg := map[string]interface{}{
				"jsonrpc": "2.0",
				"method":  "notify_status_update",
				"params":  []interface{}{objects, 1234.6},
			}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	}))

	f.hostPort = strings.TrimPrefix(f.srv.URL, "http://")
	t.Cleanup(func() {
		close(f.notify)
		f.srv.Close()
	})
	return f
}

// waitFor polls cond until it holds or the deadline passes. The client is
// asynchronous, so tests cannot assert immediately after pushing a message.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func toolheadSnapshot(ePos float64, extruder string) map[string]interface{} {
	return map[string]interface{}{
		"toolhead": map[string]interface{}{
			"position": []float64{0, 0, 0, ePos},
			"extruder": extruder,
		},
	}
}

// -----------------------------------------------------------------------------
// Host normalisation
// -----------------------------------------------------------------------------

// Printer config stores a bare IP (the field is shared with PrusaLink), so the
// client has to supply Moonraker's port itself.
func TestMoonrakerNormalizeHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"10.49.9.130", "10.49.9.130:7125"},
		{"10.49.9.130:7125", "10.49.9.130:7125"},
		{"10.49.9.130:80", "10.49.9.130:80"},
		{"voron.local", "voron.local:7125"},
	}
	for _, c := range cases {
		if got := normalizeMoonrakerHost(c.in); got != c.want {
			t.Errorf("normalizeMoonrakerHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// -----------------------------------------------------------------------------
// Baseline handling
// -----------------------------------------------------------------------------

// The subscribe snapshot establishes the tracker baseline. It must NOT be
// billed: the axis was already at that position before we connected, and
// charging it would attribute a previous job's filament to the active spool.
func TestMoonrakerSubscribeSnapshotEstablishesBaselineWithoutBilling(t *testing.T) {
	f := newFakeMoonraker(t, toolheadSnapshot(5000, "extruder"))

	c := NewMoonrakerClient(f.hostPort, false)
	defer c.Close()

	waitFor(t, "tracker baseline", func() bool { return c.Tracker().Ready() })

	c.Tracker().SetActiveSpool(intPtr(12))

	if got := c.Tracker().PendingFor(12); got != 0 {
		t.Errorf("snapshot must not bill: pending = %.3f, want 0", got)
	}

	// Extrusion after the baseline bills only the delta.
	f.notify <- toolheadSnapshot(5010, "extruder")
	waitFor(t, "10mm billed", func() bool { return c.Tracker().PendingFor(12) >= 10 })

	if got := c.Tracker().PendingFor(12); got != 10 {
		t.Errorf("pending = %.3f, want 10", got)
	}
}

// A status update whose position array is too short carries no extruder axis
// and must be ignored rather than read as index-out-of-range or zero.
func TestMoonrakerShortPositionArrayIsIgnored(t *testing.T) {
	f := newFakeMoonraker(t, toolheadSnapshot(100, "extruder"))

	c := NewMoonrakerClient(f.hostPort, false)
	defer c.Close()

	waitFor(t, "tracker baseline", func() bool { return c.Tracker().Ready() })
	c.Tracker().SetActiveSpool(intPtr(12))

	f.notify <- map[string]interface{}{
		"toolhead": map[string]interface{}{"position": []float64{1, 2, 3}},
	}
	// Then a valid one, so we can be sure the first was processed and skipped
	// rather than merely still in flight.
	f.notify <- toolheadSnapshot(105, "extruder")

	waitFor(t, "valid update billed", func() bool { return c.Tracker().PendingFor(12) > 0 })

	if got := c.Tracker().PendingFor(12); got != 5 {
		t.Errorf("pending = %.3f, want 5 (short array must not bill)", got)
	}
}

// -----------------------------------------------------------------------------
// Partial status updates
// -----------------------------------------------------------------------------

// Moonraker sends only changed fields. Decoding into a plain struct would zero
// every absent key, blanking the dashboard on each incremental message. Every
// field is a pointer for this reason; this test pins that behaviour.
func TestMoonrakerPartialUpdateDoesNotBlankExistingFields(t *testing.T) {
	f := newFakeMoonraker(t, map[string]interface{}{
		"toolhead": map[string]interface{}{
			"position": []float64{0, 0, 0, 0},
			"extruder": "extruder",
		},
		"print_stats": map[string]interface{}{
			"state":          "printing",
			"filename":       "bracket.gcode",
			"filament_used":  1234.5,
			"print_duration": 600.0,
			"info":           map[string]interface{}{"current_layer": 12, "total_layer": 250},
		},
		"virtual_sdcard": map[string]interface{}{"progress": 0.25},
		"extruder":       map[string]interface{}{"temperature": 245.0, "target": 250.0},
		"heater_bed":     map[string]interface{}{"temperature": 99.5, "target": 100.0},
	})

	c := NewMoonrakerClient(f.hostPort, false)
	defer c.Close()

	waitFor(t, "initial snapshot", func() bool {
		s, err := c.GetCurrentStatus()
		return err == nil && s.Filename == "bracket.gcode"
	})

	// A progress-only update, exactly as Moonraker would send it.
	f.notify <- map[string]interface{}{
		"virtual_sdcard": map[string]interface{}{"progress": 0.26},
	}

	waitFor(t, "progress update", func() bool {
		s, _ := c.GetCurrentStatus()
		return s.Progress > 0.255
	})

	s, err := c.GetCurrentStatus()
	if err != nil {
		t.Fatalf("GetCurrentStatus: %v", err)
	}

	// Everything not mentioned in the update must survive it.
	if s.Filename != "bracket.gcode" {
		t.Errorf("Filename = %q, want it preserved", s.Filename)
	}
	if s.State != "printing" {
		t.Errorf("State = %q, want it preserved", s.State)
	}
	if s.FilamentUsed != 1234.5 {
		t.Errorf("FilamentUsed = %.1f, want it preserved", s.FilamentUsed)
	}
	if s.CurrentLayer != 12 || s.TotalLayer != 250 {
		t.Errorf("layers = %d/%d, want 12/250 preserved", s.CurrentLayer, s.TotalLayer)
	}
	if s.NozzleTemp != 245.0 || s.BedTemp != 99.5 {
		t.Errorf("temps = %.1f/%.1f, want preserved", s.NozzleTemp, s.BedTemp)
	}
}

// Layer counts come from print_stats.info, which PrusaLink cannot provide at
// all. Worth its own assertion since it is a headline reason for this printer
// type existing.
func TestMoonrakerReadsLayerCounts(t *testing.T) {
	f := newFakeMoonraker(t, map[string]interface{}{
		"toolhead": map[string]interface{}{"position": []float64{0, 0, 0, 0}},
		"print_stats": map[string]interface{}{
			"info": map[string]interface{}{"current_layer": 7, "total_layer": 300},
		},
	})

	c := NewMoonrakerClient(f.hostPort, false)
	defer c.Close()

	waitFor(t, "layer counts", func() bool {
		s, err := c.GetCurrentStatus()
		return err == nil && s.TotalLayer == 300
	})

	s, _ := c.GetCurrentStatus()
	if s.CurrentLayer != 7 {
		t.Errorf("CurrentLayer = %d, want 7", s.CurrentLayer)
	}
}

// -----------------------------------------------------------------------------
// Connection state
// -----------------------------------------------------------------------------

// An unreachable printer must surface as an error rather than hanging or
// panicking — the dashboard reads this on every render.
func TestMoonrakerStatusErrorsWhenNotConnected(t *testing.T) {
	// Port 1 is reserved and will not accept connections.
	c := NewMoonrakerClient("127.0.0.1:1", false)
	defer c.Close()

	if _, err := c.GetCurrentStatus(); err == nil {
		t.Error("expected an error while disconnected")
	}
}

// Close must be safe to call repeatedly; the monitor loop may race a shutdown.
func TestMoonrakerCloseIsIdempotent(t *testing.T) {
	c := NewMoonrakerClient("127.0.0.1:1", false)
	c.Close()
	c.Close()
}

// The subscribe request must ask for toolhead.position — the raw kinematic
// axis — and must never subscribe to gcode_move, whose gcode_position is
// rewritten by G92 and would silently lose extrusion.
func TestMoonrakerSubscribesToToolheadNotGcodeMove(t *testing.T) {
	objects := moonrakerSubscription()

	if _, ok := objects["gcode_move"]; ok {
		t.Error("must not subscribe to gcode_move: G92 rewrites gcode_position")
	}

	fields, ok := objects["toolhead"].([]string)
	if !ok {
		t.Fatal("toolhead subscription missing")
	}
	var hasPosition bool
	for _, f := range fields {
		if f == "position" {
			hasPosition = true
		}
	}
	if !hasPosition {
		t.Error("toolhead subscription must include position")
	}

	// Guard the wire format too, since this is what Klipper actually parses.
	if _, err := json.Marshal(objects); err != nil {
		t.Errorf("subscription must marshal: %v", err)
	}
}
