// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// moonraker_reporter.go
// =============================================================================
// Periodic flush of tracked extrusion to Spoolman.
//
// ExtrusionTracker accumulates millimetres but performs no I/O. This is the
// half that talks to Spoolman: every sync interval it drains the tracker and
// reports each spool's usage via PUT /spool/{id}/use.
//
// Reporting length rather than weight is the point. Spoolman converts mm to
// grams using the filament's own density and diameter, so nothing here has to
// guess a density — unlike the PrusaLink G-code path, which falls back to a
// hardcoded 1.24 g/cm3 and is therefore wrong by ~19% on ABS+ at 1.04.
//
// Failure handling mirrors Moonraker's, because the failure modes are the same:
//
//   - transient error  -> hand the millimetres back to the tracker so the next
//                         cycle retries them. An outage costs latency, never
//                         filament.
//   - 404 (spool gone) -> drop the pending usage and clear the active spool,
//                         otherwise a deleted spool is retried forever.
//   - repeated errors  -> log once, not every cycle, so a printer left running
//                         against a down Spoolman does not flood the log.
// =============================================================================

import (
	"errors"
	"log"
	"sync"
	"time"
)

// MoonrakerSyncInterval matches Moonraker's own [spoolman] sync_rate default.
// Keeping the cadence identical makes the log-only verification phase a
// like-for-like comparison.
const MoonrakerSyncInterval = 5 * time.Second

// spoolUsageReporter is the slice of SpoolmanClient this needs, kept narrow so
// tests can substitute a fake without standing up a Spoolman.
type spoolUsageReporter interface {
	UseSpoolLength(spoolID int, useLength float64) error
}

// MoonrakerReporter flushes one tracker to Spoolman on a fixed interval.
type MoonrakerReporter struct {
	tracker  *ExtrusionTracker
	interval time.Duration

	// resolve returns the Spoolman client to write through and whether this
	// flush must be log-only.
	//
	// It is called on every flush rather than captured once at construction.
	// Both values can change while the process runs: the Spoolman client is
	// replaced whenever the config reloads, and log-only is the safety switch
	// that stops double-billing. Latching either would mean a user who turns
	// log-only back on to stop a runaway double-write sees nothing happen until
	// they restart — the switch has to work when it is reached for.
	resolve func() (spoolUsageReporter, bool)

	// flushMu serialises Flush. The ticker and the poll loop both call it (the
	// poll loop flushes at print end), and without this they race on
	// errorLogged.
	flushMu sync.Mutex

	// errorLogged latches so a persistent outage logs once per recovery cycle
	// rather than once per flush. Guarded by flushMu.
	errorLogged bool

	printerID string

	done     chan struct{}
	closeOne sync.Once
	wg       sync.WaitGroup
}

// NewMoonrakerReporter creates a reporter with a fixed Spoolman client and
// log-only setting. Convenient for tests; production uses
// NewMoonrakerReporterFunc so both stay live.
func NewMoonrakerReporter(printerID string, tracker *ExtrusionTracker, spoolman spoolUsageReporter, logOnly bool) *MoonrakerReporter {
	return NewMoonrakerReporterFunc(printerID, tracker, func() (spoolUsageReporter, bool) {
		return spoolman, logOnly
	})
}

// NewMoonrakerReporterFunc creates a reporter that re-resolves its Spoolman
// client and log-only setting on every flush. Call Start to begin flushing.
func NewMoonrakerReporterFunc(printerID string, tracker *ExtrusionTracker, resolve func() (spoolUsageReporter, bool)) *MoonrakerReporter {
	return &MoonrakerReporter{
		tracker:   tracker,
		interval:  MoonrakerSyncInterval,
		resolve:   resolve,
		printerID: printerID,
		done:      make(chan struct{}),
	}
}

// Start begins the flush loop.
func (r *MoonrakerReporter) Start() {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.done:
				// Final flush so usage from the last few seconds is not lost on
				// a clean shutdown.
				r.Flush()
				return
			case <-ticker.C:
				r.Flush()
			}
		}
	}()
}

// Stop halts the loop and performs a final flush.
func (r *MoonrakerReporter) Stop() {
	r.closeOne.Do(func() { close(r.done) })
	r.wg.Wait()
}

// Flush drains the tracker once and reports each spool's usage. Exported so
// tests and the verification tooling can drive a cycle deterministically
// instead of waiting on the ticker.
func (r *MoonrakerReporter) Flush() {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()

	// Resolved once per flush, not per spool, so a config change cannot split a
	// single drain across two different Spoolman targets.
	spoolman, logOnly := r.resolve()

	pending := r.tracker.FlushPending()
	if len(pending) == 0 {
		return
	}

	for spoolID, useLength := range pending {
		if useLength <= 0 {
			continue
		}

		if logOnly {
			log.Printf("Moonraker %s [log-only]: would report %.3fmm to spool %d",
				r.printerID, useLength, spoolID)
			continue
		}

		err := spoolman.UseSpoolLength(spoolID, useLength)
		if err == nil {
			r.errorLogged = false
			continue
		}

		if errors.Is(err, ErrSpoolNotFound) {
			// The spool is gone. Retrying cannot succeed, so drop what we have
			// and stop billing it.
			log.Printf("Moonraker %s: spool %d no longer exists in Spoolman — dropping %.3fmm and clearing it",
				r.printerID, spoolID, useLength)
			r.tracker.DropSpool(spoolID)
			continue
		}

		// Transient: give the millimetres back so the next cycle retries them.
		r.tracker.RestorePending(spoolID, useLength)
		if !r.errorLogged {
			r.errorLogged = true
			log.Printf("Moonraker %s: failed to report %.3fmm for spool %d, will retry: %v",
				r.printerID, useLength, spoolID, err)
		}
	}
}
