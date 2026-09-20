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
	"math"
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

// moonrakerTimeRemaining estimates seconds left in the current print.
//
// Klipper does not publish a remaining-time figure — Mainsail and Fluidd
// compute their own. This uses the file-progress method: elapsed time scaled by
// how much of the job is left. It is rough early on, when a slow first layer
// skews the rate, and settles as the print proceeds.
func moonrakerTimeRemaining(s MoonrakerStatus) int {
	// Below a few percent the estimate is dominated by heat-up and the first
	// layer, so reporting nothing beats reporting a wild number.
	if s.Progress <= 0.02 || s.Progress >= 1 || s.PrintDuration <= 0 {
		return 0
	}
	total := s.PrintDuration / s.Progress
	remaining := total - s.PrintDuration
	if remaining < 0 {
		return 0
	}
	return int(remaining)
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
	delete(b.moonrakerPrintStart, printerID)

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

	// Moonraker's [spoolman] assignment wins when it has one.
	//
	// Mainsail and Fluidd read and write that same value, and those are the UIs
	// people have open while a print runs. Treating Moonraker as the source of
	// truth means the spool can be set from either place without the two
	// drifting apart, and it keeps Moonraker billing the spool the operator
	// actually selected.
	if mrSpool, err := client.ActiveSpoolID(); err == nil {
		if mrSpool != spoolID {
			if mrSpool == 0 {
				// Cleared in Moonraker: drop ours to match rather than keep
				// billing a spool the operator has unassigned.
				if err := b.UnmapToolhead(printerName, moonrakerToolhead); err != nil {
					log.Printf("[MOONRAKER] Could not clear spool mapping for %s: %v", printerName, err)
				}
			} else if err := b.SetToolheadMapping(printerName, moonrakerToolhead, mrSpool); err != nil {
				log.Printf("[MOONRAKER] Could not mirror Moonraker spool %d for %s: %v",
					mrSpool, printerName, err)
			} else {
				log.Printf("[MOONRAKER] Adopted spool %d from Moonraker for %s", mrSpool, printerName)
			}
		}
		spoolID = mrSpool
	} else if !errors.Is(err, ErrMoonrakerNoSpoolman) {
		// Unreachable or erroring: keep using our own mapping rather than
		// stopping billing mid-print.
		log.Printf("[MOONRAKER] Could not read active spool from %s: %v", printerName, err)
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
		newJob := status.Filename != "" && storedJobFile == ""

		b.mutex.Lock()
		if newJob {
			b.currentJobFile[printerID] = status.Filename
			b.printStartTime[printerID] = time.Now()
			log.Printf("[MOONRAKER] 📁 Stored job filename for %s: %s", printerID, status.Filename)
		}
		b.wasPrinting[printerID] = true
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

		// Remember what had been billed when the job started, so the difference
		// at print end is exactly this print's consumption.
		if newJob {
			b.moonrakerMutex.Lock()
			b.moonrakerPrintStart[printerID] = client.Tracker().BilledTotal()
			b.moonrakerMutex.Unlock()
		}

	case StatePaused:
		b.mutex.Lock()
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

	case StateFinished, StateIdle, StateStopped:
		if wasPrinting {
			// Flush before reading the total so the last few millimetres
			// extruded before the job ended belong to this print, not the next.
			b.moonrakerMutex.RLock()
			reporter := b.moonrakerReporters[printerID]
			b.moonrakerMutex.RUnlock()
			if reporter != nil {
				reporter.Flush()
			}

			if err := b.handleMoonrakerPrintFinished(printerID, config, client, storedJobFile, currentState); err != nil {
				log.Printf("[MOONRAKER] Could not record print history for %s: %v", printerID, err)
			}
		}
		b.mutex.Lock()
		b.wasPrinting[printerID] = false
		b.currentJobFile[printerID] = ""
		b.previousState[printerID] = currentState
		b.mutex.Unlock()

		b.moonrakerMutex.Lock()
		delete(b.moonrakerPrintStart, printerID)
		b.moonrakerMutex.Unlock()

	default:
		b.mutex.Lock()
		b.previousState[printerID] = currentState
		b.mutex.Unlock()
	}

	return nil
}

// handleMoonrakerPrintFinished records a finished Klipper print in history.
//
// Deliberately does NOT call processFilamentUsage, which is the shared
// deduct-and-queue path used by PrusaLink and Bambu. Moonraker printers have
// their filament accounted for already — either by Moonraker's own [spoolman]
// or by our reporter — so running it here would deduct the same print twice.
func (b *FilamentBridge) handleMoonrakerPrintFinished(
	printerID string, config PrinterConfig, client MoonrakerStatusProvider,
	filename, finalState string,
) error {
	printerName := resolvePrinterName(config)

	usedMM, source := b.moonrakerPrintUsage(printerID, client, filename)
	if usedMM <= 0 {
		// Nothing extruded, or the print was already running when we connected
		// so there is no baseline. Recording a zero-gram print would mislead.
		log.Printf("[MOONRAKER] Print ended on %s (%s) state=%s file=%s — no usage to record",
			printerID, config.Name, finalState, filename)
		return nil
	}

	spoolID, err := b.GetToolheadMapping(printerName, moonrakerToolhead)
	if err != nil {
		return fmt.Errorf("read spool mapping: %w", err)
	}

	usedGrams, err := b.moonrakerGramsForSpool(spoolID, usedMM)
	if err != nil {
		// Without a density we cannot convert honestly, and inventing one is
		// how the PrusaLink path ends up 19% out on non-PLA filament.
		return fmt.Errorf("convert %.2fmm to grams: %w", usedMM, err)
	}

	b.mutex.RLock()
	startTime := b.printStartTime[printerID]
	b.mutex.RUnlock()

	printMinutes := 0.0
	if !startTime.IsZero() {
		printMinutes = time.Since(startTime).Minutes()
	}

	status := "completed"
	if finalState == StateStopped {
		status = "cancelled"
	}

	sessionID := newSessionID()
	printID, err := b.LogPrintUsageFull(printerName, moonrakerToolhead, spoolID, usedGrams,
		filename, printMinutes, status, "", sessionID, PrinterTypeMoonraker)
	if err != nil {
		return fmt.Errorf("log print history: %w", err)
	}

	if printID > 0 {
		// usedMM is the measured quantity and usedGrams is derived from it, so
		// both are recorded. Passing 0 for the length leaves the history detail
		// view showing a blank mm column.
		_ = b.AppendFilamentUsage(printID, moonrakerToolhead, 0, spoolID, usedMM, usedGrams)
		if err := b.SnapshotAssignmentsForPrint(printID, printerID, startTime); err != nil {
			log.Printf("[MOONRAKER] Warning: could not snapshot assignments for print %d: %v", printID, err)
		}
	}

	log.Printf("[MOONRAKER] 🎉 Print %s on %s (%s): file=%s %.2fmm (%s) = %.2fg on spool %d",
		status, printerID, config.Name, filename, usedMM, source, usedGrams, spoolID)
	return nil
}

// moonrakerPrintUsage reports how much filament a finished print consumed, in
// millimetres, and where the figure came from.
//
// Moonraker's own job history is preferred. Its number is the one Spoolman was
// debited, and unlike our tracker it survives a restart of The Moment: the
// tracker's high-water mark is in-memory, so restarting rebaselines it to
// wherever the axis sits — and after a tip-shaping retraction that is below the
// previous mark, making the next print re-bill the re-prime. Measured on
// hardware, that inflated one print's recorded usage by 49mm (3.9%).
//
// Falls back to the tracker when Moonraker has no matching job, which covers
// printers without the history component and jobs that have not appeared yet.
func (b *FilamentBridge) moonrakerPrintUsage(
	printerID string, client MoonrakerStatusProvider, filename string,
) (float64, string) {
	if filename != "" {
		job, err := client.LastCompletedJob(filename)
		if err != nil {
			log.Printf("[MOONRAKER] Could not read job history for %s: %v", printerID, err)
		} else if job != nil && job.FilamentUsed > 0 {
			if job.SlicerFilament > 0 {
				// The gap is the prime line, purge and tip shaping — real
				// filament the slicer never accounts for. Logging it makes an
				// otherwise invisible per-print cost visible.
				log.Printf("[MOONRAKER] Job %s: slicer estimated %.2fmm, actually used %.2fmm (%.2fmm overhead)",
					job.JobID, job.SlicerFilament, job.FilamentUsed, job.FilamentUsed-job.SlicerFilament)
			}
			return job.FilamentUsed, "moonraker history"
		}
	}

	b.moonrakerMutex.RLock()
	startTotal, hadStart := b.moonrakerPrintStart[printerID]
	b.moonrakerMutex.RUnlock()
	if !hadStart {
		return 0, "none"
	}
	return client.Tracker().BilledTotal() - startTotal, "tracker"
}

// moonrakerGramsForSpool converts millimetres to grams using the spool's own
// filament density and diameter.
//
// The tracker deliberately works in millimetres so Spoolman can apply the real
// density. History stores grams, so the conversion has to happen somewhere —
// doing it from the spool record keeps it honest. The PrusaLink path falls back
// to a hardcoded 1.24 g/cm3, which overstates ABS+ (1.04) by 19%.
func (b *FilamentBridge) moonrakerGramsForSpool(spoolID int, usedMM float64) (float64, error) {
	if spoolID == 0 {
		return 0, fmt.Errorf("no spool assigned")
	}
	spool, err := b.spoolman.GetSpoolByID(spoolID)
	if err != nil {
		return 0, err
	}
	if spool == nil || spool.Filament == nil {
		return 0, fmt.Errorf("spool %d has no filament record", spoolID)
	}
	density, diameter := spool.Filament.Density, spool.Filament.Diameter
	if density <= 0 || diameter <= 0 {
		return 0, fmt.Errorf("spool %d: filament has density=%.3f diameter=%.3f", spoolID, density, diameter)
	}

	radius := diameter / 2
	volumeMM3 := math.Pi * radius * radius * usedMM
	// density is g/cm3; 1 cm3 = 1000 mm3.
	return (volumeMM3 / 1000.0) * density, nil
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
	b.moonrakerPrintStart = make(map[string]float64)
}
