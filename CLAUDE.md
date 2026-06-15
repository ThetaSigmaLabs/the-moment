# CLAUDE.md — The Moment

## Response Style

Be brief. No filler. No pleasantries. Just the answer.
Drop articles (a, an, the) when possible.
Code blocks normal. Technical terms exact.
There are unit and integration tests.
Make sure you run the test cases to ensure nothing is broken.
Before doing any work, mention how you could verify that work.
Before doing any work, interview me about it: what is the core problem to solve; what is success; what should this not do.
Assess the changes and add unit test, integration tests where appropriate, and ensure the coverage works for the Jenkinsfile pipeline testing.

## Project Overview

**The Moment** is a Go microservice (forked from FilaBridge, GPL-3.0) that bridges 3D printers to Spoolman for filament inventory tracking, cost estimation, and print history logging. Deployed as a Docker container on an Odroid N2+. NFC tag support (Phase 1 complete) lets physical spools be assigned to printer toolheads by scanning an NFC tag on iPhone.

---

## Tech Stack

- **Language:** Go (Gin framework)
- **Database:** SQLite (via `database/sql` + `modernc.org/sqlite`)
- **Frontend:** Vanilla JS + HTML served by Gin, WebSockets for live updates
- **External APIs:** Spoolman REST API v1, PrusaLink API v1, OctoPrint API
- **Deployment:** Docker on Odroid N2+; dev on MacBook Pro Intel

---

## Printer Inventory

| Printer | Interface | Notes |
| --- | --- | --- |
| Ender 3 V3 SE | OctoPrint | Marlin-based, single toolhead |
| Prusa CORE One L | PrusaLink + PrusaConnect | Single toolhead; built-in NFC reader coming in 2026; INDX multi-toolhead in fall |
| Bambu X1C / P1S / A1 (future) | Bambu MQTT | Not tested; AMS = multi-toolhead; each AMS slot maps to a toolhead index |
| INDX 8-head (future) | TBD | Multi-toolhead, pre-mapped |

---

## Architecture Principles

1. **Spoolman is the source of truth** for all filament and spool data. The Moment never duplicates filament specs — it reads from Spoolman and writes `used_weight` back after prints.
2. **Virtual printers must always be explicitly checked** (`IsVirtual` flag). Never rely on implicit behaviour to exclude them from hardware polling.
3. **DB cleanup must be complete.** Deleting a printer cascades to `toolhead_mappings` and `toolhead_names`.
4. **UI text extraction uses `data-*` attributes**, never `textContent` of elements that may contain child nodes.
5. **All edge cases handled before code is presented as final.** No "patch it after" patterns.
6. **Host address is never statically configured.** NFC tag URLs use `c.Request.Host` (Gin's HTTP Host header) — the app auto-detects whatever address the client used to reach it. Do not add a `THE_MOMENT_IP` or `THE_MOMENT_HOST` env var; `c.Request.Host` already includes the port and adapts correctly across LAN, hostname, and VPN access.
7. **Printer record creation rules differ by interface type.** OctoPrint is push-based: The Moment accepts any `printer_id` from an authenticated POST, even with no matching config. The API key is the security gate; rejecting unknown printer IDs would cause permanent data loss (no retry queue). PrusaLink and Virtual are pull-based: a printer config must exist before The Moment polls or processes them. `print_history.printer_name` is a plain `TEXT` column with no foreign key; creating, renaming, or deleting a printer config has no effect on existing history records.
8. **OctoPrint `print_history.spool_id` is backfilled from the filament payload.** The legacy `spool_id` column is populated from the primary filament entry (tool_index=0, change_number=0, spool_id>0) after per-tool rows are written to `print_filament_usage`. Per-tool breakdown always comes from `print_filament_usage`; the `spool_id` on `print_history` is T0 only.
9. **Spools are never filtered by remaining weight.** `GetAllSpools()` must return every spool from Spoolman regardless of `remaining_weight` — including zero and negative values. Negative remaining weight is valid: print weight estimates overshoot, the user may have unknown filament left on a spool, or manual corrections may not reconcile perfectly. Filtering by weight would hide valid spools from toolhead assignment, Print Ops dropdowns, history reassignment, and cost lookups. Only an explicit archive (🗑️ trash workflow) removes a spool from active lists. Do not re-add a `remaining_weight > 0` guard anywhere in `GetAllSpools()`, `availableSpoolsHandler`, or the dashboard template.
10. **Searchable dropdowns use a stored-options array, not DOM filtering.** When a `<select>` needs live search, keep a module-level array of `{id, label}` objects populated on fetch. Re-render the options on each `input` event by iterating the array — do not hide/show `<option>` elements (browser support is inconsistent). Search is case-insensitive, word-tokenized (AND logic), with `"quoted phrase"` support for exact substring matching. See `filterReassignSpools` / `_parseReassignTokens` in `static/js/history.js` as the reference implementation.
11. **Jenkins arm64 binary must be statically linked.** The Odroid (Ubuntu, glibc) compiles the arm64 binary natively for use with the `production-prebuilt` Dockerfile target. A dynamically-linked glibc binary crashes immediately in the Alpine Docker image (`exec ./main: no such file or directory`) because Alpine uses musl libc. The `Jenkinsfile` `Build Binaries: linux/arm64` step must always include `-tags "osusergo netgo" -ldflags="-s -w -extldflags '-static'"`. Do not simplify this to just `-ldflags="-s -w"`. The GitHub Actions `docker-build.yml` is not affected — it compiles inside `golang:1.24-alpine` and produces a musl-correct binary automatically.

---

## Environment Variables (`.env`)

Copy `.env.example` to `.env` and adjust. `.env` is gitignored — never commit it. The Makefile loads it via `-include .env`.

| Variable | Read by | Default | Purpose |
| --- | --- | --- | --- |
| `THE_MOMENT_PORT` | `docker-compose.yml`, `main.go` | `5000` | Host-side port The Moment listens on |
| `SPOOLMAN_PORT` | `docker-compose.yml` | `7912` | Host-side port Spoolman listens on |
| `THE_MOMENT_DB_PATH` | `docker-compose.yml`, `config.go` | `./the-moment-data/db` | Host directory for SQLite DB. Bind-mounted to `/app/data/db` in container. Dev: `.dev/db`. |
| `THE_MOMENT_GCODE_PATH` | `docker-compose.yml`, `config.go` | `./the-moment-data/gcode` | Host directory for print history file attachments (gcode, slicer, etc.). Bind-mounted to `/app/data/gcode`. Dev: `.dev/gcode`. |
| `THE_MOMENT_UPLOADS_PATH` | `docker-compose.yml`, `config.go` | `./the-moment-data/uploads` | Host directory for virtual printer uploaded files. Bind-mounted to `/app/data/uploads`. Dev: `.dev/uploads`. |
| `SPOOLMAN_DB_PATH` | `docker-compose.yml` | `./spoolman-data` | Host directory for Spoolman's SQLite DB. Bind-mounted to `/home/spoolman/data`. |
| `BACKUP_DIR` | `Makefile` | `./backups` | Where `make backup` writes timestamped `.tar.gz` archives. |
| `SPOOLMAN_URL` | `main.go` (first-run seed only) | `http://localhost:7912` | Internal URL to reach Spoolman. Set to `http://spoolman:8000` in `docker-compose.yml` for Docker DNS. Only applied on first run; ignored after that. |
| `BAMBU_DEBUG` | `bambu.go` | `0` | Set to `1` for verbose Bambu MQTT debug logging. Also hot-togglable via DB config key `bambu_debug = "true"`. |

Data directories are bind mounts, not Docker volumes — data lives on the host filesystem.

---

## Codebase Structure

```text
main.go                 — app entry point, Gin router setup
bridge.go               — FilamentBridge core, SetToolheadMapping, SyncSpoolmanLocationsToDB
config.go               — printer config load/save, LoadConfig, DeletePrinterConfig
cost.go                 — CostSettings, CostBreakdown, cost API routes
database.go             — SQLite init, migrations, all DB helper functions
monitor.go              — MonitorPrinters loop, skips IsVirtual printers
prusalink.go            — PrusaLink API client
octoprint.go            — OctoPrint API client
bambu.go                — Bambu MQTT client, BambuStatusProvider interface, AMS parsing
virtual.go              — virtual printer file upload, G-code parsing, StateVirtual
gcode.go                — ParseGcodeMetadata (extracts ;TIME:, JPG thumbnails)
history.go              — print history table, detail modal, note editing, delete
spoolman.go             — Spoolman API client
nfc.go                  — NDEF binary generation (BuildSpoolTagNDEF, BuildLocationTagNDEF, PrinterSlug)
nfc_routes.go           — NFC HTTP handlers, mobile spool/location pages
web.go                  — all HTTP handlers, WebSocket hub
location_sync_test.go   — integration tests for bidirectional Spoolman location sync
filament_calibration_test.go — filament calibration field, CBOR payload, and edit handler tests
static/                 — frontend HTML/CSS/JS
```

---

## OctoPrint Plugin

Plugin source is in `octoprint-plugin/`. Distributable is `octoprint-the-moment.zip` at repo root.

### Bumping the plugin version

Two files must be updated together:

1. `octoprint-plugin/octoprint_the_moment/__init__.py` — `__plugin_version__ = "x.y.z"` (line ~540)
2. `octoprint-plugin/octoprint_the_moment/templates/tab_the_moment.jinja2` — hardcoded version string in the Version row

> **Why hardcoded in the template?** OctoPrint does not call `get_template_vars()` for settings templates, so Jinja2 variable injection doesn't work there.

Rebuild the zip after editing:

```bash
cd octoprint-plugin && rm -f ../octoprint-the-moment.zip && \
  zip -r ../octoprint-the-moment.zip setup.py octoprint_the_moment/ \
      -x "octoprint_the_moment/__pycache__/*"
```

---

## Bambu Printer Support

Bambu printers communicate via MQTT over TLS. `bambu.go` implements the full integration.

### Credential Format

Encode credentials in the existing `APIKey` field as `serial:accesscode`:

```text
APIKey:    "00M09C380500001:testaccesscode"
IPAddress: "192.168.1.100"
```

`parseBambuCredentials(apiKey)` splits on `:`. Serial is the MQTT client ID and topic suffix.

### AMS → Toolhead Mapping

```text
slot_index = (ams_unit_index * 4) + tray_index
```

Set `Toolheads` to total AMS slots (4 for one AMS, 8 for two).

### MQTT Connection Details

- Broker: printer LAN IP, port 8883 (TLS, `InsecureSkipVerify: true`)
- Username: `"bblp"`, password: access code
- Topic: `device/{serial}/report`
- Payloads are **incremental** — `handleReport()` merges into cached state
- `filament_weight_total` in the FINISH payload gives total grams used

### Architecture: BambuStatusProvider Interface

```go
type BambuStatusProvider interface {
    Connect() error
    GetCurrentStatus() (*BambuStatus, error)
    Close() error
}
```

`FilamentBridge` holds a factory (`bambuClientFactory`). Tests inject `MockBambuClient` — do not bypass this pattern.

### Debug Logging

Enable via `BAMBU_DEBUG=1` env var (restart required) or DB config key `bambu_debug = "true"` (hot-toggle). All lines carry prefix `[BAMBU DEBUG]`.

```bash
docker logs the-moment 2>&1 | grep "BAMBU"   # collect
docker logs -f the-moment 2>&1 | grep "BAMBU" # tail live
```

### Bambu Tests

```bash
go test -tags=integration -v -run TestBambu ./...
```

All 10 tests must pass before any Bambu change is merged.

---

## NFC & Spoolman Spool Workflow

Phase 1 is complete. See `nfc.go`, `nfc_routes.go`, and `docs/` for implementation details.

### NFC Tag Types

`{MOMENT_HOST}` is resolved at request time from `c.Request.Host` — never configure it statically.

**Spool tags** (ICODE SLIX2, dual-record: OpenPrintTag CBOR + URL):

```text
http://{MOMENT_HOST}/nfc/spool/{spoolman-spool-id}
```

**Location tags** (NTAG215, URL only):

```text
http://{MOMENT_HOST}/nfc/location/{printer-slug}/{toolhead-index}
```

Spool tags use ICODE SLIX2 with a dual-record NDEF message (OpenPrintTag CBOR + URL). See OpenPrintTag section below.

### Spoolman Custom Fields

Auto-registered at startup via `EnsureSpoolmanFields()`. Required fields (all prefixed `nfc_`):

| Key | Type | Entity | Description |
| --- | --- | --- | --- |
| `nfc_material_class` | text | Filament | `"FFF"` or `"SLA"` |
| `nfc_min_print_temp` | integer | Filament | Min nozzle °C |
| `nfc_max_print_temp` | integer | Filament | Max nozzle °C |
| `nfc_min_bed_temp` | integer | Filament | Min bed °C |
| `nfc_max_bed_temp` | integer | Filament | Max bed °C |
| `nfc_country_of_origin` | text | Filament | ISO code e.g. `"CZ"` |
| `nfc_material_properties` | text | Filament | JSON array e.g. `["abrasive","matte"]` |
| `nfc_transmission_distance` | float | Filament | Opacity value for HueForge |
| `nfc_nominal_length` | integer | Filament | Length in mm |
| `nfc_spool_uuid` | text | Spool | UUID v4, unique per physical spool |
| `nfc_actual_weight` | float | Spool | Measured weight in grams |
| `nfc_manufacturing_date` | text | Spool | ISO date |
| `nfc_expiration_date` | text | Spool | ISO date |

**`default_value` encoding gotcha** — the value must be JSON-encoded inside the JSON body:

```go
// text field → default_value must contain literal ""  (two quote chars)
DefaultValue: `""`      // correct
DefaultValue: ""        // WRONG — rejected by Spoolman

// integer field → default_value is the bare number
DefaultValue: "0"       // correct
DefaultValue: `"0"`     // WRONG — Spoolman rejects string for integer field
```

**Setting a custom field on an existing spool** — value in `extra` map must also be JSON-encoded:

```go
extraValue, _ := json.Marshal("some-uuid-value")  // → `"some-uuid-value"` with quotes
body := map[string]interface{}{
    "extra": map[string]string{"nfc_spool_uuid": string(extraValue)},
}
```

---

## Spoolman Location Sync

Optional bidirectional sync between toolhead assignments and Spoolman's spool `location` field. Toggle: Settings → Advanced → NFC Spool Locations.

### Location format

```text
{Printer Name} - T{toolhead_index}
```

0-based index. Examples: `"Ender3 - T0"`, `"Core One L - T2"`. Any Spoolman location not matching this pattern is ignored.

### Config keys

| Key | Type | Default | Notes |
| --- | --- | --- | --- |
| `spoolman_location_sync_enabled` | string `"true"`/`"false"` | `"false"` | Feature toggle |
| `nfc_inventory_location` | string | `"Inventory"` | Where unassigned spools go; shared with the trash workflow |
| `location_sync_interval` | duration | `5m` | How often `SyncSpoolmanLocationsToDB` polls |

### Key functions (`bridge.go`)

- `GetSpoolmanLocationSyncEnabled()` — reads the toggle
- `FormatToolheadLocation(printerName, toolheadIndex)` — formats the location string
- `ParseToolheadLocation(location)` — parses back to (printerName, toolheadIndex, ok)
- `pushSpoolToInventory(spoolID)` — no-op if sync disabled or inventory location empty
- `SyncSpoolmanLocationsToDB() (bool, error)` — full bidirectional poll; returns `changed=true` if any assignment changed; also exposed as `POST /api/nfc/sync-locations-now`

### Mutex pattern for Spoolman HTTP calls

Spoolman HTTP calls must never be made while holding `b.mutex`:

```go
b.mutex.Lock()
// ... DB reads and writes ...
b.mutex.Unlock()   // explicit unlock — no defer
// ... Spoolman HTTP calls (best effort, log errors) ...
```

### Trigger points

**Moment→Spoolman (immediate):**

- Spool assigned via `SetToolheadMapping` → writes `{Name} - T{N}`
- Spool unassigned via `UnmapToolhead` or `ClearToolheadMappingsBySpool` → calls `pushSpoolToInventory`
- Toolhead count reduced → removed slots call `pushSpoolToInventory`
- Printer deleted → all slots call `pushSpoolToInventory`

**Spoolman→Moment:**

- 5-minute `locationSyncTicker` polls `SyncSpoolmanLocationsToDB`
- Opening the Print Ops tab fires `POST /api/nfc/sync-locations-now` immediately, so Spoolman changes are visible without waiting for the next poll

**Sync algorithm** (removals before additions): `SyncSpoolmanLocationsToDB` runs Pass 1 (clear stale DB mappings) before Pass 2 (add new mappings from Spoolman), so a slot reassignment (spool A replaced by spool B in Spoolman) resolves in a single cycle rather than two.

### Loop prevention

After The Moment assigns a spool and writes to Spoolman, the next `SyncSpoolmanLocationsToDB` poll finds the spool already at the expected location → no change, no loop.

---

## Critical Gotcha: SQLite DATETIME Comparisons

`DATETIME` columns have NUMERIC affinity. Direct TEXT comparison of ISO 8601 timestamps produces incorrect results with go-sqlite3 v1.14.x — all rows evaluate as `<= pivot` regardless of value. Always wrap both sides in `julianday()`:

```sql
-- WRONG
AND assigned_at <= ?
AND (unassigned_at IS NULL OR unassigned_at > ?)

-- CORRECT
AND julianday(assigned_at) <= julianday(?)
AND (unassigned_at IS NULL OR julianday(unassigned_at) > julianday(?))
```

Pass the parameter as `atTime.UTC().Format(time.RFC3339Nano)`. SQLite's `julianday()` accepts the `Z` suffix.

**Named return variables:** Do not use named return variables — they caused bugs in `ProcessVirtualFile`. Use explicit `return value, err`.

---

## OpenPrintTag CBOR Tag Generation

**Status:** CBOR generation is implemented. Integration is being built incrementally:

- **Done:** CBOR payload generation (`buildOpenPrintTagPayload`, `BuildSpoolTagNDEF`)
- **In progress (`feature/openprinttag-integration`):** External filament database lookup — Settings → Open Print Tag tab (source URL registry with adapter types `ofd_api`/`filament_db_api`) and Add Filament NFC → OpenPrintTag tab (search external sources, populate `nfc_*` fields on Spoolman filament, create/update Spoolman record, create NFC tag). Key files: `openprinttag.go`, `openprinttag_handlers.go`, `openprinttag_test.go`.
- **Pending hardware:** INBXX Semi-Smart V2 reader integration (targeted Q3 2026)

**Why:** The Core One L's built-in NFC reader sits under its own spool holder. Spools in an INBXX enclosure feed via PTFE tube — the printer's reader never sees them. INBXX Semi-Smart V2 adds readers inside the enclosure slots.

### Current Tag Format

- **Spool tags** use ICODE SLIX2 (ISO 15693 / NFC-V, longer read range)
- **Tag format:** dual-record NDEF message:
  - Record 1: MIME type `application/vnd.openprinttag`, payload = OpenPrintTag CBOR binary
  - Record 2: URI → `http://{MOMENT_HOST}/nfc/spool/{id}` (iPhone fallback)
- **Location tags:** NTAG215, URL-only (unchanged)
- **Dependency:** `github.com/fxamacker/cbor/v2`
- **Key functions in `nfc.go`:** `buildOpenPrintTagPayload` (encodes CBOR), `BuildSpoolTagNDEF` (wraps into dual-record NDEF)

### OpenPrintTag field mapping (Spoolman → CBOR)

| OpenPrintTag Field | CBOR Key | Source |
| --- | --- | --- |
| `instanceUUID` | 0 | `nfc_spool_uuid` (spool) |
| `materialClass` | 8 | `nfc_material_class` — stored as `"FFF"`/`"SLA"` string; encoded as integer 0/1 |
| `materialType` | 9 | `filament.material` (integer enum via `optMaterialTypes`) |
| `materialName` | 10 | `filament.name` |
| `brandName` | 11 | `filament.vendor.name` |
| `manufacturingDate` | 14 | `nfc_manufacturing_date` (spool) |
| `expirationDate` | 15 | `nfc_expiration_date` (spool) |
| `nominalWeight` | 16 | `filament.weight` |
| `actualWeight` | 17 | `nfc_actual_weight` (spool), fallback `spool.initial_weight` |
| `emptyContainerWeight` | 18 | `filament.spool_weight` |
| `primaryColor` | 19 | `filament.color_hex` (RGB bytes) |
| `secondaryColor_0` | 20 | `filament.multi_color_hexes` comma index 0 |
| `secondaryColor_1` | 21 | `filament.multi_color_hexes` comma index 1 |
| `secondaryColor_2` | 22 | `filament.multi_color_hexes` comma index 2 |
| `secondaryColor_3` | 23 | `filament.multi_color_hexes` comma index 3 |
| `secondaryColor_4` | 24 | `filament.multi_color_hexes` comma index 4 |
| `transmissionDistance` | 27 | `nfc_transmission_distance` |
| `tags` (materialProperties) | 28 | `nfc_material_properties` — stored as JSON array of strings; encoded as `[]int` enum |
| `density` | 29 | `filament.density` |
| `filamentDiameter` | 30 | `filament.diameter` |
| `minPrintTemp` | 34 | `nfc_min_print_temp` |
| `maxPrintTemp` | 35 | `nfc_max_print_temp` |
| `minBedTemp` | 37 | `nfc_min_bed_temp` |
| `maxBedTemp` | 38 | `nfc_max_bed_temp` |
| `materialAbbreviation` | 52 | `filament.material` (raw string) |
| `nominalLength` | 53 | `nfc_nominal_length` |
| `countryOfOrigin` | 55 | `nfc_country_of_origin` |
| `consumedWeight` | aux:0 | `spool.used_weight` (aux section) |

Spec: `https://specs.openprinttag.org` / `https://github.com/OpenPrintTag/openprinttag-specification`

**Do not invent CBOR field keys.** Only use keys confirmed in the specification.

INBXX integration approach (Option A: INBXX→Spoolman directly, or Option B: INBXX POSTs to `/api/nfc/inbxx-scan` webhook) — evaluate when hardware is available.

---

## Private Files (local-only, excluded from GitHub)

`private_files` lists paths that stay in local git but are never pushed to GitHub.
To add a file: `make private-add FILE=path/to/file`
The `github-push` and `github-release` targets call `make github-push-check` first, which verifies all private files are excluded before any commit or push.
Run `make github-push-check` manually to dry-run the verification at any time.
Currently private: `private_files`, `Jenkinsfile`, `CLAUDE.md`, `SKILLS.md`, `docs/release-workflow.md`, `docs/ssh-keychain-undo.md`

---

## Bambu Printer Support (Stubbed — v1.1.0)

Bambu MQTT code is complete in `bambu.go` and `bridge.go` but disabled at the UI layer only
(no backend guards). Stub was applied in v1.1.0 — hardware testing required before re-enabling.

To re-enable:

1. Restore `<option value="bambu">Bambu (MQTT / LAN)</option>` in `templates/modals.html` (both instances — Add Printer and Edit Printer dropdowns)
2. Restore `<optgroup label="Bambu">` model option groups in `templates/modals.html` (Add Printer and Edit Printer model selects)
3. Restore Bambu badge `<span>` in `templates/settings.html` Supported Printer Interfaces section
4. Restore feature card text "OctoPrint, PrusaLink, and Bambu MQTT" in `templates/settings.html`
5. Restore `isBambu` variable and Bambu branches in `static/js/printers.js` (typeBadge, apiKeyLine, octoPrintHint around line 212; `else if (type === 'bambu')` block in `onPrinterTypeChange`; `|| printerType === 'bambu'` in form submit guard)
6. Test on real hardware with LAN mode enabled; serial:accesscode format in API key field
