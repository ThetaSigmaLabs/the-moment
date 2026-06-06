// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// ─── Structs ──────────────────────────────────────────────────────────────────

// CostSettings holds global cost parameters — fallbacks when no per-printer
// setting overrides them.
type CostSettings struct {
	ElectricityRate  float64 `json:"electricity_rate"`  // $/kWh
	PrinterWattage   float64 `json:"printer_wattage"`   // Watts — global default
	MaintenanceRate  float64 `json:"maintenance_rate"`  // $/hour (consumables, wear)
	DepreciationRate float64 `json:"depreciation_rate"` // $/hour — global default
	MarginPercent    float64 `json:"margin_percent"`    // % markup applied to total cost
	Currency         string  `json:"currency"`          // ISO code e.g. "USD", "CAD"
}

// PrinterCostSettings holds per-printer overrides.
// Zero values mean "use the global CostSettings value".
type PrinterCostSettings struct {
	PrinterName         string  `json:"printer_name"`
	PrintWattageW       float64 `json:"print_wattage_w"`       // 0 = use global
	PreheatWattageW     float64 `json:"preheat_wattage_w"`     // watts during warmup
	PreheatTimeMin      float64 `json:"preheat_time_min"`      // minutes per print
	HighTempExtraW      float64 `json:"high_temp_extra_w"`     // extra W for ABS/ASA/PA/PC etc.
	PrinterPurchaseCost float64 `json:"printer_purchase_cost"` // $ — used with lifespan
	EstimatedLifeHrs    float64 `json:"estimated_life_hrs"`    // hours before replacement
	DepreciationPerHr   float64 `json:"depreciation_per_hr"`   // direct override (0 = derive)
}

// effectiveDepreciationPerHr returns the per-hour depreciation rate for this
// printer: direct override > purchase cost / lifespan > 0 (fall back to global).
func (p *PrinterCostSettings) effectiveDepreciationPerHr() float64 {
	if p.DepreciationPerHr > 0 {
		return p.DepreciationPerHr
	}
	if p.PrinterPurchaseCost > 0 && p.EstimatedLifeHrs > 0 {
		return p.PrinterPurchaseCost / p.EstimatedLifeHrs
	}
	return 0
}

// FilamentCostLine is one spool's contribution to a print cost — used to render
// per-spool rows in the Cost tab order-sheet.
type FilamentCostLine struct {
	ToolIndex    int     `json:"tool_index"`
	ChangeNumber int     `json:"change_number"`
	SpoolID      int     `json:"spool_id"`
	Grams        float64 `json:"grams"`
	PricePerKg   float64 `json:"price_per_kg"`
	Cost         float64 `json:"cost"`
}

// CostBreakdown is the full calculated cost for one print job.
type CostBreakdown struct {
	// Inputs (echoed back for display)
	FilamentGrams   float64 `json:"filament_grams"`
	PrintTimeMin    float64 `json:"print_time_min"`
	FilamentPriceKg float64 `json:"filament_price_per_kg"` // from Spoolman

	// Cost components
	FilamentCost     float64 `json:"filament_cost"`
	PreheatCost      float64 `json:"preheat_cost"`      // one-time warmup electricity
	ElectricityCost  float64 `json:"electricity_cost"`  // print-time electricity
	MaintenanceCost  float64 `json:"maintenance_cost"`
	DepreciationCost float64 `json:"depreciation_cost"`
	SubTotal         float64 `json:"sub_total"`    // before margin
	MarginAmount     float64 `json:"margin_amount"`
	TotalCost        float64 `json:"total_cost"`

	// Per-spool breakdown for the order-sheet Cost tab (one entry per filament segment).
	FilamentLines []FilamentCostLine `json:"filament_lines,omitempty"`

	// Diagnostic flags
	PrintWattageUsed float64 `json:"print_wattage_used"` // effective W (inc. high-temp bump)
	HighTempApplied  bool    `json:"high_temp_applied"`

	// Settings snapshot (so the UI can show what was used)
	Settings CostSettings `json:"settings"`
	Currency string       `json:"currency"`
}

// ─── High-temp material detection ────────────────────────────────────────────

// highTempMaterials lists material codes (uppercase) that typically run hotter
// than 220 °C and may warrant an enclosure heater or sustained high-bed temps.
var highTempMaterials = map[string]bool{
	"ABS": true, "ASA": true,
	"PA": true, "PA6": true, "PA12": true, "PA-CF": true, "PA-GF": true, "PA12-CF": true,
	"PC": true, "PC-ABS": true, "PC-CF": true,
	"POM": true, "PEEK": true, "PPS": true, "PEI": true, "PPSU": true,
	"NYLON": true,
}

// isHighTempMaterial returns true when the Spoolman material string indicates
// a filament that requires elevated temperatures.
func isHighTempMaterial(material string) bool {
	return highTempMaterials[strings.ToUpper(strings.TrimSpace(material))]
}

// ─── Global settings persistence ─────────────────────────────────────────────

// GetCostSettings loads global cost settings from the configuration table.
func (b *FilamentBridge) GetCostSettings() (*CostSettings, error) {
	defaults := &CostSettings{
		ElectricityRate:  0.12,
		PrinterWattage:   150,
		MaintenanceRate:  0.10,
		DepreciationRate: 0.05,
		MarginPercent:    0,
		Currency:         "USD",
	}

	rows, err := b.db.Query("SELECT key, value FROM configuration WHERE key LIKE 'cost_%'")
	if err != nil {
		return defaults, nil
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			m[k] = v
		}
	}

	if v, ok := m[ConfigKeyCostElectricityRate]; ok  { fmt.Sscanf(v, "%f", &defaults.ElectricityRate) }
	if v, ok := m[ConfigKeyCostPrinterWattage]; ok   { fmt.Sscanf(v, "%f", &defaults.PrinterWattage) }
	if v, ok := m[ConfigKeyCostMaintenanceRate]; ok  { fmt.Sscanf(v, "%f", &defaults.MaintenanceRate) }
	if v, ok := m[ConfigKeyCostDepreciationRate]; ok { fmt.Sscanf(v, "%f", &defaults.DepreciationRate) }
	if v, ok := m[ConfigKeyCostMarginPercent]; ok    { fmt.Sscanf(v, "%f", &defaults.MarginPercent) }
	if v, ok := m[ConfigKeyCostCurrency]; ok && v != "" { defaults.Currency = v }

	return defaults, nil
}

// SetCostSettings persists global cost settings.
func (b *FilamentBridge) SetCostSettings(s *CostSettings) error {
	pairs := map[string]string{
		ConfigKeyCostElectricityRate:  fmt.Sprintf("%.6f", s.ElectricityRate),
		ConfigKeyCostPrinterWattage:   fmt.Sprintf("%.2f", s.PrinterWattage),
		ConfigKeyCostMaintenanceRate:  fmt.Sprintf("%.6f", s.MaintenanceRate),
		ConfigKeyCostDepreciationRate: fmt.Sprintf("%.6f", s.DepreciationRate),
		ConfigKeyCostMarginPercent:    fmt.Sprintf("%.4f", s.MarginPercent),
		ConfigKeyCostCurrency:         s.Currency,
	}
	for k, v := range pairs {
		if _, err := b.db.Exec(
			`INSERT INTO configuration (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v,
		); err != nil {
			return fmt.Errorf("failed to save %s: %w", k, err)
		}
	}
	log.Printf("💰 Cost settings saved")
	return nil
}

// ─── Per-printer settings persistence ────────────────────────────────────────

// GetPrinterCostSettings loads per-printer overrides. Returns a zero-value
// struct (all zeros) when no row exists — callers treat 0 as "use global".
func (b *FilamentBridge) GetPrinterCostSettings(printerName string) (*PrinterCostSettings, error) {
	s := &PrinterCostSettings{PrinterName: printerName}
	err := b.db.QueryRow(`
		SELECT print_wattage_w, preheat_wattage_w, preheat_time_min,
		       high_temp_extra_w, printer_purchase_cost, estimated_life_hrs, depreciation_per_hr
		FROM printer_cost_settings WHERE printer_name = ?`, printerName,
	).Scan(
		&s.PrintWattageW, &s.PreheatWattageW, &s.PreheatTimeMin,
		&s.HighTempExtraW, &s.PrinterPurchaseCost, &s.EstimatedLifeHrs, &s.DepreciationPerHr,
	)
	if err == sql.ErrNoRows {
		return s, nil // zeros = use global
	}
	return s, err
}

// SetPrinterCostSettings upserts per-printer overrides.
func (b *FilamentBridge) SetPrinterCostSettings(s *PrinterCostSettings) error {
	_, err := b.db.Exec(`
		INSERT INTO printer_cost_settings
			(printer_name, print_wattage_w, preheat_wattage_w, preheat_time_min,
			 high_temp_extra_w, printer_purchase_cost, estimated_life_hrs, depreciation_per_hr,
			 updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(printer_name) DO UPDATE SET
			print_wattage_w      = excluded.print_wattage_w,
			preheat_wattage_w    = excluded.preheat_wattage_w,
			preheat_time_min     = excluded.preheat_time_min,
			high_temp_extra_w    = excluded.high_temp_extra_w,
			printer_purchase_cost= excluded.printer_purchase_cost,
			estimated_life_hrs   = excluded.estimated_life_hrs,
			depreciation_per_hr  = excluded.depreciation_per_hr,
			updated_at           = CURRENT_TIMESTAMP`,
		s.PrinterName, s.PrintWattageW, s.PreheatWattageW, s.PreheatTimeMin,
		s.HighTempExtraW, s.PrinterPurchaseCost, s.EstimatedLifeHrs, s.DepreciationPerHr,
	)
	if err != nil {
		return fmt.Errorf("failed to save printer cost settings for %s: %w", s.PrinterName, err)
	}
	log.Printf("💰 Printer cost settings saved for %s", s.PrinterName)
	return nil
}

// GetAllPrinterCostSettings returns saved overrides for all printers that have
// any setting stored. The list may be shorter than the full printer list.
func (b *FilamentBridge) GetAllPrinterCostSettings() ([]*PrinterCostSettings, error) {
	rows, err := b.db.Query(`
		SELECT printer_name, print_wattage_w, preheat_wattage_w, preheat_time_min,
		       high_temp_extra_w, printer_purchase_cost, estimated_life_hrs, depreciation_per_hr
		FROM printer_cost_settings ORDER BY printer_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*PrinterCostSettings
	for rows.Next() {
		s := &PrinterCostSettings{}
		if err := rows.Scan(
			&s.PrinterName, &s.PrintWattageW, &s.PreheatWattageW, &s.PreheatTimeMin,
			&s.HighTempExtraW, &s.PrinterPurchaseCost, &s.EstimatedLifeHrs, &s.DepreciationPerHr,
		); err == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

// ─── Calculation ──────────────────────────────────────────────────────────────

// assembleCostBreakdown builds a CostBreakdown from pre-computed filament cost
// and inputs, applying per-printer overrides where set.
// pc may be nil — all per-printer fields default to global settings.
// isHighTemp adds pc.HighTempExtraW to print wattage when true.
func assembleCostBreakdown(
	settings *CostSettings, pc *PrinterCostSettings,
	filamentGrams, printTimeMin, filamentCost, filamentPriceKg float64,
	isHighTemp bool,
) *CostBreakdown {
	hours := printTimeMin / 60.0

	// Effective print wattage
	printW := settings.PrinterWattage
	if pc != nil && pc.PrintWattageW > 0 {
		printW = pc.PrintWattageW
	}
	if isHighTemp && pc != nil && pc.HighTempExtraW > 0 {
		printW += pc.HighTempExtraW
	}

	// Preheat: one-time electricity cost per print (not per hour)
	var preheatCost float64
	if pc != nil && pc.PreheatWattageW > 0 && pc.PreheatTimeMin > 0 {
		preheatCost = (pc.PreheatWattageW / 1000.0) * (pc.PreheatTimeMin / 60.0) * settings.ElectricityRate
	}

	electricityCost := (printW / 1000.0) * hours * settings.ElectricityRate
	maintenanceCost := hours * settings.MaintenanceRate

	// Depreciation: per-printer rate overrides global
	depreciationRate := settings.DepreciationRate
	if pc != nil && pc.effectiveDepreciationPerHr() > 0 {
		depreciationRate = pc.effectiveDepreciationPerHr()
	}
	depreciationCost := hours * depreciationRate

	subTotal  := filamentCost + preheatCost + electricityCost + maintenanceCost + depreciationCost
	marginAmt := subTotal * (settings.MarginPercent / 100.0)
	totalCost := subTotal + marginAmt

	round4 := func(v float64) float64 { return math.Round(v*10000) / 10000 }

	return &CostBreakdown{
		FilamentGrams:    filamentGrams,
		PrintTimeMin:     printTimeMin,
		FilamentPriceKg:  filamentPriceKg,
		FilamentCost:     round4(filamentCost),
		PreheatCost:      round4(preheatCost),
		ElectricityCost:  round4(electricityCost),
		MaintenanceCost:  round4(maintenanceCost),
		DepreciationCost: round4(depreciationCost),
		SubTotal:         round4(subTotal),
		MarginAmount:     round4(marginAmt),
		TotalCost:        round4(totalCost),
		PrintWattageUsed: printW,
		HighTempApplied:  isHighTemp && pc != nil && pc.HighTempExtraW > 0,
		Settings:         *settings,
		Currency:         settings.Currency,
	}
}

// CalculatePrintCost computes cost for a single-spool print using global settings.
// spoolID=0 means no filament price lookup.
func (b *FilamentBridge) CalculatePrintCost(filamentGrams float64, printTimeMin float64, spoolID int) (*CostBreakdown, error) {
	settings, err := b.GetCostSettings()
	if err != nil {
		return nil, fmt.Errorf("could not load cost settings: %w", err)
	}

	var pricePerKg float64
	if spoolID > 0 {
		spools, err := b.spoolman.GetAllSpools()
		if err == nil {
			for _, s := range spools {
				if s.ID == spoolID {
					pricePerKg = s.PricePerKg()
					break
				}
			}
		}
	}

	filamentCost := (filamentGrams / 1000.0) * pricePerKg
	bd := assembleCostBreakdown(settings, nil, filamentGrams, printTimeMin, filamentCost, pricePerKg, false)

	log.Printf("💰 Cost calc: %.2fg filament + %.1fmin = %s %.4f (margin %.0f%%)",
		filamentGrams, printTimeMin, settings.Currency, bd.TotalCost, settings.MarginPercent)
	return bd, nil
}

// CalculatePrintCostForPrinter computes cost using per-printer settings and
// detects high-temp filament from the spool's Spoolman material field.
func (b *FilamentBridge) CalculatePrintCostForPrinter(filamentGrams, printTimeMin float64, spoolID int, printerName string) (*CostBreakdown, error) {
	settings, err := b.GetCostSettings()
	if err != nil {
		return nil, fmt.Errorf("could not load cost settings: %w", err)
	}

	pc, _ := b.GetPrinterCostSettings(printerName) // nil on error = use global

	var pricePerKg float64
	var isHighTemp bool
	if spoolID > 0 {
		spools, err := b.spoolman.GetAllSpools()
		if err == nil {
			for _, s := range spools {
				if s.ID == spoolID {
					pricePerKg = s.PricePerKg()
					if isHighTempMaterial(s.Material) {
						isHighTemp = true
					}
					break
				}
			}
		}
	}

	filamentCost := (filamentGrams / 1000.0) * pricePerKg
	bd := assembleCostBreakdown(settings, pc, filamentGrams, printTimeMin, filamentCost, pricePerKg, isHighTemp)
	bd.FilamentLines = []FilamentCostLine{{
		ToolIndex:  0,
		SpoolID:    spoolID,
		Grams:      filamentGrams,
		PricePerKg: pricePerKg,
		Cost:       math.Round(filamentCost*10000) / 10000,
	}}

	log.Printf("💰 Cost calc (%s): %.2fg + %.0fmin = %s %.4f%s",
		printerName, filamentGrams, printTimeMin, settings.Currency, bd.TotalCost,
		map[bool]string{true: " [high-temp]", false: ""}[isHighTemp])
	return bd, nil
}

// CalculatePrintCostMultiSpool computes cost for multi-spool prints using global
// settings only. Use CalculatePrintCostMultiSpoolForPrinter when a printer name
// is available.
func (b *FilamentBridge) CalculatePrintCostMultiSpool(filament []OctoPrintPayloadFilament, printTimeMin float64) (*CostBreakdown, error) {
	return b.CalculatePrintCostMultiSpoolForPrinter(filament, printTimeMin, "")
}

// CalculatePrintCostMultiSpoolForPrinter computes cost for multi-spool prints,
// applying per-printer overrides and detecting high-temp from any spool material.
// Each filament entry is priced against its own spool — one Spoolman call total.
func (b *FilamentBridge) CalculatePrintCostMultiSpoolForPrinter(filament []OctoPrintPayloadFilament, printTimeMin float64, printerName string) (*CostBreakdown, error) {
	settings, err := b.GetCostSettings()
	if err != nil {
		return nil, fmt.Errorf("could not load cost settings: %w", err)
	}

	var pc *PrinterCostSettings
	if printerName != "" {
		pc, _ = b.GetPrinterCostSettings(printerName)
	}

	spoolPrices    := make(map[int]float64)
	spoolMaterials := make(map[int]string)
	if len(filament) > 0 {
		if spools, err := b.spoolman.GetAllSpools(); err == nil {
			for _, s := range spools {
				spoolPrices[s.ID] = s.PricePerKg()
				spoolMaterials[s.ID] = s.Material
			}
		}
	}

	var totalGrams, filamentCost float64
	var isHighTemp bool
	var lines []FilamentCostLine
	r4 := func(v float64) float64 { return math.Round(v*10000) / 10000 }
	for _, f := range filament {
		totalGrams += f.FilamentUsedG
		lineCost := (f.FilamentUsedG / 1000.0) * spoolPrices[f.SpoolID]
		filamentCost += lineCost
		if isHighTempMaterial(spoolMaterials[f.SpoolID]) {
			isHighTemp = true
		}
		lines = append(lines, FilamentCostLine{
			ToolIndex:    f.ToolIndex,
			ChangeNumber: f.ChangeNumber,
			SpoolID:      f.SpoolID,
			Grams:        f.FilamentUsedG,
			PricePerKg:   spoolPrices[f.SpoolID],
			Cost:         r4(lineCost),
		})
	}

	var effectivePricePerKg float64
	if totalGrams > 0 && filamentCost > 0 {
		effectivePricePerKg = filamentCost / (totalGrams / 1000.0)
	}
	bd := assembleCostBreakdown(settings, pc, totalGrams, printTimeMin, filamentCost, effectivePricePerKg, isHighTemp)
	bd.FilamentLines = lines

	htLabel := ""
	if isHighTemp { htLabel = " [high-temp]" }
	log.Printf("💰 Cost calc (multi-spool%s): %.2fg filament + %.1fmin = %s %.4f (margin %.0f%%)",
		htLabel, totalGrams, printTimeMin, settings.Currency, bd.TotalCost, settings.MarginPercent)
	return bd, nil
}

// SavePrintCost persists a cost record linked to a print_history row (upsert).
func (b *FilamentBridge) SavePrintCost(printHistoryID int, bd *CostBreakdown) error {
	_, err := b.db.Exec(`
		INSERT INTO print_costs
			(print_history_id, filament_cost, electricity_cost, maintenance_cost, total_cost, currency, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(print_history_id) DO UPDATE SET
			filament_cost    = excluded.filament_cost,
			electricity_cost = excluded.electricity_cost,
			maintenance_cost = excluded.maintenance_cost,
			total_cost       = excluded.total_cost,
			currency         = excluded.currency`,
		printHistoryID,
		bd.FilamentCost,
		bd.ElectricityCost,
		bd.MaintenanceCost+bd.DepreciationCost+bd.PreheatCost,
		bd.TotalCost,
		bd.Currency,
		time.Now(),
	)
	return err
}
