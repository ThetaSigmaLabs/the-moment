# Roadmap

Ideas under consideration — not commitments, not scheduled. Captured here so they don't get lost.

---

## Analytics & Reporting

- [ ] Print count per G-code file — how many times a specific model has been printed
- [ ] Per-printer stats: total prints, total print time, total filament consumed, total cost
- [ ] Total time spent printing and total cost recovered per job (useful for print-as-a-service)
- [ ] Printed object inventory — running count of each distinct model produced across all printers
- [ ] Export — CSV or PDF of print history and cost breakdown (tax records, invoicing)
- [ ] Printer utilization — uptime %, prints per week, idle time trends over time

## Spool & Filament Reports

- [ ] Spool summary report: totals by filament type and color across all spools
- [ ] Estimated spool depletion date based on recent usage rate
- [ ] Low-spool threshold alerts — configurable per spool or material type
- [ ] Actual vs. slicer-estimated filament comparison — flag prints where reported usage diverged significantly from the slicer's estimate (useful for catching bad flow calibration)

## Slicer Integration — OrcaSlicer Calibration Sync

The Moment is the source of truth for filament calibration values. OrcaSlicer stores its own copies in JSON profile files on the user's machine.

**Currently stored in The Moment** (as Spoolman `extra` fields):
- `cal_pressure_advance` → OrcaSlicer `pressure_advance`
- `cal_flow_ratio` → OrcaSlicer `filament_flow_ratio`
- `cal_retraction_length` → OrcaSlicer `filament_retract_length`
- `cal_retraction_speed` → OrcaSlicer `filament_retract_speed`

**Proposed sync workflow (browser-based, no laptop agent required):**

- [ ] User uploads their OrcaSlicer filament profile JSON via the web UI
- [ ] The Moment diffs its stored calibration values against the uploaded profile, field by field
- [ ] User accepts Moment values or OrcaSlicer values per field
- [ ] The Moment generates a resolved OrcaSlicer-compatible profile JSON for download and re-import

**Longer term:**
- [ ] Companion script/CLI for the user's laptop — reads OrcaSlicer profile directory, POSTs to The Moment API, writes resolved profiles back to disk without manual download/import

## Multi-user

- [ ] Multiple user accounts with per-user history views
- [ ] Role-based access — admin (full control) vs. view-only (read history, no config changes)

## Notifications & Automation

- [ ] Push alerts on print complete or failure — via ntfy, Pushover, or generic webhook
- [ ] Print failure logging — record failed prints with wasted filament and cost tallied separately
- [ ] Print queue — log planned upcoming prints against available spool inventory

## Printer Support

- [ ] Bambu hardware validation — promote beta → fully supported once tested against a physical X1C, P1S, or A1
- [ ] INDX 8-head — full multi-toolhead support when hardware ships (fall target per vendor)
- [ ] Material compatibility matrix — record which filament materials each printer is rated for; warn on mismatch at assignment time

### Bambu Printer Support (Hardware Testing Required)

MQTT client implemented in `bambu.go`. Disabled in UI pending validation on real hardware.
LAN mode must be enabled on the printer. No specific models are targeted.
Re-enable when hardware testing is complete (see CLAUDE.md for re-enable steps).

### Testing Improvements

- Backup/restore automated tests (`/api/backup/*`) — critical data-safety workflow
- Virtual printer file processing tests (`/api/printers/:id/files/*`) — complex G-code parsing pipeline
- Mobile NFC assignment flow tests (`/nfc/spool/*`, `/nfc/location/*`) — core user-facing workflow
- Error response hardening — wrap internal `err.Error()` before returning to HTTP clients

## Spool Lifecycle

- [ ] Spool transfer audit log — record when a spool moved between printers or storage locations, with timestamps
- [ ] Carbon/electricity footprint per spool — cumulative kWh consumed across all prints from a given spool
