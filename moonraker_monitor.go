// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// moonraker_monitor.go
// =============================================================================
// Bridge-side integration for Klipper printers running Moonraker.
//
// This is the Moonraker counterpart to monitorBambu: the WebSocket client is
// long-lived, so the poll loop does not open a connection. It reads the cached
// status the client maintains, keeps the extrusion tracker pointed at the right
// spool, and lets the reporter drain that tracker to Spoolman on its own timer.
//
// Why the tracker is fed from here rather than from the client: the spool
// assignment lives in this application's database (toolhead_mappings), and the
// client deliberately knows nothing about the database so it stays testable
// against a bare WebSocket.
// =============================================================================

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// moonrakerToolhead is the toolhead index Moonraker printers map to.
//
// Klipper supports multiple extruders, but the tracker bills whichever extruder
// Klipper reports as active and rebaselines across tool changes, so a single
// mapping is correct for the single-extruder machines this supports today.
// Multi-extruder Klipper would need one mapping per extruder name.
const moonrakerToolhead = 0

// mapMoonrakerState converts print_stats.state into the application's shared
// printer states, mirroring mapBambuState.
//
// Klipper's "complete" and "cancelled" are distinct terminal states, unlike
// Bambu's FINISH, so a cancelled job reports STOPPED and does not get counted
// as a successful print.
func mapMoonrakerState(state string) string {
	switch strings.ToLower(state) {
	case "printing":
		return StatePrinting
	case "paused":
		return StatePaused
	case "complete":
		return StateFinished
	case "standby":
		return StateIdle
	case "cancelled", "error":
		return StateStopped
	case "":
		return StateOffline // no status received yet
	default:
		return state
	}
}

// moonrakerLogOnly reports whether Moonraker printers should compute usage
// without writing it to Spoolman.
//
// It fails safe: any error reading the setting, and any value that is not
// explicitly "false", leaves log-only ON. Getting this wrong in the permissive
// direction means double-deducting a spool alongside Moonraker's own [spoolman]
// component, which is unrecoverable without hand-editing Spoolman; getting it
// wrong in the restrictive direction only means usage is logged and not written.
func (b *FilamentBridge) moonrakerLogOnly() bool {
	v, err := b.GetConfigValue(ConfigKeyMoonrakerLogOnly)
	if err != nil {
		// An absent key is the normal default state, not a problem worth
		// reporting — and this runs on every flush, so logging it would spam.
		// Any other error is genuine and worth surfacing.
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("[MOONRAKER] Could not read %s (%v); defaulting to log-only",
				ConfigKeyMoonrakerLogOnly, err)
		}
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(v), "false")
}

// getOrCreateMoonrakerClient returns the live client for a printer, creating it
// and starting its Spoolman reporter on first use.
func (b *FilamentBridge) getOrCreateMoonrakerClient(printerID string, config PrinterConfig) MoonrakerStatusProvider {
	b.moonrakerMutex.Lock()
	defer b.moonrakerMutex.Unlock()

	if client, exists := b.moonrakerClients[printerID]; exists {
		return client
	}

	client := b.moonrakerClientFactory(config.IPAddress, config.DebugLog)
	b.moonrakerClients[printerID] = client
	b.moonrakerHosts[printerID] = config.IPAddress

	// The Spoolman client and the log-only switch are resolved on every flush
	// rather than captured here: the config can reload underneath us, and
	// log-only must take effect the moment it is set, not on next restart.
	reporter := NewMoonrakerReporterFunc(printerID, client.Tracker(), func() (spoolUsageReporter, bool) {
		b.mutex.RLock()
		spoolman := b.spoolman
		b.mutex.RUnlock()
		return spoolman, b.moonrakerLogOnly()
	})
	reporter.Start()
	b.moonrakerReporters[printerID] = reporter

	mode := "writing to Spoolman"
	if b.moonrakerLogOnly() {
		mode = "LOG-ONLY (no Spoolman writes)"
	}
	log.Printf("[MOONRAKER] Connected client for %s (%s) at %s — %s",
		printerID, config.Name, config.IPAddress, mode)

	return client
}

// closeMoonrakerClient tears down one printer's client and reporter.
// Caller must hold moonrakerMutex.
func (b *FilamentBridge) closeMoonrakerClient(printerID, reason string) {
	reporter, hasReporter := b.moonrakerReporters[printerID]
	client, hasClient := b.moonrakerClients[printerID]
	if !hasReporter && !hasClient {
		return
	}

	// Clear the spool first so the reporter's final flush cannot bill a printer
	// we are deliberately letting go of.
	if hasClient {
		client.Tracker().SetActiveSpool(nil)
	}
	if hasReporter {
		reporter.Stop()
		delete(b.moonrakerReporters, printerID)
	}
	if hasClient {
		client.Close()
		delete(b.moonrakerClients, printerID)
	}
	delete(b.moonrakerHosts, printerID)

	log.Printf("[MOONRAKER] Released client for %s (%s)", printerID, reason)
}

// reconcileMoonrakerClients closes clients whose printer no longer warrants
// one.
//
// This matters more here than for Bambu: a Bambu client only writes when the
// poll loop drives it, but a Moonraker reporter has its own 5-second timer. An
// orphaned one keeps debiting Spoolman for a printer that was deleted, retyped,
// or re-addressed — with nothing left polling it to notice.
func (b *FilamentBridge) reconcileMoonrakerClients(printers map[string]PrinterConfig) {
	b.moonrakerMutex.Lock()
	defer b.moonrakerMutex.Unlock()

	for printerID := range b.moonrakerClients {
		config, stillConfigured := printers[printerID]
		switch {
		case !stillConfigured:
			b.closeMoonrakerClient(printerID, "printer deleted")
		case config.PrinterType != PrinterTypeMoonraker:
			b.closeMoonrakerClient(printerID, "printer type changed")
		case config.IPAddress != b.moonrakerHosts[printerID]:
			// A new client is created on the next poll, pointed at the new host.
			b.closeMoonrakerClient(printerID, "address changed")
		}
	}
}

// syncMoonrakerSpool points the tracker at the spool currently mapped to this
// printer's toolhead.
//
// Called every poll because the mapping can change from the web UI mid-print.
// Setting the same spool twice is a no-op in the tracker, so the repeated call
// costs nothing; a genuine change rebaselines so filament extruded under the
// old spool is never billed to the new one.
func (b *FilamentBridge) syncMoonrakerSpool(printerName string, client MoonrakerStatusProvider) {
	spoolID, err := b.GetToolheadMapping(printerName, moonrakerToolhead)
	if err != nil {
		// Leave the previous assignment alone — a transient database error must
		// not silently stop billing an in-progress print.
		log.Printf("[MOONRAKER] Could not read spool mapping for %s: %v", printerName, err)
		return
	}

	tracker := client.Tracker()
	if spoolID == 0 {
		tracker.SetActiveSpool(nil) // no spool assigned; extrusion goes unbilled
		return
	}
	tracker.SetActiveSpool(&spoolID)
}

// monitorMoonraker polls one Klipper printer per ticker tick.
//
// The WebSocket client pushes updates continuously in its own goroutine; this
// function only samples the resulting cache, so it never blocks on the network.
func (b *FilamentBridge) monitorMoonraker(printerID string, config PrinterConfig) error {
	client := b.getOrCreateMoonrakerClient(printerID, config)
	cl := b.getCommLog(printerID)

	// Sync the spool before anything can return early.
	//
	// The first poll after startup always finds the client still connecting, so
	// doing this after the not-connected check would skip it exactly once — and
	// the WebSocket connects within milliseconds while the next poll is a full
	// interval away. Everything extruded in that gap would be seen by the
	// tracker with no spool assigned, and then discarded by the rebaseline when
	// the spool finally arrived.
	b.syncMoonrakerSpool(config.Name, client)

	status, err := client.GetCurrentStatus()
	if err != nil {
		// Not connected yet, or the printer is unreachable. The client retries
		// on its own backoff, so there is nothing to do but wait.
		cl.Append("RX", "error", fmt.Sprintf("GetCurrentStatus: %v", err), "")
		return nil
	}

	if !status.KlippyReady {
		// Moonraker is up but Klipper is not — a firmware restart or a shutdown
		// state. Reporting IDLE here would look like a finished print.
		cl.Append("RX", "ws_recv", "klippy not ready", "")
		return nil
	}

	currentState := mapMoonrakerState(status.State)
	progressPct := status.Progress * 100

	b.mutex.RLock()
	wasPrinting := b.wasPrinting[printerID]
	storedJobFile := b.currentJobFile[printerID]
	prevState := b.previousState[printerID]
	b.mutex.RUnlock()

	if prevState != currentState && prevState != "" {
		cl.Append("EV", "state_change", fmt.Sprintf("%s → %s", prevState, currentState), "")
	}
	cl.Append("RX", "ws_recv", fmt.Sprintf("state=%s progress=%.1f%% file=%q filament=%.1fmm",
		status.State, progressPct, status.Filename, status.FilamentUsed), "")

	switch currentState {

	case StatePrinting:
		b.mutex.Lock()
		if status.Filename != "" && storedJobFile == "" {
			b.currentJobFile[printerID] = status.Filename
			b.printStartTime[printerID] = time.Now()
			log.Printf("[MOONRAKER] 📁 Stored job filename for %s: %s", printerID, status.Filename)
		}
		b.wasPrinting[printerID] = true
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

	case StatePaused:
		b.mutex.Lock()
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

	case StateFinished, StateIdle, StateStopped:
		if wasPrinting {
			// Flush before clearing state so the last few millimetres extruded
			// before the job ended are attributed to this print, not the next.
			b.moonrakerMutex.RLock()
			reporter := b.moonrakerReporters[printerID]
			b.moonrakerMutex.RUnlock()
			if reporter != nil {
				reporter.Flush()
			}

			log.Printf("[MOONRAKER] Print ended on %s (%s): state=%s file=%s klipper_filament=%.1fmm",
				printerID, config.Name, currentState, storedJobFile, status.FilamentUsed)
		}
		b.mutex.Lock()
		b.wasPrinting[printerID] = false
		b.currentJobFile[printerID] = ""
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

	default:
		b.mutex.Lock()
		b.previousState[printerID] = currentState
		b.mutex.Unlock()
	}

	return nil
}

// CloseMoonrakerClients stops every reporter and disconnects every client.
//
// Reporters are stopped first: MoonrakerReporter.Stop performs a final flush,
// and that flush must happen while the tracker still holds its pending usage.
func (b *FilamentBridge) CloseMoonrakerClients() {
	b.moonrakerMutex.Lock()
	defer b.moonrakerMutex.Unlock()

	// Reporters first: Stop performs a final flush, and that flush must happen
	// while the tracker still holds its pending millimetres.
	for id, reporter := range b.moonrakerReporters {
		reporter.Stop()
		delete(b.moonrakerReporters, id)
	}
	for id, client := range b.moonrakerClients {
		client.Close()
		delete(b.moonrakerClients, id)
	}
	b.moonrakerHosts = make(map[string]string)
}
