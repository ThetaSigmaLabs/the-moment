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
	"testing"
)

// fakeMoonrakerProvider stands in for a live WebSocket client.
type fakeMoonrakerProvider struct {
	status  MoonrakerStatus
	err     error
	tracker *ExtrusionTracker
	closed  bool
}

func newFakeMoonrakerProvider() *fakeMoonrakerProvider {
	return &fakeMoonrakerProvider{tracker: NewExtrusionTracker()}
}

func (f *fakeMoonrakerProvider) GetCurrentStatus() (MoonrakerStatus, error) {
	return f.status, f.err
}
func (f *fakeMoonrakerProvider) Tracker() *ExtrusionTracker { return f.tracker }
func (f *fakeMoonrakerProvider) Close()                     { f.closed = true }

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
