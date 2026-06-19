// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// APIShapeMonitor detects fields in PrusaLink API responses that the
// application does not know about. It diffs each response body against a
// per-endpoint schema (the set of field paths the structs declare, plus
// state- and lifecycle-dependent keys the API may or may not emit) and
// alerts only on paths outside that schema.
//
// Diff is against the schema, not against the first body seen. The first
// body is unreliable as a baseline: it may be missing state-dependent
// fields (e.g. time_remaining absent on FINISHED), which would lock in a
// partial schema and trigger a false positive the first time a print starts.
//
// Muting rules (in-memory; reset on process restart):
//   - alertPending: true while an unacknowledged alert exists. Blocks new
//     alerts until ClearAlert is called (user dismisses).
//   - muted: true after user mutes. Suppresses alerts until UnmuteOnPrint
//     (on IDLE→PRINTING).
type APIShapeMonitor struct {
	mu           sync.Mutex
	schema       map[string]map[string]bool // endpoint → known field paths
	alertPending map[string]bool            // printerID → alert is live and unacknowledged
	muted        map[string]bool            // printerID → muted until next print / restart
}

// NewAPIShapeMonitor creates a ready-to-use monitor.
func NewAPIShapeMonitor() *APIShapeMonitor {
	return &APIShapeMonitor{
		schema: map[string]map[string]bool{
			"status": statusSchema(),
			"job":    jobSchema(),
		},
		alertPending: make(map[string]bool),
		muted:        make(map[string]bool),
	}
}

// statusSchema is the allow-list of field paths /api/v1/status may return.
// Mirrors PrusaLinkStatus (prusalink.go) and adds state-/lifecycle-dependent
// keys the API may emit but the struct does not decode:
//   - storage: optional across firmware versions
//   - job: present while printing, absent when idle/finished
//   - transfer: present only during file uploads
//   - printer.axis_x / axis_y: present when idle, absent while printing (Core One L)
func statusSchema() map[string]bool {
	return map[string]bool{
		"/job":                        true,
		"/job/id":                     true,
		"/job/progress":               true,
		"/job/time_remaining":         true,
		"/job/time_printing":          true,
		"/storage":                    true,
		"/storage/path":               true,
		"/storage/name":               true,
		"/storage/read_only":          true,
		"/printer":                    true,
		"/printer/state":              true,
		"/printer/temp_nozzle":        true,
		"/printer/target_nozzle":      true,
		"/printer/temp_bed":           true,
		"/printer/target_bed":         true,
		"/printer/axis_x":             true,
		"/printer/axis_y":             true,
		"/printer/axis_z":             true,
		"/printer/flow":               true,
		"/printer/speed":              true,
		"/printer/fan_hotend":         true,
		"/printer/fan_print":          true,
		"/transfer":                   true,
		"/transfer/id":                true,
		"/transfer/progress":          true,
		"/transfer/time_transferring": true,
		"/transfer/transferred":       true,
	}
}

// jobSchema is the allow-list of field paths /api/v1/job may return.
// Mirrors PrusaLinkJob (prusalink.go). All fields are state- or
// file-dependent and may legitimately be absent; the list is the
// allow-list, not a required set.
func jobSchema() map[string]bool {
	return map[string]bool{
		"/id":                   true,
		"/state":                true,
		"/progress":             true,
		"/time_remaining":       true,
		"/time_printing":        true,
		"/file":                 true,
		"/file/name":            true,
		"/file/display_name":    true,
		"/file/path":            true,
		"/file/size":            true,
		"/file/m_timestamp":     true,
		"/file/refs":            true,
		"/file/refs/download":   true,
		"/file/refs/icon":       true,
		"/file/refs/thumbnail":  true,
		"/filament":             true,
		"/filament/toolhead_id": true,
		"/filament/length":      true,
		"/filament/weight":      true,
	}
}

// Check returns the field paths in body that are not in the schema for
// endpoint. removed is always nil — known fields may legitimately
// disappear with state, so their absence is not an alert-worthy event.
// Returns (added, nil, false) for empty body, unparseable JSON, or an
// unknown endpoint.
func (m *APIShapeMonitor) Check(endpoint string, body []byte) (added, removed []string, changed bool) {
	if len(body) == 0 {
		return nil, nil, false
	}
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil, false
	}
	known, ok := m.schema[endpoint]
	if !ok {
		return nil, nil, false
	}
	for _, p := range jsonFieldPaths(v, "") {
		if !known[p] {
			added = append(added, p)
		}
	}
	sort.Strings(added)
	return added, nil, len(added) > 0
}

// ShouldAlert returns true if an API-change alert should be fired for this
// printer — i.e. the printer is not muted and has no pending alert.
func (m *APIShapeMonitor) ShouldAlert(printerID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.muted[printerID] && !m.alertPending[printerID]
}

// SetAlertPending marks that a live alert exists for this printer.
// Called after addPrintError fires so we don't pile on additional alerts.
func (m *APIShapeMonitor) SetAlertPending(printerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alertPending[printerID] = true
}

// ClearAlert clears the pending-alert flag for a printer so the next detected
// change can fire a new notification. Called when the user dismisses the alert.
func (m *APIShapeMonitor) ClearAlert(printerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alertPending[printerID] = false
}

// Mute suppresses all further API-change alerts for a printer until the next
// print starts (UnmuteOnPrint) or the process restarts.
func (m *APIShapeMonitor) Mute(printerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.muted[printerID] = true
	m.alertPending[printerID] = false
}

// UnmuteOnPrint clears the mute flag when a new print begins (IDLE→PRINTING),
// allowing API-change alerts to fire again if the shape has shifted.
func (m *APIShapeMonitor) UnmuteOnPrint(printerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.muted[printerID] = false
}

// FormatDiff produces a human-readable summary of a field diff.
func FormatDiff(endpoint string, added, removed []string) string {
	var parts []string
	if len(added) > 0 {
		parts = append(parts, fmt.Sprintf("new fields in %s: %s", endpoint, strings.Join(added, ", ")))
	}
	if len(removed) > 0 {
		parts = append(parts, fmt.Sprintf("removed fields from %s: %s", endpoint, strings.Join(removed, ", ")))
	}
	return strings.Join(parts, "; ")
}

// jsonFieldPaths recursively walks a decoded JSON value and returns all
// field paths in slash notation. Only object keys are included — array
// elements are walked but not indexed (arrays signal presence, not count).
func jsonFieldPaths(v interface{}, prefix string) []string {
	switch val := v.(type) {
	case map[string]interface{}:
		var paths []string
		for k, child := range val {
			path := prefix + "/" + k
			paths = append(paths, path)
			paths = append(paths, jsonFieldPaths(child, path)...)
		}
		return paths
	case []interface{}:
		var paths []string
		for _, elem := range val {
			paths = append(paths, jsonFieldPaths(elem, prefix)...)
		}
		return paths
	default:
		return nil
	}
}
