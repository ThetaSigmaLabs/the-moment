# Understanding Print Cost Tracking

This page explains how The Moment calculates the cost of each print, what each setting does,
and how per-printer overrides interact with global defaults.

<!-- screenshot needed: Settings → Cost Settings page (global + per-printer tabs) -->
<!-- screenshot needed: History entry expanded with cost breakdown detail -->

_Source: `cost.go`_

---

## The short version

- Every print produces a **cost breakdown** with five components: filament, preheat electricity,
  print-time electricity, maintenance, and depreciation, plus an optional margin
- Global settings are fallbacks — **per-printer overrides** take precedence when set (non-zero)
- Filament price comes from the spool record in Spoolman (spool-level price > filament-level price)
- **High-temp materials** (ABS, ASA, PA, PC, PEEK, etc.) optionally add extra wattage during the print
- Preheat cost is charged **once per print**, not per hour — it covers bed and hotend warmup

---

## How cost is calculated

Given filament grams used and print time in minutes, the calculation proceeds as follows.

```
hours = print_time_min / 60

filament_cost     = (grams / 1000) × price_per_kg
preheat_cost      = (preheat_wattage_w / 1000) × (preheat_time_min / 60) × electricity_rate
electricity_cost  = (effective_wattage_w / 1000) × hours × electricity_rate
maintenance_cost  = hours × maintenance_rate
depreciation_cost = hours × effective_depreciation_rate

subtotal          = filament_cost + preheat_cost + electricity_cost
                    + maintenance_cost + depreciation_cost
margin_amount     = subtotal × (margin_percent / 100)
total_cost        = subtotal + margin_amount
```

`effective_wattage_w` is `print_wattage_w` plus `high_temp_extra_w` when the filament material
is in the high-temp list and `high_temp_extra_w > 0`.

`effective_depreciation_rate` resolves in this order:
1. Per-printer `depreciation_per_hr` (if non-zero)
2. Derived: `printer_purchase_cost / estimated_life_hrs` (if both non-zero)
3. Global `depreciation_rate`

_Source: `cost.go` → `assembleCostBreakdown`, `effectiveDepreciationPerHr`_

---

## Global settings

Configured under **Settings → Cost** in the dashboard, or via `POST /api/cost-settings`.

| Setting | DB key | Default | Description |
|---|---|---|---|
| Electricity rate | `cost_electricity_rate` | `0.12` $/kWh | Cost of electricity per kilowatt-hour |
| Printer wattage | `cost_printer_wattage` | `150` W | Default draw during printing |
| Maintenance rate | `cost_maintenance_rate` | `0.10` $/hr | Consumables and wear (nozzles, beds, lubrication) |
| Depreciation rate | `cost_depreciation_rate` | `0.05` $/hr | Printer value lost per hour of operation |
| Margin percent | `cost_margin_percent` | `0` % | Markup applied on top of total cost |
| Currency | `cost_currency` | `"USD"` | ISO currency code shown in history and exports |

_Source: `cost.go` → `GetCostSettings`, `constants.go`_

Defaults are applied on first startup. To reset to defaults, delete the relevant rows from the
`configuration` table.

---

## Per-printer overrides

Configured under **Settings → Printers → [Printer Name] → Cost Settings**, or via
`POST /api/printers/:id/cost-settings`. A value of `0` means "use the global setting".

| Field | Description |
|---|---|
| `print_wattage_w` | Wattage this printer draws during the print move. Overrides global `cost_printer_wattage`. |
| `preheat_wattage_w` | Wattage during bed and hotend warmup. Only charged if `preheat_time_min > 0`. |
| `preheat_time_min` | Minutes of preheat per print. Charged once, not per hour. |
| `high_temp_extra_w` | Extra watts added to `print_wattage_w` when a high-temp material is detected. |
| `printer_purchase_cost` | What the printer cost, used to derive hourly depreciation. |
| `estimated_life_hrs` | Expected print-hours before replacement. Combined with `printer_purchase_cost`. |
| `depreciation_per_hr` | Direct hourly depreciation override. Takes precedence over the derived rate. |

_Source: `cost.go` → `PrinterCostSettings`_

### Example: Prusa CORE One L with enclosure

A 300 W printer with a heated enclosure running ABS:

```json
{
  "print_wattage_w": 300,
  "preheat_wattage_w": 500,
  "preheat_time_min": 15,
  "high_temp_extra_w": 80,
  "printer_purchase_cost": 1200,
  "estimated_life_hrs": 5000
}
```

Depreciation = 1200 / 5000 = **$0.24/hr**. A 2-hour ABS print adds 80 W for 2 hours =
0.08 kWh of extra electricity (about $0.01 at $0.12/kWh).

---

## High-temp material detection

When a spool is assigned, The Moment checks the Spoolman `material` field against a fixed list
of high-temp materials. If the material matches and `high_temp_extra_w > 0`, the extra wattage
is added to `print_wattage_w` for the duration of the print. The `high_temp_applied` flag in
the cost breakdown confirms when this happened.

Materials treated as high-temp:

> ABS · ASA · PA · PA6 · PA12 · PA-CF · PA-GF · PA12-CF · PC · PC-ABS · PC-CF · POM · PEEK · PPS · PEI · PPSU · NYLON

The check is case-insensitive and trims whitespace. Any material not in this list — including
PLA, PETG, TPU — is treated as standard-temp.

_Source: `cost.go` → `highTempMaterials`, `isHighTempMaterial`_

---

## Filament price

Price per kg is read from Spoolman at print time. The lookup order:

1. **Spool-level price** — the optional price field on the specific spool record
2. **Filament-level price** — the price on the filament type used by the spool

If neither is set, filament cost is $0. No error is raised — prints still record time and
electricity costs.

_Source: `spoolman.go` → `PricePerKg`_

---

## Recalculating costs

Print costs are calculated when a print completes. If you change settings after the fact, you
can recalculate using:

- **Single print:** open the print detail modal → **Recalculate Cost**
- **Batch:** `POST /api/history/batch-recalc` — recalculates all history entries using current
  global settings (does not apply per-printer overrides to old records unless the printer name
  matches a saved per-printer config)

---

## What the cost breakdown returns

Each completed print stores a `CostBreakdown` with these fields:

| Field | Description |
|---|---|
| `filament_cost` | Grams used × price/kg ÷ 1000 |
| `preheat_cost` | One-time warmup electricity charge |
| `electricity_cost` | Print-duration electricity charge |
| `maintenance_cost` | Hourly maintenance rate × hours |
| `depreciation_cost` | Effective depreciation rate × hours |
| `sub_total` | Sum of the five components above |
| `margin_amount` | `sub_total × margin_percent / 100` |
| `total_cost` | `sub_total + margin_amount` |
| `print_wattage_used` | Effective wattage used (includes high-temp bump if applied) |
| `high_temp_applied` | `true` when `high_temp_extra_w` was added |
| `currency` | ISO code from settings at calculation time |

_Source: `cost.go` → `CostBreakdown`_

---

## Quick calculator

`POST /api/cost/calculate` runs the full cost calculation without persisting anything.
Useful for testing new settings before committing them.

**Request body:**

```json
{
  "filament_grams": 42.5,
  "print_time_min": 180,
  "spool_id": 7,
  "printer_name": "Core One L"
}
```

`spool_id` is optional — omit it for a materials-agnostic estimate. `printer_name` is optional
— omit it to use global settings only.

_See also: [Spool Lifecycle](spool-lifecycle.md)_
