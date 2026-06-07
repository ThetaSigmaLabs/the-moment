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

// APIShapeMonitor detects when the JSON structure of PrusaLink API responses
// changes between poll cycles — for example when an unknown object is received
// that was not present before. On first call for a printer+endpoint pair the
// response is stored as the baseline. Subsequent calls return the field diff.
//
// Muting rules (all state is in-memory; resets on process restart):
//   - alertPending: true while an unacknowledged API-change alert exists for a
//     printer. Blocks new alerts until ClearAlert is called (i.e. user dismisses).
//   - muted: true after the user explicitly mutes a printer. Suppresses all
//     further alerts until UnmuteOnPrint is called (on IDLE→PRINTING transition).
type APIShapeMonitor struct {
	mu           sync.Mutex
	shapes       map[string]string // "printerID:endpoint" → sorted field-path fingerprint
	alertPending map[string]bool   // printerID → alert is live and unacknowledged
	muted        map[string]bool   // printerID → muted until next print / restart
}

// NewAPIShapeMonitor creates a ready-to-use monitor.
func NewAPIShapeMonitor() *APIShapeMonitor {
	return &APIShapeMonitor{
		shapes:       make(map[string]string),
		alertPending: make(map[string]bool),
		muted:        make(map[string]bool),
	}
}

// Check compares the JSON shape of body against the stored baseline for
// printerID+endpoint. On first call the body is stored as the baseline and
// (nil, nil, false) is returned. On subsequent calls a diff is returned when
// the field set changes.
//
// Returns (added, removed []string, changed bool).
// added/removed are slash-prefixed paths, e.g. "/printer/temp_nozzle".
func (m *APIShapeMonitor) Check(printerID, endpoint string, body []byte) (added, removed []string, changed bool) {
	if len(body) == 0 {
		return nil, nil, false
	}

	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil, false
	}

	paths := jsonFieldPaths(v, "")
	sort.Strings(paths)
	fingerprint := strings.Join(paths, "\n")

	key := printerID + ":" + endpoint

	m.mu.Lock()
	defer m.mu.Unlock()

	prev, seen := m.shapes[key]
	m.shapes[key] = fingerprint

	if !seen {
		return nil, nil, false
	}
	if prev == fingerprint {
		return nil, nil, false
	}

	prevSet := splitSet(prev)
	currSet := splitSet(fingerprint)

	for p := range currSet {
		if !prevSet[p] {
			added = append(added, p)
		}
	}
	for p := range prevSet {
		if !currSet[p] {
			removed = append(removed, p)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed, true
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

func splitSet(fingerprint string) map[string]bool {
	set := make(map[string]bool)
	for _, line := range strings.Split(fingerprint, "\n") {
		if line != "" {
			set[line] = true
		}
	}
	return set
}
