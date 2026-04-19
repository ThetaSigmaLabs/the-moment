// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

package main

import (
	"fmt"
	"log"
	"math"
	"time"
)

// ─── Structs ──────────────────────────────────────────────────────────────────

// CostSettings holds all user-configurable cost parameters.
// Stored in the config table; loaded on every cost calculation.
type CostSettings struct {
	ElectricityRate  float64 `json:"electricity_rate"`  // $/kWh
	PrinterWattage   float64 `json:"printer_wattage"`   // Watts (whole printer draw)
	MaintenanceRate  float64 `json:"maintenance_rate"`  // $/hour (consumables, wear)
	DepreciationRate float64 `json:"depreciation_rate"` // $/hour (printer purchase spread over life)
	MarginPercent    float64 `json:"margin_percent"`    // % markup applied to total cost
	Currency         string  `json:"currency"`          // ISO code e.g. "USD", "CAD"
}

// CostBreakdown is the calculated result for one print job.
type CostBreakdown struct {
	// Inputs (echoed back for the UI)
	FilamentGrams   float64 `json:"filament_grams"`
	PrintTimeMin    float64 `json:"print_time_min"`
	FilamentPriceKg float64 `json:"filament_price_per_kg"` // from Spoolman

	// Cost components
	FilamentCost    float64 `json:"filament_cost"`
	ElectricityCost float64 `json:"electricity_cost"`
	MaintenanceCost float64 `json:"maintenance_cost"`
	DepreciationCost float64 `json:"depreciation_cost"`
	SubTotal        float64 `json:"sub_total"`   // before margin
	MarginAmount    float64 `json:"margin_amount"`
	TotalCost       float64 `json:"total_cost"`  // what you charge

	// Settings snapshot (so the UI can display what was used)
	Settings CostSettings `json:"settings"`
	Currency string       `json:"currency"`
}

// ─── Settings persistence ─────────────────────────────────────────────────────

// GetCostSettings loads cost settings from the config table, using safe defaults
// when keys are absent (new install) or unparseable.
func (b *FilamentBridge) GetCostSettings() (*CostSettings, error) {
	defaults := &CostSettings{
		ElectricityRate:  0.12,
		PrinterWattage:   150,
		MaintenanceRate:  0.10,
		DepreciationRate: 0.05,
		MarginPercent:    0,
		Currency:         "USD",
	}

	rows, err := b.db.Query("SELECT key, value FROM config WHERE key LIKE 'cost_%'")
	if err != nil {
		return defaults, nil // silently use defaults on DB error
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

// SetCostSettings persists cost settings to the config table (upsert).
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
			`INSERT INTO config (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v,
		); err != nil {
			return fmt.Errorf("failed to save %s: %w", k, err)
		}
	}
	log.Printf("💰 Cost settings saved")
	return nil
}

// ─── Calculation ──────────────────────────────────────────────────────────────

// CalculatePrintCost computes a full cost breakdown for a virtual print.
// spoolID is used to look up filament price from Spoolman (0 = no filament cost).
// printTimeMin is the estimated print duration in minutes.
func (b *FilamentBridge) CalculatePrintCost(filamentGrams float64, printTimeMin float64, spoolID int) (*CostBreakdown, error) {
	settings, err := b.GetCostSettings()
	if err != nil {
		return nil, fmt.Errorf("could not load cost settings: %w", err)
	}

	// Filament price from Spoolman
	var pricePerKg float64
	if spoolID > 0 {
		spools, err := b.spoolman.GetAllSpools()
		if err == nil {
			for _, s := range spools {
				if s.ID == spoolID && s.Price != nil {
					pricePerKg = *s.Price
					break
				}
			}
		}
	}

	hours := printTimeMin / 60.0

	filamentCost    := (filamentGrams / 1000.0) * pricePerKg
	electricityCost := (settings.PrinterWattage / 1000.0) * hours * settings.ElectricityRate
	maintenanceCost := hours * settings.MaintenanceRate
	depreciationCost:= hours * settings.DepreciationRate

	subTotal    := filamentCost + electricityCost + maintenanceCost + depreciationCost
	marginAmt   := subTotal * (settings.MarginPercent / 100.0)
	totalCost   := subTotal + marginAmt

	// Round to 4 decimal places to avoid float noise in UI
	round4 := func(v float64) float64 { return math.Round(v*10000) / 10000 }

	bd := &CostBreakdown{
		FilamentGrams:    filamentGrams,
		PrintTimeMin:     printTimeMin,
		FilamentPriceKg:  pricePerKg,
		FilamentCost:     round4(filamentCost),
		ElectricityCost:  round4(electricityCost),
		MaintenanceCost:  round4(maintenanceCost),
		DepreciationCost: round4(depreciationCost),
		SubTotal:         round4(subTotal),
		MarginAmount:     round4(marginAmt),
		TotalCost:        round4(totalCost),
		Settings:         *settings,
		Currency:         settings.Currency,
	}

	log.Printf("💰 Cost calc: %.2fg filament + %.1fmin = %s %.4f (margin %.0f%%)",
		filamentGrams, printTimeMin, settings.Currency, totalCost, settings.MarginPercent)
	return bd, nil
}

// SavePrintCost persists a cost record linked to a print_history row.
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
		bd.MaintenanceCost+bd.DepreciationCost, // stored in maintenance_cost column
		bd.TotalCost,
		bd.Currency,
		time.Now(),
	)
	return err
}
