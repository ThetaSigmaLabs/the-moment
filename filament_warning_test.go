// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import "testing"

// TestHighestSeverity_Critical verifies that any critical warning makes the result "critical".
func TestHighestSeverity_Critical(t *testing.T) {
	warnings := []PrinterWarning{
		{Severity: "warning"},
		{Severity: "critical"},
	}
	got := highestSeverity(warnings)
	if got != "critical" {
		t.Errorf("highestSeverity: got %q, want %q", got, "critical")
	}
}

// TestHighestSeverity_WarningOnly verifies that a slice with only warnings returns "warning".
func TestHighestSeverity_WarningOnly(t *testing.T) {
	warnings := []PrinterWarning{
		{Severity: "warning"},
		{Severity: "warning"},
	}
	got := highestSeverity(warnings)
	if got != "warning" {
		t.Errorf("highestSeverity: got %q, want %q", got, "warning")
	}
}

// TestHighestSeverity_Empty verifies that an empty slice returns "".
func TestHighestSeverity_Empty(t *testing.T) {
	got := highestSeverity(nil)
	if got != "" {
		t.Errorf("highestSeverity(nil): got %q, want %q", got, "")
	}
	got = highestSeverity([]PrinterWarning{})
	if got != "" {
		t.Errorf("highestSeverity([]): got %q, want %q", got, "")
	}
}
