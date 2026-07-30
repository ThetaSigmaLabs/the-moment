// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// moonraker_monitor_test.go
// =============================================================================
// Tests for the bridge-side Moonraker integration seam.
//
// The client, tracker and reporter each have their own unit tests. What these
// cover is the wiring between them and the bridge: that a Moonraker printer is
// not mistaken for a PrusaLink one, that the spool assignment reaches the
// tracker, and that log-only fails safe.
//
// Run:
//   go test ./... -v -run TestMoonraker
// =============================================================================

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeMoonrakerProvider stands in for a live WebSocket client.
type fakeMoonrakerProvider struct {
	status  MoonrakerStatus
	err     error
	tracker *ExtrusionTracker
	closed  bool

	// activeSpool is what Moonraker's [spoolman] reports. spoolErr defaults to
	// ErrMoonrakerNoSpoolman so tests that do not care about the component
	// behave as if it is absent and The Moment's own mapping is authoritative.
	activeSpool int
	spoolErr    error
	spoolSetTo  int

	// lastJob is what Moonraker's own history reports. Nil means no matching
	// job, which sends the caller to the tracker fallback.
	lastJob    *MoonrakerJob
	lastJobErr error
}

func newFakeMoonrakerProvider() *fakeMoonrakerProvider {
	return &fakeMoonrakerProvider{
		tracker:  NewExtrusionTracker(),
		spoolErr: ErrMoonrakerNoSpoolman,
	}
}

func (f *fakeMoonrakerProvider) GetCurrentStatus() (MoonrakerStatus, error) {
	return f.status, f.err
}
func (f *fakeMoonrakerProvider) Tracker() *ExtrusionTracker { return f.tracker }
func (f *fakeMoonrakerProvider) Close()                     { f.closed = true }

func (f *fakeMoonrakerProvider) ActiveSpoolID() (int, error) {
	return f.activeSpool, f.spoolErr
}

func (f *fakeMoonrakerProvider) SetActiveSpoolID(spoolID int) error {
	f.spoolSetTo = spoolID
	return nil
}

func (f *fakeMoonrakerProvider) LastCompletedJob(filename string) (*MoonrakerJob, error) {
	return f.lastJob, f.lastJobErr
}

// moonrakerTestConfig returns a PrinterConfig for Moonraker tests. No real host
// is contacted — the factory override intercepts it.
func moonrakerTestConfig(name string) PrinterConfig {
	return PrinterConfig{
		Name:        name,
		Model:       "Voron Trident",
		IPAddress:   "10.0.0.99",
		Toolheads:   1,
		PrinterType: PrinterTypeMoonraker,
	}
}

// installFakeMoonraker points the bridge at a fake client and returns it.
func installFakeMoonraker(b *FilamentBridge) *fakeMoonrakerProvider {
	fake := newFakeMoonrakerProvider()
	b.moonrakerClientFactory = func(host string, debugLog bool) MoonrakerStatusProvider {
		return fake
	}
	return fake
}

// -----------------------------------------------------------------------------
// State mapping
// -----------------------------------------------------------------------------

// Klipper reports "complete" and "cancelled" as distinct terminal states. They
// must not collapse into one, or a cancelled job counts as a finished print.
func TestMapMoonrakerState(t *testing.T) {
	cases := map[string]string{
		"printing":  StatePrinting,
		"paused":    StatePaused,
		"complete":  StateFinished,
		"standby":   StateIdle,
		"cancelled": StateStopped,
		"error":     StateStopped,
		"":          StateOffline,
		"PRINTING":  StatePrinting, // case-insensitive
	}
	for in, want := range cases {
		if got := mapMoonrakerState(in); got != want {
			t.Errorf("mapMoonrakerState(%q) = %q, want %q", in, got, want)
		}
	}
}

// -----------------------------------------------------------------------------
// Client lifecycle
// -----------------------------------------------------------------------------

// The client must be created once and reused — a new WebSocket per poll would
// reconnect every tick and reset the extrusion baseline each time.
func TestMoonrakerClientIsReused(t *testing.T) {
	b := newTestBridge(t)
	calls := 0
	fake := newFakeMoonrakerProvider()
	b.moonrakerClientFactory = func(host string, debugLog bool) MoonrakerStatusProvider {
		calls++
		return fake
	}

	cfg := moonrakerTestConfig("Voron")
	for i := 0; i < 3; i++ {
		if err := b.monitorMoonraker("mr1", cfg); err != nil {
			t.Fatalf("monitorMoonraker: %v", err)
		}
	}

	if calls != 1 {
		t.Errorf("factory called %d times, want 1 (client must be reused)", calls)
	}
}

// Closing must stop reporters and clients, and must be safe to call when no
// Moonraker printer was ever configured.
func TestCloseMoonrakerClients(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)

	if err := b.monitorMoonraker("mr1", moonrakerTestConfig("Voron")); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	b.CloseMoonrakerClients()

	if !fake.closed {
		t.Error("client was not closed")
	}
	b.moonrakerMutex.RLock()
	nClients, nReporters := len(b.moonrakerClients), len(b.moonrakerReporters)
	b.moonrakerMutex.RUnlock()
	if nClients != 0 || nReporters != 0 {
		t.Errorf("after close: %d clients, %d reporters; want 0 and 0", nClients, nReporters)
	}

	// Idempotent — Close() calls this too.
	b.CloseMoonrakerClients()
}

// -----------------------------------------------------------------------------
// Spool assignment
// -----------------------------------------------------------------------------

// The spool mapping lives in this application's database, so the poll loop is
// what must carry it to the tracker. Without this, extrusion is never billed.
func TestMoonrakerSpoolMappingReachesTracker(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	if err := b.SetToolheadMapping(cfg.Name, moonrakerToolhead, 12); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	fake.status = MoonrakerStatus{Connected: true, KlippyReady: true, State: "printing"}
	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	spool := fake.tracker.ActiveSpool()
	if spool == nil {
		t.Fatal("tracker has no active spool; the mapping never reached it")
	}
	if *spool != 12 {
		t.Errorf("active spool = %d, want 12", *spool)
	}
}

// An unmapped toolhead must clear the spool rather than keep billing whichever
// spool happened to be assigned before.
func TestMoonrakerNoMappingClearsSpool(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	fake.status = MoonrakerStatus{Connected: true, KlippyReady: true, State: "standby"}
	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	if fake.tracker.ActiveSpool() != nil {
		t.Error("tracker has an active spool with no mapping configured")
	}
}

// -----------------------------------------------------------------------------
// Connection edge cases
// -----------------------------------------------------------------------------

// A printer that is unreachable must not error the poll cycle — the client
// reconnects on its own backoff.
func TestMoonrakerDisconnectedIsNotAnError(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	fake.err = errNotConnectedForTest{}

	if err := b.monitorMoonraker("mr1", moonrakerTestConfig("Voron")); err != nil {
		t.Errorf("monitorMoonraker returned %v; a disconnected printer must not error", err)
	}
}

// Moonraker up but Klippy down (firmware restart / shutdown) must not be
// recorded as IDLE — that would look like a print finished.
func TestMoonrakerKlippyNotReadyDoesNotEndPrint(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	// Establish an in-progress print.
	fake.status = MoonrakerStatus{Connected: true, KlippyReady: true, State: "printing", Filename: "bench.gcode"}
	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}
	b.mutex.RLock()
	printing := b.wasPrinting["mr1"]
	b.mutex.RUnlock()
	if !printing {
		t.Fatal("setup failed: printer not marked as printing")
	}

	// Klipper restarts mid-print.
	fake.status = MoonrakerStatus{Connected: true, KlippyReady: false, State: "standby"}
	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	b.mutex.RLock()
	stillPrinting := b.wasPrinting["mr1"]
	b.mutex.RUnlock()
	if !stillPrinting {
		t.Error("klippy-not-ready ended the print; it must be ignored, not treated as IDLE")
	}
}

// -----------------------------------------------------------------------------
// Log-only safety
// -----------------------------------------------------------------------------

// Log-only must default ON. Defaulting off would double-deduct every spool
// alongside Moonraker's own [spoolman] component, which cannot be undone
// without hand-editing Spoolman.
func TestMoonrakerLogOnlyDefaultsOn(t *testing.T) {
	b := newTestBridge(t)
	if !b.moonrakerLogOnly() {
		t.Error("log-only defaulted to OFF on a fresh database; it must fail safe to ON")
	}
}

// Only an explicit "false" turns writes on. Anything else — including a typo —
// stays in the safe mode.
func TestMoonrakerLogOnlyOnlyExplicitFalseEnablesWrites(t *testing.T) {
	cases := map[string]bool{
		"false": false, // the one value that enables writing
		"False": false, // case-insensitive
		"true":  true,
		"":      true,
		"no":    true, // typo must not silently enable writes
	}
	for value, wantLogOnly := range cases {
		b := newTestBridge(t)
		if err := b.SetConfigValue(ConfigKeyMoonrakerLogOnly, value); err != nil {
			t.Fatalf("SetConfigValue(%q): %v", value, err)
		}
		if got := b.moonrakerLogOnly(); got != wantLogOnly {
			t.Errorf("%s=%q → logOnly=%v, want %v", ConfigKeyMoonrakerLogOnly, value, got, wantLogOnly)
		}
	}
}

// errNotConnectedForTest mimics the client's not-connected error.
type errNotConnectedForTest struct{}

func (errNotConnectedForTest) Error() string { return "moonraker: not connected" }

// -----------------------------------------------------------------------------
// Per-printer teardown
// -----------------------------------------------------------------------------
//
// A Moonraker reporter drives itself on a 5s timer, unlike the Bambu client
// which only acts when the poll loop calls it. So a client left behind after
// its printer is gone keeps debiting Spoolman with nothing left to notice.

func TestMoonrakerReconcileReleasesDeletedPrinter(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	// Printer no longer in the config snapshot.
	b.reconcileMoonrakerClients(map[string]PrinterConfig{})

	if !fake.closed {
		t.Error("client for a deleted printer was not closed")
	}
	b.moonrakerMutex.RLock()
	n := len(b.moonrakerReporters)
	b.moonrakerMutex.RUnlock()
	if n != 0 {
		t.Errorf("%d reporter(s) still running for a deleted printer, want 0", n)
	}
}

func TestMoonrakerReconcileReleasesRetypedPrinter(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	retyped := cfg
	retyped.PrinterType = PrinterTypePrusaLink
	b.reconcileMoonrakerClients(map[string]PrinterConfig{"mr1": retyped})

	if !fake.closed {
		t.Error("client was not closed after the printer type changed away from moonraker")
	}
}

// A corrected IP must not keep billing from the old host.
func TestMoonrakerReconcileReleasesOnAddressChange(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	moved := cfg
	moved.IPAddress = "10.0.0.250"
	b.reconcileMoonrakerClients(map[string]PrinterConfig{"mr1": moved})

	if !fake.closed {
		t.Error("client was not closed after the printer address changed")
	}
}

// Reconcile must leave a still-valid printer alone, or every poll would
// reconnect and reset the extrusion baseline.
func TestMoonrakerReconcileKeepsUnchangedPrinter(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	b.reconcileMoonrakerClients(map[string]PrinterConfig{"mr1": cfg})

	if fake.closed {
		t.Error("reconcile closed a printer that had not changed")
	}
}

// Releasing a printer must clear its spool first, so the reporter's final
// flush cannot bill a printer we are deliberately letting go of.
func TestMoonrakerReleaseClearsSpoolBeforeFinalFlush(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	if err := b.SetToolheadMapping(cfg.Name, moonrakerToolhead, 12); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}
	if fake.tracker.ActiveSpool() == nil {
		t.Fatal("setup failed: spool was never assigned")
	}

	b.reconcileMoonrakerClients(map[string]PrinterConfig{})

	if fake.tracker.ActiveSpool() != nil {
		t.Error("spool still active after the printer was released")
	}
}

// -----------------------------------------------------------------------------
// Log-only must not be latched
// -----------------------------------------------------------------------------

// Turning log-only back ON is the emergency stop for a runaway double-write.
// If the reporter captured the value at construction, the switch would do
// nothing until the process restarted — which is exactly when an operator
// needs it to work.
func TestMoonrakerLogOnlyTakesEffectWithoutRestart(t *testing.T) {
	b := newTestBridge(t)
	installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	// Start in write mode.
	if err := b.SetConfigValue(ConfigKeyMoonrakerLogOnly, "false"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	if err := b.SetToolheadMapping(cfg.Name, moonrakerToolhead, 12); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}
	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	b.moonrakerMutex.RLock()
	reporter := b.moonrakerReporters["mr1"]
	b.moonrakerMutex.RUnlock()
	if reporter == nil {
		t.Fatal("no reporter created")
	}

	if _, logOnly := reporter.resolve(); logOnly {
		t.Fatal("setup failed: expected write mode before the switch is flipped")
	}

	// Operator hits the emergency stop.
	if err := b.SetConfigValue(ConfigKeyMoonrakerLogOnly, "true"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}

	spool, logOnly := reporter.resolve()
	if !logOnly {
		t.Error("log-only did not take effect without a restart")
	}
	if spool == nil {
		t.Error("resolve returned a nil Spoolman client")
	}
}

// -----------------------------------------------------------------------------
// Moonraker owns the spool assignment
// -----------------------------------------------------------------------------
//
// Mainsail and Fluidd read and write Moonraker's [spoolman] spool_id, and those
// are the UIs people have open while a print runs. Treating Moonraker as the
// source of truth is what lets the spool be set from either place without the
// two drifting apart.

func TestMoonrakerSpoolAssignmentIsAdoptedFromMoonraker(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	// The operator picks a different spool in Mainsail.
	fake.activeSpool = 7
	fake.spoolErr = nil
	if err := b.SetToolheadMapping(cfg.Name, moonrakerToolhead, 12); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	spool := fake.tracker.ActiveSpool()
	if spool == nil || *spool != 7 {
		t.Errorf("tracker spool = %v, want 7 (Moonraker's assignment wins)", spool)
	}

	// And our own mapping is updated to match, so the two do not diverge.
	got, err := b.GetToolheadMapping(cfg.Name, moonrakerToolhead)
	if err != nil {
		t.Fatalf("GetToolheadMapping: %v", err)
	}
	if got != 7 {
		t.Errorf("stored mapping = %d, want 7 (mirrored from Moonraker)", got)
	}
}

// Clearing the spool in Mainsail must clear it here too, rather than leaving us
// billing a spool the operator has unassigned.
func TestMoonrakerClearedSpoolIsAdopted(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	fake.activeSpool = 0 // component present, nothing assigned
	fake.spoolErr = nil
	if err := b.SetToolheadMapping(cfg.Name, moonrakerToolhead, 12); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	if fake.tracker.ActiveSpool() != nil {
		t.Error("tracker still has a spool after Moonraker cleared it")
	}
	got, err := b.GetToolheadMapping(cfg.Name, moonrakerToolhead)
	if err != nil {
		t.Fatalf("GetToolheadMapping: %v", err)
	}
	if got != 0 {
		t.Errorf("stored mapping = %d, want 0 (cleared to match Moonraker)", got)
	}
}

// Without a [spoolman] section Moonraker holds no assignment, so The Moment's
// own mapping stands. This is the path for Klipper printers that do not use
// Spoolman through Moonraker at all.
func TestMoonrakerWithoutSpoolmanUsesOwnMapping(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	fake.spoolErr = ErrMoonrakerNoSpoolman
	if err := b.SetToolheadMapping(cfg.Name, moonrakerToolhead, 12); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	spool := fake.tracker.ActiveSpool()
	if spool == nil || *spool != 12 {
		t.Errorf("tracker spool = %v, want 12 (our mapping stands)", spool)
	}
}

// An unreachable Moonraker must not stop billing mid-print — fall back to the
// mapping we already hold rather than clearing it.
func TestMoonrakerSpoolQueryFailureKeepsExistingMapping(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	cfg := moonrakerTestConfig("Voron")

	fake.spoolErr = errNotConnectedForTest{}
	if err := b.SetToolheadMapping(cfg.Name, moonrakerToolhead, 12); err != nil {
		t.Fatalf("SetToolheadMapping: %v", err)
	}

	if err := b.monitorMoonraker("mr1", cfg); err != nil {
		t.Fatalf("monitorMoonraker: %v", err)
	}

	spool := fake.tracker.ActiveSpool()
	if spool == nil || *spool != 12 {
		t.Errorf("tracker spool = %v, want 12 (kept through a query failure)", spool)
	}
}

// -----------------------------------------------------------------------------
// Print history: mm -> grams
// -----------------------------------------------------------------------------

// spoolWithFilament serves one spool record with the given density/diameter.
func spoolWithFilament(t *testing.T, density, diameter float64) *SpoolmanClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":12,"filament":{"id":4,"name":"BLACK","density":%v,"diameter":%v}}`,
			density, diameter)
	}))
	t.Cleanup(srv.Close)
	return &SpoolmanClient{baseURL: srv.URL, httpClient: srv.Client()}
}

// The tracker works in millimetres so Spoolman can apply the real density.
// History stores grams, so the conversion happens here — and it must use the
// spool's own density, not a constant. The PrusaLink path falls back to a
// hardcoded 1.24, which overstates ABS+ by 19%.
func TestMoonrakerGramsUsesSpoolDensity(t *testing.T) {
	b := newTestBridge(t)

	// 1307.639mm of 1.75mm ABS+ at density 1.04 — the figures measured on a
	// real print, where Spoolman independently recorded 3.2710g.
	b.spoolman = spoolWithFilament(t, 1.04, 1.75)

	got, err := b.moonrakerGramsForSpool(12, 1307.639)
	if err != nil {
		t.Fatalf("moonrakerGramsForSpool: %v", err)
	}
	if math.Abs(got-3.2710) > 0.001 {
		t.Errorf("got %.4fg, want 3.2710g (Spoolman's own figure for this print)", got)
	}

	// The same length of PLA-density filament must NOT produce the same answer.
	b.spoolman = spoolWithFilament(t, 1.24, 1.75)
	pla, err := b.moonrakerGramsForSpool(12, 1307.639)
	if err != nil {
		t.Fatalf("moonrakerGramsForSpool: %v", err)
	}
	if math.Abs(pla-got) < 0.5 {
		t.Errorf("density is being ignored: ABS+ %.4fg vs PLA %.4fg", got, pla)
	}
}

// -----------------------------------------------------------------------------
// Per-print usage comes from Moonraker, not our tracker
// -----------------------------------------------------------------------------

// Moonraker's job history is the authoritative per-print figure — it is the
// number Spoolman was debited, and it has no in-memory state to lose.
//
// This is not theoretical. Restarting The Moment rebaselines the tracker's
// high-water mark to wherever the extruder axis sits. After a tip-shaping
// retraction that is BELOW the previous mark, so the next print re-bills the
// re-prime. Measured on hardware: the tracker said 1293.77mm for a print
// Moonraker and Spoolman both recorded as 1244.69mm — 49mm (3.9%) too high.
func TestMoonrakerUsagePrefersMoonrakerHistory(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)

	// The tracker holds the inflated figure a restart would produce.
	b.moonrakerMutex.Lock()
	b.moonrakerPrintStart["mr1"] = 2504.051
	b.moonrakerMutex.Unlock()
	fake.tracker.Reset(0, "extruder")
	fake.tracker.SetActiveSpool(intPtr(12))
	fake.tracker.HandleStatusUpdate(3797.818, "extruder")

	// Moonraker reports what actually happened.
	fake.lastJob = &MoonrakerJob{
		JobID:          "000126",
		Filename:       "Cube_ABS_8m36s.gcode",
		Status:         "completed",
		FilamentUsed:   1244.686,
		SlicerFilament: 1225.74,
	}

	used, source := b.moonrakerPrintUsage("mr1", fake, "Cube_ABS_8m36s.gcode")

	if source != "moonraker history" {
		t.Errorf("source = %q, want %q", source, "moonraker history")
	}
	if math.Abs(used-1244.686) > 0.001 {
		t.Errorf("used = %.3fmm, want 1244.686 (Moonraker's figure, not the tracker's)", used)
	}
}

// Without Moonraker history — no history component, or the job has not landed
// yet — the tracker still provides a figure rather than losing the print.
func TestMoonrakerUsageFallsBackToTracker(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)

	b.moonrakerMutex.Lock()
	b.moonrakerPrintStart["mr1"] = 1000
	b.moonrakerMutex.Unlock()
	fake.tracker.Reset(0, "extruder")
	fake.tracker.SetActiveSpool(intPtr(12))
	fake.tracker.HandleStatusUpdate(1500, "extruder")

	fake.lastJob = nil // no matching job

	used, source := b.moonrakerPrintUsage("mr1", fake, "Cube_ABS_8m36s.gcode")

	if source != "tracker" {
		t.Errorf("source = %q, want %q", source, "tracker")
	}
	if math.Abs(used-500) > 0.001 {
		t.Errorf("used = %.3fmm, want 500", used)
	}
}

// A print already running when we connected has no baseline, so neither source
// can produce an honest number. Reporting zero lets the caller skip the record
// rather than invent one.
func TestMoonrakerUsageWithoutBaselineIsZero(t *testing.T) {
	b := newTestBridge(t)
	fake := installFakeMoonraker(b)
	fake.lastJob = nil

	used, source := b.moonrakerPrintUsage("mr1", fake, "Cube_ABS_8m36s.gcode")

	if used != 0 || source != "none" {
		t.Errorf("used=%.3f source=%q, want 0 and \"none\"", used, source)
	}
}

// Without a usable density the conversion must fail loudly rather than invent
// one — silently guessing is the bug this whole path exists to avoid.
func TestMoonrakerGramsRefusesWithoutDensity(t *testing.T) {
	b := newTestBridge(t)
	b.spoolman = spoolWithFilament(t, 0, 1.75)

	if _, err := b.moonrakerGramsForSpool(12, 1000); err == nil {
		t.Error("expected an error when the filament has no density")
	}

	if _, err := b.moonrakerGramsForSpool(0, 1000); err == nil {
		t.Error("expected an error when no spool is assigned")
	}
}
