// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2026 maudy2u

package main

// =============================================================================
// fan_display_test.go
// =============================================================================
// Fan speed reaches the dashboard in two different units: PrusaLink reports
// RPM, Klipper a 0-1 fraction. These pin the conversion so a raw RPM reading is
// never rendered as a percentage — which is what produced the "8099%" display
// this replaces.
// =============================================================================

import "testing"

func TestPercentOfMax(t *testing.T) {
	cases := []struct {
		name       string
		value, max int
		want       int
	}{
		{"half speed", 4000, 8000, 50},
		{"full speed", 8000, 8000, 100},
		{"stopped", 0, 8000, 0},
		{"real hotend reading", 8073, 8500, 94},
		{"real print fan reading", 5025, 6000, 83},

		// Fans routinely read a little above their rated speed. Showing 104%
		// would look like the unit bug this conversion exists to fix.
		{"overspeed clamps to 100", 8400, 8000, 100},

		// Unknown maximum must yield 0 rather than divide by zero; the caller
		// treats a zero maximum as "not configured" and shows RPM instead.
		{"unknown max", 5000, 0, 0},
		{"negative max", 5000, -1, 0},
		{"negative value", -5, 8000, 0},
	}

	for _, c := range cases {
		if got := percentOfMax(c.value, c.max); got != c.want {
			t.Errorf("%s: percentOfMax(%d, %d) = %d, want %d",
				c.name, c.value, c.max, got, c.want)
		}
	}
}
