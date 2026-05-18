# CLAUDE.md — The Moment: NFC & Spoolman Workflow

## Response Style

Be brief. No filler. No pleasantries. Just the answer.
Drop articles (a, an, the) when possible.
Code blocks normal. Technical terms exact.

## Project Overview

**The Moment** is a Go microservice (forked from FilaBridge, GPL-3.0) that bridges 3D printers to Spoolman for filament inventory tracking, cost estimation, and print history logging. It is deployed as a Docker container on an Odroid N2+.

This document describes the **NFC & Spoolman spool workflow** to be implemented. It is the authoritative guide for all coding work in this feature area.

---

## Tech Stack

- **Language:** Go (Gin framework)
- **Database:** SQLite (via `database/sql` + `modernc.org/sqlite`)
- **Frontend:** Vanilla JS + HTML served by Gin, using WebSockets for live updates
- **External APIs:** Spoolman REST API v1, PrusaLink API v1, OctoPrint API
- **Deployment:** Docker on Odroid N2+; dev on macBook Pro Intel

---

## Printer Inventory

| Printer | Interface | Notes |
| --- | --- | --- |
| Ender 3 V3 SE | OctoPrint | Marlin-based, single toolhead |
| Prusa CORE One L | PrusaLink + PrusaConnect | Single toolhead, NFC reader coming in 2026 |
| Bambu X1C / P1S / A1 (future) | Bambu MQTT | AMS = multi-toolhead; each AMS slot maps to a toolhead index |
| INDX 8-head (future) | TBD | Multi-toolhead, pre-mapped |

---

## Architecture Principles

1. **Spoolman is the source of truth** for all filament and spool data. The Moment never duplicates filament specs — it reads from Spoolman and writes `used_weight` back after prints.
2. **Virtual printers must always be explicitly checked** (`IsVirtual` flag). Never rely on implicit behaviour to exclude them from hardware polling.
3. **DB cleanup must be complete.** Deleting a printer cascades to `toolhead_mappings` and `toolhead_names`.
4. **UI text extraction uses `data-*` attributes**, never `textContent` of elements that may contain child nodes.
5. **All edge cases handled before code is presented as final.** No "patch it after" patterns.
6. **Host address is never statically configured.** NFC tag URLs use `c.Request.Host` (Gin's HTTP Host header) — the app auto-detects whatever address the client used to reach it. Do not add a `THE_MOMENT_IP` or `THE_MOMENT_HOST` env var; `c.Request.Host` already includes the port and adapts correctly across LAN, hostname, and VPN access.
7. **Printer record creation rules differ by interface type.** OctoPrint is push-based: The Moment accepts any `printer_id` from an authenticated POST, even with no matching config. The API key is the security gate; rejecting unknown printer IDs would cause permanent data loss (no retry queue). PrusaLink and Virtual are pull-based: a printer config must exist before The Moment polls or processes them — there is no push path to miss. `print_history.printer_name` is a plain `TEXT` column with no foreign key; creating, renaming, or deleting a printer config has no effect on existing history records. Printer-specific cost rates only apply when a matching config exists at print time; no retroactive recalculation occurs.
8. **OctoPrint `print_history.spool_id` is backfilled from the filament payload.** The legacy `spool_id` column on `print_history` is populated from the primary filament entry (tool_index=0, change_number=0, spool_id>0) after per-tool rows are written to `print_filament_usage`. Per-tool breakdown always comes from `print_filament_usage`; the `spool_id` on `print_history` is T0 only, used for display and cost recalculation from the modal.

---

## Environment Variables (`.env`)

Copy `.env.example` to `.env` and adjust. `.env` is gitignored — never commit it. The Makefile loads it automatically via `-include .env`.

| Variable | Read by | Default | Purpose |
| --- | --- | --- | --- |
| `THE_MOMENT_PORT` | `docker-compose.yml`, `main.go` | `5000` | Host-side port The Moment listens on |
| `SPOOLMAN_PORT` | `docker-compose.yml` | `7912` | Host-side port Spoolman listens on |
| `THE_MOMENT_DB_PATH` | `docker-compose.yml` (bind mount source), `config.go` | `./the-moment-data` | Host directory for The Moment's SQLite DB and config files. Bind-mounted to `/app/data` inside the container. |
| `SPOOLMAN_DB_PATH` | `docker-compose.yml` (bind mount source) | `./spoolman-data` | Host directory for Spoolman's SQLite DB. Bind-mounted to `/home/spoolman/data` inside the container. |
| `BACKUP_DIR` | `Makefile` | `./backups` | Where `make backup` writes timestamped `.tar.gz` archives. Can be an absolute path or a network-mounted share. |
| `SPOOLMAN_URL` | `main.go` (first-run seed only) | `http://localhost:7912` | Internal URL The Moment uses to reach Spoolman. Set to `http://spoolman:8000` in `docker-compose.yml` to use Docker DNS. Only applied on first run if the DB value is still the default; ignored after that. |
| `BAMBU_DEBUG` | `bambu.go` | `0` | Set to `1` to enable verbose Bambu MQTT debug logging. Also toggleable at runtime via DB config key `bambu_debug = "true"` without a restart. |

**Data directories are bind mounts, not Docker volumes.** This makes backup and migration simple — the data lives on the host filesystem, not inside Docker's volume store. Paths can be relative (resolved from the `docker-compose.yml` directory) or absolute.

**`SPOOLMAN_URL` is a first-run seed, not a live config.** After The Moment writes the URL to its SQLite DB on first startup, the env var is ignored. To change the Spoolman URL after first run, update it via the UI or edit the DB directly.

---

## Existing Codebase Structure (relevant files)

```text
main.go                 — app entry point, Gin router setup
config.go               — printer config load/save, LoadConfig, DeletePrinterConfig
cost.go                 — CostSettings, CostBreakdown, cost API routes
database.go             — SQLite init, migrations, all DB helper functions
monitor.go              — MonitorPrinters loop, skips IsVirtual printers
prusalink.go            — PrusaLink API client
octoprint.go            — OctoPrint API client
bambu.go                — Bambu MQTT client, BambuStatusProvider interface, AMS parsing, debug logging
virtual.go              — virtual printer file upload, G-code parsing, StateVirtual
gcode.go                — ParseGcodeMetadata (extracts ;TIME:, JPG thumbnails)
history.go              — print history table, detail modal, note editing, delete
spoolman.go             — Spoolman API client (existing, partial)
static/                 — frontend HTML/CSS/JS
```

---

## OctoPrint Plugin

Plugin source is in `octoprint-plugin/`. The distributable is `octoprint-the-moment.zip` at repo root.

### Bumping the plugin version

Two files must be updated together — both must show the same version string:

1. `octoprint-plugin/octoprint_the_moment/__init__.py` — `__plugin_version__ = "x.y.z"` (line ~540)
2. `octoprint-plugin/octoprint_the_moment/templates/tab_the_moment.jinja2` — hardcoded `v1.1.0` in the Version row

> **Why hardcoded in the template?** OctoPrint does not call `get_template_vars()` for settings templates, so Jinja2 variable injection doesn't work there. The version is a literal string.

After editing both files, rebuild the zip:

```bash
cd octoprint-plugin && rm -f ../octoprint-the-moment.zip && \
  zip -r ../octoprint-the-moment.zip setup.py octoprint_the_moment/ \
      -x "octoprint_the_moment/__pycache__/*"
```

Install via OctoPrint Plugin Manager → "Install from file".

---

## Bambu Printer Support

Bambu printers (X1C, P1S, A1, A1 Mini, etc.) communicate via MQTT over TLS. They do not use PrusaLink or OctoPrint. `bambu.go` implements the full Bambu integration.

### Credential Format

No new struct fields. Encode credentials in the existing `APIKey` field as `serial:accesscode`:

```text
APIKey:    "00M09C380500001:testaccesscode"
IPAddress: "192.168.1.100"   (printer's LAN IP)
```

`parseBambuCredentials(apiKey)` splits on `:`. The serial is also the MQTT client ID and the MQTT topic suffix.

### AMS → Toolhead Mapping

AMS (Automatic Material System) slots map directly to The Moment's toolhead index concept:

```text
slot_index = (ams_unit_index * 4) + tray_index
```

- AMS unit 0, tray 0 → toolhead 0
- AMS unit 0, tray 3 → toolhead 3
- AMS unit 1, tray 0 → toolhead 4

Set `Toolheads` in the printer config to the total number of AMS slots (e.g. 4 for a single AMS, 8 for two). Assign Spoolman spools to slots via the NFC workflow exactly as with any other multi-toolhead printer — The Moment fills the filament identity gap that Bambu's optional RFID does not require.

### MQTT Connection Details

- Broker: printer's LAN IP, port 8883 (TLS)
- Username: `"bblp"`, password: access code
- Topic: `device/{serial}/report`
- TLS: `InsecureSkipVerify: true` (Bambu uses a self-signed cert)
- Payloads are **incremental** — only changed fields appear; `handleReport()` merges into cached state
- `filament_weight_total` in the FINISH payload gives total grams used — no G-code download needed

### Architecture: BambuStatusProvider Interface

```go
type BambuStatusProvider interface {
    Connect() error
    GetCurrentStatus() (*BambuStatus, error)
    Close() error
}
```

`FilamentBridge` holds a factory:

```go
bambuClientFactory func(ip, serial, accessCode string) BambuStatusProvider
```

Tests override the factory to inject `MockBambuClient` — no network, no MQTT broker needed. Production uses `NewBambuMQTTClient`. Do not bypass this pattern when adding Bambu tests.

### Bambu Debug Logging

Two ways to enable verbose debug logging:

1. **Environment variable** (requires container restart):

   ```bash
   BAMBU_DEBUG=1
   ```

2. **DB config key** (hot-toggle, no restart needed):

   ```text
   bambu_debug = "true"
   ```

   Toggle via Settings → Advanced or directly in the DB.

When enabled, all Bambu log lines carry the prefix `[BAMBU DEBUG]`. Paho MQTT internal logs use `[BAMBU MQTT]` / `[BAMBU MQTT WARN]` / `[BAMBU MQTT ERROR]`.

#### Collecting a Debug Log Transcript

```bash
# Collect from Docker (all Bambu lines)
docker logs the-moment 2>&1 | grep "BAMBU"

# Tail live
docker logs -f the-moment 2>&1 | grep "BAMBU"
```

**What the log captures:**

| Line | Diagnoses |
| --- | --- |
| `[BAMBU DEBUG] Connecting to tls://…` | Whether connect is attempted |
| `[BAMBU DEBUG] TLS handshake result: ok/error` | Cert trust / TLS failure |
| `[BAMBU DEBUG] MQTT connect result: …` | CONNACK code (auth failure, broker unavailable) |
| `[BAMBU DEBUG] Raw MQTT payload (N bytes): …` | Full JSON — verifies topic subscription works |
| `[BAMBU DEBUG] Parsed status: gcode_state=… mc_percent=… filament_weight_total=…` | Field name alignment |
| `[BAMBU DEBUG] Incremental merge: field X updated old → new` | Partial update handling |
| `[BAMBU DEBUG] AMS unit N tray N: type=… color=… remain=…%` | AMS slot parsing |
| `[BAMBU DEBUG] State transition: X → Y` | State machine correctness |
| `[BAMBU DEBUG] Triggering print finish: file=… weight_total=…g` | End-of-print detection |
| `[BAMBU DEBUG] computeBambuFilamentUsage: active_slots=N per_slot=…g` | Usage distribution |

Provide this transcript when asking Claude to debug a real-printer integration issue. Include lines from printer add through at least one complete print cycle.

### Bambu-Specific Tests

```bash
# Run all Bambu integration tests
go test -tags=integration -v -run TestBambu ./...
```

Tests use `MockBambuClient` — no hardware required. All 10 tests must pass before any Bambu change is merged.

---

## Feature: NFC & Spoolman Spool Workflow

### Concept

Spoolman holds all filament and spool data, including OpenPrintTag-compatible fields stored as Spoolman custom fields. The Moment:

1. Reads spool data from Spoolman
2. Generates URL-only NDEF binary files (`.bin`) for writing to NFC tags via NFC Tools iPhone app ("Write Dump" feature)
3. Serves web pages at NFC tag URLs so iPhone scanning a tag opens The Moment UI in context
4. Records which spool is loaded on which toolhead at any point in time
5. Records spool swap events during prints (runout, manual swap, multi-colour)
6. After print completion, pushes consumed filament weight back to Spoolman per spool

**Note on OpenPrintTag CBOR:** The Spoolman custom fields (all `nfc_*` fields) are populated and maintained now so the data is ready. Generating the CBOR binary for NFC tags is **Phase 2** work, deferred until INBXX Semi-Smart V2 hardware ships (targeted Q3 2026). See the Phase 2 section at the end of this document.

### NFC Tag Types

**`{MOMENT_HOST}` resolution:** In URL format examples below, `{MOMENT_HOST}` is not a configured value — it is resolved at request time from `c.Request.Host` (the HTTP Host header). This includes the port automatically (e.g. `192.168.1.50:5001`). Do not add an env var for this; the auto-detection approach handles LAN IP, mDNS hostname, and VPN access without any static config.

#### Location Tags (cheap NTAG213 — URL only)

These are small stickers placed at each printer toolhead position. They contain one NDEF URL record.

**URL format:**

```text
http://{MOMENT_HOST}/nfc/location/{printer-slug}/{toolhead-index}
```

**Examples:**

```text
http://192.168.1.50:8080/nfc/location/core-one-l/0
http://192.168.1.50:8080/nfc/location/ender3/0
```

When scanned, the iPhone browser opens The Moment's web UI pre-set to assign a spool to that toolhead.

#### Spool Tags (NTAG213 — URL only, Phase 1)

These are stickers on physical filament spools. They contain **one NDEF URI record** pointing to The Moment.

**URL format:**

```text
http://{MOMENT_HOST}/nfc/spool/{spoolman-spool-id}
```

**Example:**

```text
http://192.168.1.50:8080/nfc/spool/42
```

When an iPhone scans a spool tag, the browser opens The Moment's spool assignment page. The Moment looks up the spool in Spoolman by ID and displays all data.

**Tag hardware:** NTAG213 is sufficient — a short URL fits in 144 bytes easily. No need for NTAG215 or ICODE SLIX2 in Phase 1.

**Phase 2 note:** When INBXX Semi-Smart V2 ships (targeted Q3 2026), spool tags will be upgraded to ICODE SLIX2 with two NDEF records — Record 1: OpenPrintTag CBOR (for INBXX hardware reader), Record 2: the URL above (for iPhone). The URL record and the Spoolman data are already in place; only the CBOR generation needs to be added. See the Phase 2 section.

---

### New Files to Create

#### `nfc.go`

URL-based NDEF tag binary generation. **No CBOR in Phase 1.**

**Responsibilities:**

- `BuildSpoolTagNDEF(spoolID int, host string) ([]byte, error)` — builds a single URI NDEF record pointing to `/nfc/spool/{spoolID}` as raw bytes suitable for NFC Tools "Write Dump"
- `BuildLocationTagNDEF(printerSlug string, toolheadIndex int, host string) ([]byte, error)` — builds a single URI NDEF record pointing to `/nfc/location/{slug}/{index}`
- `PrinterSlug(name string) string` — converts printer name to URL-safe slug (lowercase, spaces → hyphens, strip non-alphanumeric)

**NDEF URI record format:**

An NDEF URI record for `http://` URLs uses:

- TNF: `0x03` (Absolute URI) or `0x01` (Well Known) with RTD `"U"` and URI prefix code `0x03` for `http://`
- Use the Well Known + URI prefix approach for compactness

The raw bytes written to the tag must be a valid NDEF message. Build this manually in Go — do not add an external NDEF library for something this simple. The structure is well-documented and only two record types are needed (both identical URI structure).

**OpenPrintTag field mapping** — kept in Spoolman custom fields for Phase 2 use, but not written to tags in Phase 1. See the field table in the Spoolman Custom Fields Setup section.

---

#### `nfc_routes.go`

HTTP handlers for all NFC-related endpoints. Register these in `main.go`.

**API endpoints (JSON):**

```text
GET  /api/nfc/spool-tag/:spoolman_id
     → Generates and returns a URL-only NDEF .bin file for a spool tag
     → Sets Content-Disposition: attachment; filename="spool-{id}.bin"
     → Tag contains one URI record: http://{host}/nfc/spool/{spoolman_id}
     → If nfc_spool_uuid is empty in Spoolman, auto-generates UUID v4 and saves it first

GET  /api/nfc/location-tag/:printer_slug/:toolhead_index
     → Generates and returns the single URI NDEF .bin file for a location tag
     → Sets Content-Disposition: attachment; filename="location-{slug}-t{index}.bin"

GET  /api/nfc/spools
     → Proxy to Spoolman spool list with basic fields for display
     → Returns: [{id, name, material, vendor, color_hex, remaining_weight, nfc_spool_uuid}]

GET  /api/nfc/spools/:spoolman_id
     → Proxy to Spoolman spool detail (all fields + extra fields)

PATCH /api/nfc/spools/:spoolman_id/use
     → Body: {use_weight: float64}
     → Proxies to Spoolman POST /api/v1/spool/{id}/use
     → Updates consumed weight after a print

GET  /api/nfc/assignments
     → Returns current toolhead-to-spool assignments for all printers
     → [{printer_id, toolhead_index, spoolman_spool_id, assigned_at, spool_name, spool_color}]

POST /api/nfc/assignments
     → Body: {printer_id, toolhead_index, spoolman_spool_id, reason}
     → Assigns a spool to a toolhead; closes any existing open assignment for that slot
     → reason: "manual", "nfc_scan", "runout", "multicolor"

DELETE /api/nfc/assignments/:printer_id/:toolhead_index
     → Unassigns the spool from a toolhead slot

GET  /api/nfc/prints/:print_history_id/spool-events
     → Returns all spool events for a print (start assignments + swaps)

POST /api/nfc/prints/:print_history_id/spool-swap
     → Body: {toolhead_index, old_spool_id, new_spool_id, reason}
     → Records a spool swap event mid-print
     → reason: "runout", "manual", "multicolor"
```

**Web pages (HTML — served by Gin, open in iPhone browser via NFC scan):**

```text
GET  /nfc/location/:printer_slug/:toolhead_index
     → Renders a mobile-optimised HTML page
     → Shows: "Assigning Toolhead {index} on {printer name}"
     → Lists available spools from Spoolman (with remaining weight + colour swatch)
     → User taps a spool to assign it → POST /api/nfc/assignments → success message

GET  /nfc/spool/:spoolman_id
     → Renders a mobile-optimised HTML page
     → Shows spool info (name, material, colour, remaining weight)
     → Shows current assignment if any ("Currently on: Core One L Toolhead 0")
     → Offers: "Assign to..." (list of printer toolheads) or "View in Spoolman"
     → On assignment → POST /api/nfc/assignments → success message
```

These pages must work with no JavaScript framework dependency. Keep them simple — they render on a phone over local WiFi.

---

### New Database Tables

Add these in `database.go` as new migrations. Use the existing migration pattern in the codebase.

```sql
-- Current spool-to-toolhead assignments (one open record per slot)
CREATE TABLE IF NOT EXISTS toolhead_spool_assignments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    printer_id TEXT NOT NULL,
    toolhead_index INTEGER NOT NULL,
    spoolman_spool_id INTEGER NOT NULL,
    assigned_at DATETIME NOT NULL DEFAULT (datetime('now')),
    unassigned_at DATETIME,           -- NULL means currently active
    assignment_reason TEXT NOT NULL DEFAULT 'manual'
                                      -- 'manual', 'nfc_scan', 'runout', 'multicolor'
);

-- Spool events tied to a specific print record
CREATE TABLE IF NOT EXISTS print_spool_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    print_history_id INTEGER NOT NULL,
    toolhead_index INTEGER NOT NULL,
    old_spoolman_spool_id INTEGER,    -- NULL for the initial 'start' event
    new_spoolman_spool_id INTEGER NOT NULL,
    event_type TEXT NOT NULL,         -- 'start', 'swap_runout', 'swap_manual', 'swap_multicolor'
    event_time DATETIME NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (print_history_id) REFERENCES print_history(id) ON DELETE CASCADE
);
```

**Helper functions to add in `database.go`:**

- `GetCurrentAssignment(printerID string, toolheadIndex int) (*ToolheadSpoolAssignment, error)`
- `GetAllCurrentAssignments(printerID string) ([]ToolheadSpoolAssignment, error)`
- `SetAssignment(printerID string, toolheadIndex int, spoolmanSpoolID int, reason string) error` — closes any open assignment for that slot first
- `CloseAssignment(printerID string, toolheadIndex int) error`
- `GetPrintSpoolEvents(printHistoryID int) ([]PrintSpoolEvent, error)`
- `AddPrintSpoolEvent(event PrintSpoolEvent) error`
- `SnapshotAssignmentsForPrint(printHistoryID int, printerID string) error` — called when a print starts; copies current assignments as 'start' events

---

### Spoolman Client Updates (`spoolman.go`)

Add or verify these functions exist:

```go
// Get full spool detail including extra fields
func GetSpoolmanSpool(spoolID int) (*SpoolmanSpool, error)

// Get all spools (with pagination support)
func GetSpoolmanSpools() ([]SpoolmanSpool, error)

// Update consumed weight for a spool
func UseSpoolmanFilament(spoolID int, useWeightGrams float64) error

// Read a specific extra/custom field value from a spool
func GetSpoolExtraField(spool SpoolmanSpool, fieldKey string) string

// Write a custom field value back to Spoolman
func SetSpoolExtraField(spoolID int, fieldKey string, value string) error
```

**SpoolmanSpool struct** must include:

```go
type SpoolmanSpool struct {
    ID             int                    `json:"id"`
    Filament       SpoolmanFilament       `json:"filament"`
    UsedWeight     float64                `json:"used_weight"`
    RemainingWeight float64               `json:"remaining_weight"`
    LotNr          string                 `json:"lot_nr"`
    Location       string                 `json:"location"`
    Extra          map[string]interface{} `json:"extra"`  // custom fields
}

type SpoolmanFilament struct {
    ID             int              `json:"id"`
    Name           string           `json:"name"`
    Material       string           `json:"material"`
    ColorHex       string           `json:"color_hex"`
    MultiColorHexes string          `json:"multi_color_hexes"`
    Diameter       float64          `json:"diameter"`
    Weight         float64          `json:"weight"`
    SpoolWeight    float64          `json:"spool_weight"`
    ExtruderTemp   int              `json:"extruder_temp"`
    BedTemp        int              `json:"bed_temp"`
    Vendor         SpoolmanVendor   `json:"vendor"`
}

type SpoolmanVendor struct {
    ID   int    `json:"id"`
    Name string `json:"name"`
}
```

---

### Print Start/End Hooks

When The Moment detects a print has started (via PrusaLink or OctoPrint polling in `monitor.go`):

1. Call `SnapshotAssignmentsForPrint(printHistoryID, printerID)` — records which spools were loaded at print start as `event_type = 'start'` rows in `print_spool_events`

When The Moment detects a print has completed:

1. Read all `print_spool_events` for this print
2. For each spool that participated, calculate estimated grams used based on G-code metadata (already parsed by `ParseGcodeMetadata`)
3. Call `UseSpoolmanFilament(spoolID, grams)` for each spool to update Spoolman
4. This updates `remaining_weight` in Spoolman, which will be reflected next time the spool tag is regenerated

**Filament usage per spool per toolhead:** The G-code metadata from PrusaSlicer/OrcaSlicer includes per-filament estimates. Extract these by toolhead index. If not available, divide total usage proportionally by toolhead time.

---

### Print History Enhancements

In `history.go` and the frontend, add to the print detail modal:

- **Spools used:** table showing toolhead → spool name → estimated grams used → cost
- **Spool swap log:** list of swap events with timestamp and reason
- "Regenerate NFC Tag" button per spool (calls `/api/nfc/spool-tag/:id`, downloads `.bin`)

---

### Spoolman Custom Fields Setup

The Moment needs these Spoolman custom fields to exist before the NFC workflow functions. **Do not require the user to create these manually.** Auto-register them on startup, following the same pattern used by SpoolSense (an open-source ESP32 NFC reader that also auto-registers its fields in Spoolman on first sync).

#### API Endpoints

```text
GET /api/nfc/spoolman-setup-status
    → Checks whether all required custom fields exist in Spoolman
    → Returns: {ok: bool, missing: [{key, entity}]}
    → Used by the UI to show a warning banner if setup is incomplete

POST /api/nfc/spoolman-setup
    → Creates any missing custom fields in Spoolman
    → Idempotent — safe to run multiple times
    → Returns: {created: [{key, entity}], already_existed: [{key, entity}], errors: [{key, entity, error}]}
```

#### `EnsureSpoolmanFields()` — called at app startup

This function runs before the HTTP server starts accepting requests. It calls `GET /api/nfc/spoolman-setup-status` logic internally and creates any missing fields. Failures are logged but do not prevent startup — the NFC tab will show a warning banner instead.

Algorithm for each required field:

```text
1. GET {SPOOLMAN_URL}/api/v1/field/{entity_type}/{field_key}
   - HTTP 200 → field exists, skip
   - HTTP 404 → field missing, proceed to step 2
   - Other error → log warning, mark field as failed, continue to next field

2. POST {SPOOLMAN_URL}/api/v1/field/{entity_type}/{field_key}
   Body: {"name": "{display_name}", "field_type": "{type}", "default_value": "{json_encoded_default}"}
   - HTTP 201 → created successfully
   - HTTP 409 → already exists (race/concurrent call), treat as success
   - Other error → log warning, mark field as failed
```

#### Spoolman Field API — confirmed behaviour

**Endpoint:** `POST /api/v1/field/{entity_type}/{field_key}`

- `entity_type` is one of: `spool`, `filament`, `vendor`
- `field_key` is the programmatic key (e.g. `nfc_spool_uuid`) — also used as the URL path segment

**Body fields:**

| Field | Required | Notes |
| --- | --- | --- |
| `name` | yes | Display name shown in Spoolman UI |
| `field_type` | yes | `"text"`, `"integer"`, `"float"` confirmed. `"boolean"` likely exists but unconfirmed — test against your instance before using |
| `default_value` | yes | **Must be JSON-encoded inside the JSON body.** See encoding rules below |

**`default_value` encoding rules — this is the critical gotcha:**

The value must be a JSON-encoded string representing the JSON value. This means:

```go
// text field → default empty string
// The default_value JSON string contains: ""
defaultValue := `""`           // correct
defaultValue := ""             // WRONG — rejected by Spoolman

// integer field → default zero
// The default_value JSON string contains: 0
defaultValue := "0"            // correct
defaultValue := `"0"`          // WRONG — string not integer

// float field → default zero
defaultValue := "0.0"          // correct

// text field with a real default
// The default_value JSON string contains: "FFF"
defaultValue := `"FFF"`        // correct
```

In Go, build the POST body like this:

```go
type SpoolmanFieldCreate struct {
    Name         string `json:"name"`
    FieldType    string `json:"field_type"`
    DefaultValue string `json:"default_value"`
}

// text field with empty string default
field := SpoolmanFieldCreate{
    Name:         "NFC Spool UUID",
    FieldType:    "text",
    DefaultValue: `""`,
}

// integer field with zero default
field := SpoolmanFieldCreate{
    Name:         "NFC Min Print Temp",
    FieldType:    "integer",
    DefaultValue: "0",
}
```

**Setting a custom field value on an existing spool or filament:**

```text
PATCH /api/v1/spool/{id}
Body: {"extra": {"nfc_spool_uuid": "\"some-uuid-value\""}}
```

The value in the `extra` map must also be JSON-encoded. A text value `"abc"` is sent as `"\"abc\""`.

In Go:

```go
extraValue, _ := json.Marshal("some-uuid-value")  // produces: "some-uuid-value" with quotes
body := map[string]interface{}{
    "extra": map[string]string{
        "nfc_spool_uuid": string(extraValue),
    },
}
```

#### Required Spoolman Custom Fields

| Key | Display Name | Type | Entity | Default | Description |
| --- | --- | --- | --- | --- | --- |
| `nfc_material_class` | NFC Material Class | text | Filament | `""` | `"FFF"` or `"SLA"` |
| `nfc_min_print_temp` | NFC Min Print Temp | integer | Filament | `0` | Min nozzle °C |
| `nfc_max_print_temp` | NFC Max Print Temp | integer | Filament | `0` | Max nozzle °C |
| `nfc_min_bed_temp` | NFC Min Bed Temp | integer | Filament | `0` | Min bed °C |
| `nfc_max_bed_temp` | NFC Max Bed Temp | integer | Filament | `0` | Max bed °C |
| `nfc_country_of_origin` | NFC Country of Origin | text | Filament | `""` | ISO code e.g. `"CZ"` |
| `nfc_material_properties` | NFC Material Properties | text | Filament | `""` | JSON array e.g. `["abrasive","matte"]` |
| `nfc_transmission_distance` | NFC Transmission Distance | float | Filament | `0.0` | Opacity value for HueForge |
| `nfc_nominal_length` | NFC Nominal Length | integer | Filament | `0` | Length in mm |
| `nfc_spool_uuid` | NFC Spool UUID | text | Spool | `""` | UUID v4, unique per physical spool |
| `nfc_actual_weight` | NFC Actual Weight | float | Spool | `0.0` | Measured weight in grams |
| `nfc_manufacturing_date` | NFC Manufacturing Date | text | Spool | `""` | ISO date e.g. `"2025-09-15"` |
| `nfc_expiration_date` | NFC Expiration Date | text | Spool | `""` | ISO date |

#### Equivalent curl commands (for manual verification/testing)

```bash
# Filament fields
curl -X POST http://localhost:7912/api/v1/field/filament/nfc_material_class \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Material Class","field_type":"text","default_value":"\"\""}'

curl -X POST http://localhost:7912/api/v1/field/filament/nfc_min_print_temp \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Min Print Temp","field_type":"integer","default_value":"0"}'

curl -X POST http://localhost:7912/api/v1/field/filament/nfc_max_print_temp \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Max Print Temp","field_type":"integer","default_value":"0"}'

curl -X POST http://localhost:7912/api/v1/field/filament/nfc_min_bed_temp \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Min Bed Temp","field_type":"integer","default_value":"0"}'

curl -X POST http://localhost:7912/api/v1/field/filament/nfc_max_bed_temp \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Max Bed Temp","field_type":"integer","default_value":"0"}'

curl -X POST http://localhost:7912/api/v1/field/filament/nfc_country_of_origin \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Country of Origin","field_type":"text","default_value":"\"\""}'

curl -X POST http://localhost:7912/api/v1/field/filament/nfc_material_properties \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Material Properties","field_type":"text","default_value":"\"\""}'

curl -X POST http://localhost:7912/api/v1/field/filament/nfc_transmission_distance \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Transmission Distance","field_type":"float","default_value":"0.0"}'

curl -X POST http://localhost:7912/api/v1/field/filament/nfc_nominal_length \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Nominal Length","field_type":"integer","default_value":"0"}'

# Spool fields
curl -X POST http://localhost:7912/api/v1/field/spool/nfc_spool_uuid \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Spool UUID","field_type":"text","default_value":"\"\""}'

curl -X POST http://localhost:7912/api/v1/field/spool/nfc_actual_weight \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Actual Weight","field_type":"float","default_value":"0.0"}'

curl -X POST http://localhost:7912/api/v1/field/spool/nfc_manufacturing_date \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Manufacturing Date","field_type":"text","default_value":"\"\""}'

curl -X POST http://localhost:7912/api/v1/field/spool/nfc_expiration_date \
  -H "Content-Type: application/json" \
  -d '{"name":"NFC Expiration Date","field_type":"text","default_value":"\"\""}'
```

These curl commands are also useful for testing The Moment's setup logic — run them first to pre-populate fields, then verify The Moment's status check returns `ok: true`.

---

### UI: NFC & Spools Tab

Add a new tab to the main dashboard. It contains three sections:

#### Section 1: Spoolman Spool List

- Table: Spool ID, Filament Name, Vendor, Material, Colour (swatch), Remaining Weight, UUID, Actions
- Actions per row: "Generate Spool Tag" (downloads `.bin`), "View in Spoolman" (external link)
- Filter/search by material, vendor
- Shows setup warning if Spoolman custom fields are not created

#### Section 2: Current Toolhead Assignments

- One card per printer
- Each card shows toolhead slots with current spool assignment (name + colour swatch) or "Empty"
- "Assign" button per slot → opens assignment modal (spool picker)
- "Generate Location Tag" button per slot (downloads `.bin`)
- Real-time update via WebSocket when assignments change

#### Section 3: Spool Assignment History

- Recent assignment/swap log: when, which printer, which toolhead, which spool, reason

---

### Mobile NFC Web Pages

These pages are opened when an iPhone scans a tag. Keep them simple — minimal CSS, no heavy JS.

**`/nfc/location/:printer_slug/:toolhead_index`**

```text
┌─────────────────────────────┐
│  🖨 Core One L              │
│  Toolhead 0                  │
│                              │
│  Assign a spool:             │
│  ┌──────────────────────┐   │
│  │ 🟠 Prusament PLA     │   │
│  │ Orange · 743g left   │   │
│  └──────────────────────┘   │
│  ┌──────────────────────┐   │
│  │ ⬛ eSUN PETG         │   │
│  │ Black · 1000g left   │   │
│  └──────────────────────┘   │
│  [Cancel]                    │
└─────────────────────────────┘
```

Tapping a spool POSTs the assignment and shows a success screen.

**`/nfc/spool/:spoolman_id`**

```text
┌─────────────────────────────┐
│  🟠 Prusament PLA Orange    │
│  743g remaining              │
│                              │
│  Currently: Core One L T0   │
│                              │
│  Assign to:                  │
│  [ Core One L — Toolhead 0 ]│
│  [ Ender 3 — Toolhead 0    ]│
│                              │
│  [View in Spoolman]          │
└─────────────────────────────┘
```

---

### Go Module Dependencies to Add

```text
github.com/google/uuid   — UUID v4 generation for nfc_spool_uuid
```

Check `go.mod` first — `github.com/google/uuid` may already be present.

`github.com/fxamacker/cbor/v2` is **not needed in Phase 1**. Add it in Phase 2 when CBOR encoding is implemented.

---

### Coding Patterns to Follow

These patterns are already established in the codebase. Follow them exactly.

**Error handling:**

```go
if err != nil {
    c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
    return
}
```

**Named return variables:** Do not use named return variables in functions — they caused bugs in `ProcessVirtualFile`. Use explicit `return value, err` instead.

**Database migrations:** Add new tables in `database.go` inside the existing migration function. Use `IF NOT EXISTS`. Run migrations at startup.

**Config access:** Access printer configs via the existing `LoadConfig()` pattern. Do not cache config in package-level variables.

**WebSocket broadcast:** Use the existing WebSocket hub to push real-time updates to the frontend when assignments change.

**Printer slug:** Derive from printer name — lowercase, spaces replaced with hyphens, non-alphanumeric characters removed. Add a helper `PrinterSlug(name string) string`.

**SQLite DATETIME timestamp comparisons — use `julianday()`:** `DATETIME` columns have NUMERIC affinity. Direct TEXT comparison of two same-format ISO 8601 timestamps (e.g. `"2026-05-16T12:14:05.321069Z"`) produces incorrect results with go-sqlite3 v1.14.x — all rows evaluate as `<= pivot` regardless of actual value. Always wrap both sides in `julianday()` for any WHERE clause that compares stored timestamps:

```sql
-- WRONG — broken with go-sqlite3 DATETIME columns
AND assigned_at <= ?
AND (unassigned_at IS NULL OR unassigned_at > ?)

-- CORRECT
AND julianday(assigned_at) <= julianday(?)
AND (unassigned_at IS NULL OR julianday(unassigned_at) > julianday(?))
```

Pass the parameter as `atTime.UTC().Format(time.RFC3339Nano)`. SQLite's `julianday()` accepts the `Z` UTC suffix. This applies to any new query comparing `assigned_at`, `unassigned_at`, or any other `DATETIME` column against a Go `time.Time`-derived value.

---

### OpenPrintTag Specification Reference (Phase 2)

This section is reference material for Phase 2. Do not implement CBOR encoding in Phase 1.

- Full spec: `https://specs.openprinttag.org`
- GitHub: `https://github.com/OpenPrintTag/openprinttag-specification`
- NDEF MIME type: `application/vnd.openprinttag`
- Tag hardware needed for Phase 2: ICODE SLIX2 (ISO 15693 / NFC-V)
- Encoding: CBOR (RFC 8949), not JSON

**Do not invent CBOR field keys.** Only use field keys confirmed in the specification. If a field key is unknown, omit that field rather than guessing.

---

### What Is Out of Scope for Phase 1

- **OpenPrintTag CBOR tag generation** — deferred to Phase 2 (see below); Spoolman data is maintained but not yet written to tags as CBOR
- Automatic detection of spool runout via PrusaLink (PrusaLink API does not expose this)
- Mid-print M600 filament swap detection (not detectable via PrusaLink API)
- INDX 8-head printer integration (deferred until hardware is available)
- Import of NFC tag data from external sources (export only)
- Cloud sync or remote access outside LAN VPN

---

### Implementation Order (Phase 1)

Build in this order to allow incremental testing:

1. `spoolman.go` — add `GetSpoolmanSpool`, `GetSpoolmanSpools`, `UseSpoolmanFilament`, `SetSpoolExtraField`
2. `database.go` — add new tables and helper functions
3. `nfc.go` — URL-only NDEF binary generation for spool tags and location tags
4. `nfc_routes.go` — API endpoints (start with `/api/nfc/spools` and `/api/nfc/spool-tag/:id`)
5. Mobile NFC web pages (`/nfc/location/...` and `/nfc/spool/...`)
6. Spoolman setup check/create endpoints (`EnsureSpoolmanFields`)
7. Print start/end hooks in `monitor.go`
8. Print history detail enhancements in `history.go`
9. NFC & Spools tab in dashboard frontend

---

### Testing Checklist (Phase 1)

Before marking any step complete:

- [ ] All new API endpoints return correct HTTP status codes for missing resources (404), bad input (400), and server errors (500)
- [ ] Spool tag `.bin` output can be written to an NTAG213 via NFC Tools "Write Dump" and scanning it on iPhone opens the correct Moment page
- [ ] Location tag `.bin` output can be written to an NTAG213 via NFC Tools "Write Dump" and scanning it on iPhone opens the correct Moment page
- [ ] Scanning a location tag URL on iPhone opens the correct mobile page with printer and toolhead context
- [ ] Scanning a spool tag URL on iPhone opens the correct spool detail page with remaining weight and current assignment
- [ ] Assignment of a spool closes any previous open assignment for that slot
- [ ] `SnapshotAssignmentsForPrint` correctly records start state before any swap events
- [ ] `UseSpoolmanFilament` correctly updates Spoolman and does not double-count usage
- [ ] `EnsureSpoolmanFields` creates all required custom fields and is idempotent (safe to run twice)
- [ ] Virtual printers are excluded from assignment display (they do not have physical toolheads)
- [ ] All new DB operations are wrapped in proper error handling — no silent failures

---

## Phase 2: OpenPrintTag CBOR Tag Generation

**Trigger:** INBXX Semi-Smart V2 ships with NFC readers in the spool holder slots (targeted Q3 2026).

### Why This Is Phase 2, Not Phase 1

The Core One L has a planned built-in NFC reader that sits under the printer's own spool holder. When spools live in an INBXX enclosure (on top of or beside the printer and feeding filament via PTFE tubes), the printer's reader never sees them — the physical positions are incompatible.

INBXX Semi-Smart V2 will have NFC readers built into the spool holder slots inside the INBXX cabinet itself. That is the hardware that will read and write spool tags in this setup, not the Core One L's reader.

Until INBXX Semi-Smart V2 ships, all tag reading and writing happens via iPhone. URL-only tags are sufficient for that workflow.

### What Changes in Phase 2

**Tag hardware:** Replace NTAG213 spool tags with ICODE SLIX2 (ISO 15693 / NFC-V). These have a longer read range (needed for hardware readers to detect spools without precise alignment) and are the tag type INBXX will target.

**Tag format:** Spool tags gain a second NDEF record. The message becomes:

- Record 1: MIME type `application/vnd.openprinttag`, payload = OpenPrintTag CBOR binary
- Record 2: URI → `http://{MOMENT_HOST}/nfc/spool/{spoolman-spool-id}` (unchanged from Phase 1)

Location tags remain NTAG213 URL-only — INBXX does not read location tags.

**New work in `nfc.go`:**

- `SpoolToOpenPrintTag(spool SpoolmanSpool) (OpenPrintTagData, error)` — maps Spoolman spool + `nfc_*` custom fields to OpenPrintTag data structure
- `EncodeOpenPrintTagCBOR(data OpenPrintTagData) ([]byte, error)` — encodes as CBOR per spec
- Update `BuildSpoolTagNDEF` to accept optional CBOR bytes and produce a dual-record message

**New Go dependency:** `github.com/fxamacker/cbor/v2`

**OpenPrintTag field mapping** (Spoolman → CBOR):

| OpenPrintTag Field | Source |
| --- | --- |
| `materialName` | `filament.name` |
| `brandName` | `filament.vendor.name` |
| `materialType` | `filament.material` (integer enum per spec) |
| `materialClass` | custom field `nfc_material_class` |
| `primaryColor` | `filament.color_hex` |
| `secondaryColors` | `filament.multi_color_hexes` (comma-separated) |
| `filamentDiameter` | `filament.diameter` |
| `nominalWeight` | `filament.weight` |
| `emptyContainerWeight` | `filament.spool_weight` |
| `consumedWeight` | `spool.used_weight` |
| `nominalLength` | custom field `nfc_nominal_length` |
| `minPrintTemp` | custom field `nfc_min_print_temp` |
| `maxPrintTemp` | custom field `nfc_max_print_temp` |
| `minBedTemp` | custom field `nfc_min_bed_temp` |
| `maxBedTemp` | custom field `nfc_max_bed_temp` |
| `manufacturingDate` | custom field `nfc_manufacturing_date` |
| `expirationDate` | custom field `nfc_expiration_date` |
| `countryOfOrigin` | custom field `nfc_country_of_origin` |
| `spoolUUID` | custom field `nfc_spool_uuid` |
| `materialProperties` | custom field `nfc_material_properties` (JSON array) |

**Data flow when INBXX reads a tag:**

1. INBXX reads CBOR record → extracts `nfc_spool_uuid`
2. INBXX calls Spoolman `GET /api/v1/spool?extra[nfc_spool_uuid]={uuid}` to find the spool
3. INBXX sets that spool as active for the slot that was scanned
4. The Moment reads the active assignment from Spoolman when a print starts

Alternatively (Option B — more control):

1. INBXX POSTs to The Moment's `/api/nfc/inbxx-scan` webhook with slot number and UUID
2. The Moment maps the INBXX slot to a toolhead index and updates assignments directly

The integration approach depends on what API INBXX Semi-Smart V2 exposes. Evaluate when hardware is available.

### What Does NOT Change in Phase 2

- Spoolman custom fields — already created and populated in Phase 1
- The URL record in the spool tag — remains identical, so iPhone workflow is unaffected
- Location tags — unchanged
- All existing API endpoints — unchanged
- Database schema — unchanged
- The Moment's role as Spoolman sync layer — unchanged
