// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

import "testing"

// TestGetSpoolmanExternalURL_FallsBackToInternal verifies the helper returns the
// internal URL when the external one is empty (single-URL deployments).
func TestGetSpoolmanExternalURL_FallsBackToInternal(t *testing.T) {
	b := &FilamentBridge{
		config: &Config{
			SpoolmanURL:         "http://spoolman:8000",
			SpoolmanExternalURL: "",
		},
	}
	if got := b.GetSpoolmanExternalURL(); got != "http://spoolman:8000" {
		t.Errorf("expected fallback to internal URL, got %q", got)
	}
}

// TestGetSpoolmanExternalURL_PrefersExternal verifies the helper returns the
// external URL when both are set (Docker/k8s deploy with separate external host).
func TestGetSpoolmanExternalURL_PrefersExternal(t *testing.T) {
	b := &FilamentBridge{
		config: &Config{
			SpoolmanURL:         "http://spoolman:8000",
			SpoolmanExternalURL: "http://nas.local:7912",
		},
	}
	if got := b.GetSpoolmanExternalURL(); got != "http://nas.local:7912" {
		t.Errorf("expected external URL, got %q", got)
	}
}
