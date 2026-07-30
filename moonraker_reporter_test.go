// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// moonraker_reporter_test.go
// =============================================================================
// Tests for the Spoolman flush loop.
//
// The reliability rules here are the ones that decide whether a Spoolman
// outage costs latency or costs filament, so each failure mode gets a test.
//
// Run:
//   go test ./... -v -run TestMoonrakerReporter
// =============================================================================

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeSpoolReporter records calls and can be told to fail.
type fakeSpoolReporter struct {
	mu    sync.Mutex
	calls []struct {
		spoolID int
		length  float64
	}
	err error
}

func (f *fakeSpoolReporter) UseSpoolLength(spoolID int, useLength float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct {
		spoolID int
		length  float64
	}{spoolID, useLength})
	return f.err
}

func (f *fakeSpoolReporter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSpoolReporter) totalFor(spoolID int) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var total float64
	for _, c := range f.calls {
		if c.spoolID == spoolID {
			total += c.length
		}
	}
	return total
}

// trackerWithUsage returns a tracker holding mm of pending usage on spool 12.
func trackerWithUsage(mm float64) *ExtrusionTracker {
	tr := NewExtrusionTracker()
	tr.Reset(0, "extruder")
	tr.SetActiveSpool(intPtr(12))
	tr.HandleStatusUpdate(mm, "extruder")
	return tr
}

// -----------------------------------------------------------------------------
// Happy path
// -----------------------------------------------------------------------------

func TestMoonrakerReporterFlushesPendingUsage(t *testing.T) {
	tr := trackerWithUsage(250)
	sp := &fakeSpoolReporter{}

	NewMoonrakerReporter("voron", tr, sp, false).Flush()

	if got := sp.totalFor(12); got != 250 {
		t.Errorf("reported %.3fmm, want 250", got)
	}
	if got := tr.PendingFor(12); got != 0 {
		t.Errorf("pending after successful flush = %.3f, want 0", got)
	}
}

// Nothing pending must mean no HTTP call at all — this runs every 5s per
// printer, and an idle printer should be silent.
func TestMoonrakerReporterNoPendingMakesNoCall(t *testing.T) {
	tr := NewExtrusionTracker()
	tr.Reset(0, "extruder")
	sp := &fakeSpoolReporter{}

	NewMoonrakerReporter("voron", tr, sp, false).Flush()

	if sp.callCount() != 0 {
		t.Errorf("made %d call(s) with nothing pending, want 0", sp.callCount())
	}
}

// -----------------------------------------------------------------------------
// Failure handling
// -----------------------------------------------------------------------------

// A transient failure must return the millimetres to the tracker so the next
// cycle retries them. Losing them here would silently under-bill.
func TestMoonrakerReporterRestoresUsageOnTransientFailure(t *testing.T) {
	tr := trackerWithUsage(100)
	sp := &fakeSpoolReporter{err: errors.New("connection refused")}

	r := NewMoonrakerReporter("voron", tr, sp, false)
	r.Flush()

	if got := tr.PendingFor(12); got != 100 {
		t.Fatalf("pending after failed flush = %.3f, want 100 restored", got)
	}

	// Spoolman recovers; the retry sends the full amount.
	sp.err = nil
	r.Flush()

	if got := sp.totalFor(12); got != 200 {
		// 100 from the failed attempt + 100 from the retry = 2 calls of 100.
		t.Errorf("total reported across both attempts = %.3f, want 200", got)
	}
	if got := tr.PendingFor(12); got != 0 {
		t.Errorf("pending after recovery = %.3f, want 0", got)
	}
}

// Usage accruing during a failed flush must add to the restored amount, not
// replace it.
func TestMoonrakerReporterRestoreAccumulatesWithNewUsage(t *testing.T) {
	tr := trackerWithUsage(100)
	sp := &fakeSpoolReporter{err: errors.New("spoolman down")}

	r := NewMoonrakerReporter("voron", tr, sp, false)
	r.Flush()

	// More extrusion after the failure.
	tr.HandleStatusUpdate(130, "extruder")

	if got := tr.PendingFor(12); got != 130 {
		t.Errorf("pending = %.3f, want 130 (100 restored + 30 new)", got)
	}
}

// A 404 means the spool was deleted. Retrying can never succeed, so the usage
// must be dropped and the spool cleared — otherwise it is retried forever.
func TestMoonrakerReporterDropsDeletedSpool(t *testing.T) {
	tr := trackerWithUsage(100)
	sp := &fakeSpoolReporter{err: fmt.Errorf("spool 12: %w", ErrSpoolNotFound)}

	NewMoonrakerReporter("voron", tr, sp, false).Flush()

	if got := tr.PendingFor(12); got != 0 {
		t.Errorf("pending after 404 = %.3f, want 0 (dropped)", got)
	}
	if tr.ActiveSpool() != nil {
		t.Error("active spool must be cleared after a 404")
	}
}

// -----------------------------------------------------------------------------
// Log-only mode (the verification gate)
// -----------------------------------------------------------------------------

// Log-only runs the whole pipeline but must never write. This is what allows
// the ported tracker to run alongside Moonraker's still-enabled [spoolman] --
// and alongside another The Moment instance -- without any of them racing on
// the same spool.
func TestMoonrakerReporterLogOnlyNeverWrites(t *testing.T) {
	tr := trackerWithUsage(500)
	sp := &fakeSpoolReporter{}

	NewMoonrakerReporter("voron", tr, sp, true).Flush()

	if sp.callCount() != 0 {
		t.Errorf("log-only made %d write(s), want 0", sp.callCount())
	}
	// The tracker is still drained, so log-only reflects real cadence rather
	// than accumulating an ever-growing total.
	if got := tr.PendingFor(12); got != 0 {
		t.Errorf("pending after log-only flush = %.3f, want 0 (drained)", got)
	}
}

// -----------------------------------------------------------------------------
// SpoolmanClient.UseSpoolLength wire format
// -----------------------------------------------------------------------------

// The request must be PUT /api/v1/spool/{id}/use carrying use_length in mm.
// Spoolman applies the delta server-side, which is what avoids the lost-update
// race that UpdateSpoolUsage's read-modify-write suffers from.
func TestSpoolmanUseSpoolLengthWireFormat(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]float64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &SpoolmanClient{baseURL: srv.URL, httpClient: srv.Client()}

	if err := c.UseSpoolLength(12, 123.456); err != nil {
		t.Fatalf("UseSpoolLength: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", gotMethod)
	}
	if gotPath != "/api/v1/spool/12/use" {
		t.Errorf("path = %s, want /api/v1/spool/12/use", gotPath)
	}
	if gotBody["use_length"] != 123.456 {
		t.Errorf("use_length = %v, want 123.456", gotBody["use_length"])
	}
}

// A deleted spool must surface as ErrSpoolNotFound so the reporter can tell it
// apart from a transient failure — the two need opposite handling.
func TestSpoolmanUseSpoolLengthReturnsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"no such spool"}`))
	}))
	defer srv.Close()

	c := &SpoolmanClient{baseURL: srv.URL, httpClient: srv.Client()}

	err := c.UseSpoolLength(999, 10)
	if !errors.Is(err, ErrSpoolNotFound) {
		t.Errorf("err = %v, want ErrSpoolNotFound", err)
	}
}
