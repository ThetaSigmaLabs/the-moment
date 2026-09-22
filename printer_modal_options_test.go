// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

// =============================================================================
// printer_modal_options_test.go
// =============================================================================
// Guards the printer Add/Edit modals against a silent data-loss bug.
//
// Assigning a value to a <select> that has no matching <option> leaves the
// select blank; the form submit then falls back to a default and the record is
// rewritten. detectPrinterModel can return CORE One, CORE One L, XL, MK4 and
// MK3.5 — every one of those must exist as an option value in both modals, or
// opening and saving the edit dialog changes the printer's model.
//
// The option VALUE must match the Go constant byte for byte. A cosmetic edit to
// the markup ("Core One", "MK3.5+") reintroduces the bug with no visible sign.
// setSelectValue in static/js/printers.js is the runtime backstop; this test is
// the CI gate that keeps the markup honest.

import (
	"os"
	"strings"
	"testing"
)

// modelsDetectable lists every model string detectPrinterModel can return,
// excluding ModelUnknown (deliberately not an option — setSelectValue adds it
// on demand so an undetected printer still round-trips).
var modelsDetectable = []string{
	ModelCoreOne,
	ModelCoreOneL,
	ModelXL,
	ModelMK4,
	ModelMK35,
	ModelMiniPlus,
}

// printerTypeSelects are the id attributes of the two printer-type selects.
var printerTypeSelects = []string{"printerType", "editPrinterType"}

func TestPrinterModalsListEveryDetectableModel(t *testing.T) {
	markup := readModalsTemplate(t)

	// Both modals carry their own copy of the model list, so count the
	// occurrences rather than merely finding one.
	for _, model := range modelsDetectable {
		option := `<option value="` + model + `">`
		if got := strings.Count(markup, option); got != 2 {
			t.Errorf("model %q: found %d option(s) with this exact value in templates/modals.html, want 2 (Add Printer and Edit Printer)\n"+
				"detectPrinterModel can return this string; a printer with it blanks the select and is saved as a different model",
				model, got)
		}
	}
}

func TestPrinterModalsOfferOtherFallback(t *testing.T) {
	markup := readModalsTemplate(t)

	// "Other" is what the submit path falls back to, so it must be selectable.
	if got := strings.Count(markup, `<option value="Other"`); got != 2 {
		t.Errorf(`found %d <option value="Other"> in templates/modals.html, want 2`, got)
	}
}

func TestPrinterTypeSelectsExist(t *testing.T) {
	markup := readModalsTemplate(t)

	// setSelectValue is keyed to these ids; a rename in the markup would make
	// it a silent no-op and reopen the PR #7 bug.
	for _, id := range printerTypeSelects {
		if !strings.Contains(markup, `id="`+id+`"`) {
			t.Errorf("select id=%q missing from templates/modals.html; static/js/printers.js looks it up by this id", id)
		}
	}
}

func readModalsTemplate(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("templates/modals.html")
	if err != nil {
		t.Fatalf("read templates/modals.html: %v", err)
	}
	return string(b)
}
