// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// moonraker_tracker.go
// =============================================================================
// Extrusion tracker for Moonraker/Klipper printers.
//
// This is a port of the tracking algorithm in Moonraker's own [spoolman]
// component (moonraker/components/spoolman.py, v0.10.0). We port rather than
// reuse because The Moment is the sole writer to Spoolman for these printers —
// running both would race on the same spool record.
//
// Three properties of the original MUST survive the port. Each has a test.
//
//  1. Retraction-aware. Only monotonic *new highs* of the extruder axis are
//     billed. A retraction lowers the reported position but never lowers the
//     high-water mark, so re-extruding the same filament is not billed twice.
//
//  2. Immune to "G92 E0". The caller must feed this tracker
//     toolhead.position[3] — the raw kinematic axis — and never
//     gcode_move.gcode_position. G92 rewrites gcode_move's offset and leaves
//     toolhead.position alone. Feeding the wrong field silently loses
//     extrusion on every slicer E-reset, which is the easiest way to get this
//     wrong and the hardest to notice.
//
//  3. Rebaselining is not billing. On a tool change or a spool change the
//     high-water mark is reset without charging the delta to anyone. Filament
//     extruded across that boundary belongs to neither spool.
//
// Billing is continuous and delta-based, so a cancelled print needs no
// reconciliation at completion — purge, prime and wipe are already counted.
//
// Known limitation: FORCE_MOVE is invisible here. It drives the stepper
// directly, bypassing the kinematics, so it never reaches toolhead.position —
// verified on real hardware, where three 35mm FORCE_MOVE retractions left the
// reported axis unchanged to four decimal places. motion_report.live_position
// mirrors the toolhead and does not capture it either, so there is no way to
// see it through Moonraker's object model.
//
// This is harmless for its usual purpose: macros use FORCE_MOVE to seek a
// sensor or park filament, which moves material inside the toolhead without
// consuming any. It only drifts if a macro mixes methods — retracting with
// FORCE_MOVE but pushing back with G1 — because then the axis climbs without
// the matching descent. Moonraker's [spoolman] reads the same axis and behaves
// identically, so this is a shared limitation rather than a regression.
//
// This type performs no I/O and holds no clock. It accumulates pending usage
// in millimetres; flushing to Spoolman is the caller's job (see
// FlushPending/RestorePending/DropSpool). Keeping it pure is a deliberate
// departure from the Python original, which interleaves HTTP into the same
// object — it makes every rule above testable without a printer or a network.
// =============================================================================

import "sync"

// ExtrusionTracker converts a stream of extruder-axis positions into per-spool
// filament usage in millimetres. It is safe for concurrent use.
type ExtrusionTracker struct {
	mu sync.Mutex

	// highestEPos is the high-water mark of the extruder axis. Usage is billed
	// only when a reading exceeds it.
	highestEPos float64

	// lastEPos is the most recent reading, high or not. A spool change
	// rebaselines to this rather than to highestEPos so that filament already
	// retracted at the moment of the swap is not billed to the incoming spool.
	lastEPos float64

	currentExtruder string

	// spoolID is the active spool, or nil when none is assigned. Extrusion
	// with no active spool advances the high-water mark but is billed to
	// nobody — matching Moonraker, which cannot attribute it either.
	spoolID *int

	// pending accumulates unflushed usage in millimetres, keyed by spool ID.
	pending map[int]float64

	// ready reports whether a baseline has been established. Until Reset is
	// called the tracker ignores updates: without a starting position the
	// first reading would otherwise be billed in full as one enormous delta.
	ready bool
}

// NewExtrusionTracker returns a tracker with no baseline. Call Reset once the
// printer reports a starting extruder position before feeding it updates.
func NewExtrusionTracker() *ExtrusionTracker {
	return &ExtrusionTracker{
		currentExtruder: "extruder",
		pending:         make(map[int]float64),
	}
}

// Reset establishes the baseline from the printer's current extruder position.
// Call it on Klippy ready and after any reconnect: while disconnected the axis
// may have moved, and billing that gap would attribute another job's filament
// to the active spool.
func (t *ExtrusionTracker) Reset(ePos float64, extruder string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if extruder == "" {
		extruder = "extruder"
	}
	t.highestEPos = ePos
	t.lastEPos = ePos
	t.currentExtruder = extruder
	t.ready = true
}

// Ready reports whether a baseline has been established.
func (t *ExtrusionTracker) Ready() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ready
}

// BilledTotal returns the high-water mark of the extruder axis.
//
// Sampling this at the start and end of a print gives exactly the millimetres
// billed in between — the same figure sent to Spoolman. Print history uses it
// rather than print_stats.filament_used, which decrements on retraction and so
// under-reports the spool debit by the end-of-print retraction (measured at
// -3.9% on a small ABS print with a tip-shaping macro).
func (t *ExtrusionTracker) BilledTotal() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.highestEPos
}

// HandleStatusUpdate feeds one reading of the extruder axis.
//
// ePos MUST come from toolhead.position[3]. See property 2 in the file header.
//
// Returns the millimetres billed by this update, which is zero for retractions,
// tool changes, readings below the high-water mark, and updates arriving before
// Reset. The return value exists for logging and for the log-only verification
// phase; callers do not need it to drive the flush.
func (t *ExtrusionTracker) HandleStatusUpdate(ePos float64, extruder string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.ready {
		return 0
	}
	if extruder == "" {
		extruder = t.currentExtruder
	}

	t.lastEPos = ePos

	// Tool change: rebaseline, bill nothing. The delta spans two extruders and
	// belongs to neither.
	if extruder != t.currentExtruder {
		t.highestEPos = ePos
		t.currentExtruder = extruder
		return 0
	}

	// Not a new high — retraction, or re-extruding ground already billed.
	if ePos <= t.highestEPos {
		return 0
	}

	used := ePos - t.highestEPos
	t.highestEPos = ePos

	if t.spoolID == nil {
		return 0
	}
	t.pending[*t.spoolID] += used
	return used
}

// SetActiveSpool changes the spool that subsequent extrusion is billed to.
// Passing nil clears it.
//
// The rebaseline is to lastEPos, not highestEPos, matching Moonraker
// (spoolman.py set_active_spool). Any extrusion between the last reading and
// this call is billed to neither spool — deliberate, since the physical swap
// happened somewhere inside that window and splitting it would be a guess.
func (t *ExtrusionTracker) SetActiveSpool(spoolID *int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if spoolID == nil && t.spoolID == nil {
		return
	}
	if spoolID != nil && t.spoolID != nil && *spoolID == *t.spoolID {
		return
	}

	t.highestEPos = t.lastEPos

	if spoolID == nil {
		t.spoolID = nil
		return
	}
	id := *spoolID
	t.spoolID = &id
}

// ActiveSpool returns the active spool ID, or nil when none is assigned.
func (t *ExtrusionTracker) ActiveSpool() *int {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.spoolID == nil {
		return nil
	}
	id := *t.spoolID
	return &id
}

// FlushPending removes and returns all accumulated usage in millimetres. Usage
// that fails to reach Spoolman must be handed back via RestorePending so a
// Spoolman outage costs nothing but latency.
func (t *ExtrusionTracker) FlushPending() map[int]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.pending) == 0 {
		return nil
	}
	out := t.pending
	t.pending = make(map[int]float64)
	return out
}

// RestorePending adds usage back after a failed flush. It accumulates onto
// whatever arrived while the flush was in flight rather than overwriting it.
func (t *ExtrusionTracker) RestorePending(spoolID int, usedLength float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending[spoolID] += usedLength
}

// PendingFor reports the unflushed millimetres for a spool.
func (t *ExtrusionTracker) PendingFor(spoolID int) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending[spoolID]
}

// DropSpool discards pending usage for a spool that Spoolman reports as gone
// (HTTP 404), and clears it as the active spool if it was one. Without this a
// deleted spool's usage would be retried forever.
func (t *ExtrusionTracker) DropSpool(spoolID int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, spoolID)
	if t.spoolID != nil && *t.spoolID == spoolID {
		t.spoolID = nil
	}
}
