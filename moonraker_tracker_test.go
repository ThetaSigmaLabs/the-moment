// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// moonraker_tracker_test.go
// =============================================================================
// Tests for the ported Moonraker extrusion tracker.
//
// The first three tests correspond one-to-one with the three properties listed
// in moonraker_tracker.go's header. If one of them fails the port is wrong in a
// way that silently mis-bills filament, so they are worth reading before
// changing anything in the tracker.
//
// Run:
//   go test ./... -v -run TestExtrusionTracker
// =============================================================================

import (
	"math"
	"sync"
	"testing"
)

const epsilon = 1e-9

func assertClose(t *testing.T, got, want float64, msg string) {
	t.Helper()
	if math.Abs(got-want) > epsilon {
		t.Errorf("%s: got %.6f, want %.6f", msg, got, want)
	}
}

func intPtr(i int) *int { return &i }

// newReadyTracker returns a tracker baselined at ePos with spool 12 active.
// Spool 12 is the designated physical test spool (CCTREE ABS+).
func newReadyTracker(ePos float64) *ExtrusionTracker {
	tr := NewExtrusionTracker()
	tr.Reset(ePos, "extruder")
	tr.SetActiveSpool(intPtr(12))
	return tr
}

// -----------------------------------------------------------------------------
// Property 1: retraction-aware
// -----------------------------------------------------------------------------

// A retraction must not reduce the total, and re-extruding the same filament
// must not be billed a second time. Only ground above the previous high counts.
func TestExtrusionTracker_RetractionIsNotBilledTwice(t *testing.T) {
	tr := newReadyTracker(0)

	tr.HandleStatusUpdate(10, "extruder") // extrude to 10
	tr.HandleStatusUpdate(8, "extruder")  // retract 2
	tr.HandleStatusUpdate(10, "extruder") // re-extrude the same 2

	assertClose(t, tr.PendingFor(12), 10, "retract-then-re-extrude must bill 10mm once")

	// Only the portion above the old high is new.
	billed := tr.HandleStatusUpdate(12, "extruder")
	assertClose(t, billed, 2, "only the new high is billed")
	assertClose(t, tr.PendingFor(12), 12, "total after advancing past the high")
}

// A retraction deeper than anything since the baseline still bills nothing, and
// leaves the high-water mark untouched.
func TestExtrusionTracker_DeepRetractionBillsNothing(t *testing.T) {
	tr := newReadyTracker(100)

	assertClose(t, tr.HandleStatusUpdate(95, "extruder"), 0, "retraction below baseline")
	assertClose(t, tr.HandleStatusUpdate(50, "extruder"), 0, "deeper retraction")
	assertClose(t, tr.PendingFor(12), 0, "no usage from retraction alone")

	// Recovering to just under the old high is still not new filament.
	assertClose(t, tr.HandleStatusUpdate(99, "extruder"), 0, "recovery below high")
	assertClose(t, tr.HandleStatusUpdate(100.5, "extruder"), 0.5, "only above the high")
}

// -----------------------------------------------------------------------------
// Property 2: immune to G92 E0
// -----------------------------------------------------------------------------

// G92 E0 rewrites gcode_move's offset but leaves toolhead.position alone. Fed
// the correct field, the tracker sees a continuous axis and bills normally.
// This test documents the contract: a slicer E-reset is invisible here.
func TestExtrusionTracker_G92ResetDoesNotLoseExtrusion(t *testing.T) {
	tr := newReadyTracker(0)

	tr.HandleStatusUpdate(500, "extruder")
	assertClose(t, tr.PendingFor(12), 500, "before the reset")

	// The slicer emits G92 E0. gcode_move.gcode_position would drop to 0 here;
	// toolhead.position[3] keeps counting. Feeding the latter, the next reading
	// is 510 and bills 10 — not 510, and not 0.
	billed := tr.HandleStatusUpdate(510, "extruder")

	assertClose(t, billed, 10, "extrusion after a G92 E0 bills the delta only")
	assertClose(t, tr.PendingFor(12), 510, "no filament lost across the reset")
}

// If a caller wires up gcode_move.gcode_position by mistake, the axis appears
// to collapse to 0 and then climb again. The tracker cannot detect this, but it
// fails safe: the drop bills nothing and the climb re-bills only above the old
// high, so usage is under-counted rather than wildly over-counted.
func TestExtrusionTracker_WrongFieldUnderCountsRatherThanOverCounts(t *testing.T) {
	tr := newReadyTracker(0)

	tr.HandleStatusUpdate(500, "extruder")
	tr.HandleStatusUpdate(0, "extruder") // the mistake: apparent reset
	tr.HandleStatusUpdate(10, "extruder")

	assertClose(t, tr.PendingFor(12), 500, "an apparent reset must never inflate the total")
}

// -----------------------------------------------------------------------------
// Property 3: rebaselining is not billing
// -----------------------------------------------------------------------------

// A tool change resets the high-water mark and bills nothing for the jump.
func TestExtrusionTracker_ToolChangeRebaselinesWithoutBilling(t *testing.T) {
	tr := newReadyTracker(0)

	tr.HandleStatusUpdate(100, "extruder")
	assertClose(t, tr.PendingFor(12), 100, "usage on the first extruder")

	// Switching to extruder1 at a much higher axis value must not bill 900mm.
	billed := tr.HandleStatusUpdate(1000, "extruder1")
	assertClose(t, billed, 0, "the tool-change delta belongs to neither extruder")
	assertClose(t, tr.PendingFor(12), 100, "total unchanged by the tool change")

	// Billing resumes from the new baseline.
	assertClose(t, tr.HandleStatusUpdate(1005, "extruder1"), 5, "billing resumes after rebaseline")
}

// A spool change rebaselines to the last reading, so neither spool is charged
// for the swap window, and the incoming spool starts from zero.
func TestExtrusionTracker_SpoolChangeDoesNotBillAcrossTheSwap(t *testing.T) {
	tr := newReadyTracker(0)

	tr.HandleStatusUpdate(200, "extruder")
	assertClose(t, tr.PendingFor(12), 200, "usage on the outgoing spool")

	tr.SetActiveSpool(intPtr(1))

	assertClose(t, tr.PendingFor(1), 0, "incoming spool starts at zero")
	assertClose(t, tr.PendingFor(12), 200, "outgoing spool total is frozen")

	assertClose(t, tr.HandleStatusUpdate(250, "extruder"), 50, "post-swap extrusion")
	assertClose(t, tr.PendingFor(1), 50, "billed to the incoming spool")
	assertClose(t, tr.PendingFor(12), 200, "outgoing spool untouched")
}

// Rebaselining uses lastEPos, not highestEPos. If a retraction precedes the
// swap, the retracted-but-not-yet-re-extruded filament must not be billed to
// the incoming spool when it is pushed back out.
func TestExtrusionTracker_SpoolChangeRebaselinesToLastNotHighest(t *testing.T) {
	tr := newReadyTracker(0)

	tr.HandleStatusUpdate(100, "extruder") // high = 100
	tr.HandleStatusUpdate(90, "extruder")  // retract; last = 90, high = 100

	tr.SetActiveSpool(intPtr(1)) // baseline becomes 90, not 100

	// Re-extruding to 100 is new filament for the incoming spool, because the
	// physical swap happened at 90.
	assertClose(t, tr.HandleStatusUpdate(100, "extruder"), 10, "billed from lastEPos")
	assertClose(t, tr.PendingFor(1), 10, "incoming spool billed from the swap point")
	assertClose(t, tr.PendingFor(12), 100, "outgoing spool frozen at its total")
}

// Setting the same spool twice must not rebaseline — otherwise a redundant
// assignment would silently discard extrusion.
func TestExtrusionTracker_RedundantSpoolSetIsNoop(t *testing.T) {
	tr := newReadyTracker(0)

	tr.HandleStatusUpdate(100, "extruder")
	tr.HandleStatusUpdate(90, "extruder") // retract: last = 90, high = 100

	tr.SetActiveSpool(intPtr(12)) // already 12 — must not move the baseline

	// If the baseline had moved to 90, this would wrongly bill 10mm.
	assertClose(t, tr.HandleStatusUpdate(100, "extruder"), 0, "redundant set must not rebaseline")
	assertClose(t, tr.PendingFor(12), 100, "total unchanged")
}

// -----------------------------------------------------------------------------
// Baseline and unassigned-spool handling
// -----------------------------------------------------------------------------

// Updates before Reset must be ignored. Without a baseline the first reading
// would be billed in full as one enormous delta.
func TestExtrusionTracker_IgnoresUpdatesBeforeReset(t *testing.T) {
	tr := NewExtrusionTracker()
	tr.SetActiveSpool(intPtr(12))

	assertClose(t, tr.HandleStatusUpdate(5000, "extruder"), 0, "no baseline yet")
	assertClose(t, tr.PendingFor(12), 0, "nothing billed before Reset")

	if tr.Ready() {
		t.Error("tracker must not report ready before Reset")
	}

	tr.Reset(5000, "extruder")
	if !tr.Ready() {
		t.Error("tracker must report ready after Reset")
	}
	assertClose(t, tr.HandleStatusUpdate(5010, "extruder"), 10, "bills from the new baseline")
}

// Reset after a reconnect must not bill the gap: while disconnected the axis
// may have moved for another job entirely.
func TestExtrusionTracker_ResetAfterReconnectDoesNotBillTheGap(t *testing.T) {
	tr := newReadyTracker(0)

	tr.HandleStatusUpdate(100, "extruder")
	assertClose(t, tr.PendingFor(12), 100, "before the disconnect")

	// Reconnect; the printer now reports a far higher axis position.
	tr.Reset(9000, "extruder")

	assertClose(t, tr.PendingFor(12), 100, "the disconnected gap is not billed")
	assertClose(t, tr.HandleStatusUpdate(9050, "extruder"), 50, "billing resumes from the new baseline")
}

// Extrusion with no active spool advances the high-water mark but bills nobody.
// Crucially, assigning a spool afterwards must not retroactively bill it.
func TestExtrusionTracker_NoActiveSpoolBillsNobody(t *testing.T) {
	tr := NewExtrusionTracker()
	tr.Reset(0, "extruder")

	assertClose(t, tr.HandleStatusUpdate(100, "extruder"), 0, "no spool assigned")

	if len(tr.FlushPending()) != 0 {
		t.Error("unassigned extrusion must not accumulate against any spool")
	}

	tr.SetActiveSpool(intPtr(12))
	assertClose(t, tr.PendingFor(12), 0, "assignment must not bill history retroactively")
}

// Clearing the active spool stops billing without discarding what was accrued.
func TestExtrusionTracker_ClearingSpoolStopsBillingButKeepsPending(t *testing.T) {
	tr := newReadyTracker(0)

	tr.HandleStatusUpdate(100, "extruder")
	tr.SetActiveSpool(nil)

	if tr.ActiveSpool() != nil {
		t.Error("active spool must be nil after clearing")
	}
	assertClose(t, tr.HandleStatusUpdate(200, "extruder"), 0, "no billing without a spool")
	assertClose(t, tr.PendingFor(12), 100, "previously accrued usage is retained")
}

// -----------------------------------------------------------------------------
// Flush reliability
// -----------------------------------------------------------------------------

// A failed flush must lose nothing, and must accumulate onto usage that arrived
// while the request was in flight.
func TestExtrusionTracker_RestoreAfterFailedFlushLosesNothing(t *testing.T) {
	tr := newReadyTracker(0)
	tr.HandleStatusUpdate(100, "extruder")

	drained := tr.FlushPending()
	assertClose(t, drained[12], 100, "flush drains the pending total")
	assertClose(t, tr.PendingFor(12), 0, "pending is empty after a flush")

	// Extrusion continues while the (failing) request is in flight.
	tr.HandleStatusUpdate(130, "extruder")

	// Spoolman was down: hand the drained amount back.
	tr.RestorePending(12, drained[12])

	assertClose(t, tr.PendingFor(12), 130, "restored usage accumulates, not overwrites")
}

// Flushing an empty tracker returns nothing rather than an empty map to send.
func TestExtrusionTracker_FlushEmptyReturnsNil(t *testing.T) {
	tr := newReadyTracker(0)
	if got := tr.FlushPending(); got != nil {
		t.Errorf("expected nil from an empty flush, got %v", got)
	}
}

// A spool deleted in Spoolman (HTTP 404) must have its pending usage dropped
// rather than retried forever, and must be cleared as the active spool.
func TestExtrusionTracker_DropSpoolClearsPendingAndActive(t *testing.T) {
	tr := newReadyTracker(0)
	tr.HandleStatusUpdate(100, "extruder")

	tr.DropSpool(12)

	assertClose(t, tr.PendingFor(12), 0, "pending usage discarded for a deleted spool")
	if tr.ActiveSpool() != nil {
		t.Error("a deleted active spool must be cleared")
	}
}

// Dropping some other spool must not disturb the active one.
func TestExtrusionTracker_DropOtherSpoolLeavesActiveAlone(t *testing.T) {
	tr := newReadyTracker(0)
	tr.HandleStatusUpdate(100, "extruder")

	tr.DropSpool(1)

	assertClose(t, tr.PendingFor(12), 100, "unrelated spool's usage is untouched")
	if got := tr.ActiveSpool(); got == nil || *got != 12 {
		t.Error("active spool must survive dropping a different spool")
	}
}

// -----------------------------------------------------------------------------
// Concurrency
// -----------------------------------------------------------------------------

// The WebSocket reader and the flush timer touch the tracker from different
// goroutines. Run with -race. Every millimetre must be accounted for exactly
// once, whether it ends up flushed or still pending.
func TestExtrusionTracker_ConcurrentUpdatesAndFlushes(t *testing.T) {
	tr := newReadyTracker(0)

	const steps = 1000
	var flushed float64
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 1; i <= steps; i++ {
			tr.HandleStatusUpdate(float64(i), "extruder")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < steps; i++ {
			for id, mm := range tr.FlushPending() {
				if id == 12 {
					mu.Lock()
					flushed += mm
					mu.Unlock()
				}
			}
		}
	}()

	wg.Wait()

	mu.Lock()
	total := flushed + tr.PendingFor(12)
	mu.Unlock()

	assertClose(t, total, float64(steps), "flushed plus pending must equal total extrusion")
}
