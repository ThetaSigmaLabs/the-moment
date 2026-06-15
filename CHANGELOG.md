# Changelog

All notable changes to The Moment will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.1.0] — 2026-06-15

### Added

#### OpenPrintTag Integration

- External filament database sources — new Settings → Open Print Tag tab
- OFD API adapter: search Open Filament Database by brand, material, name; fetch colour variants
- filament-db adapter: connect to self-hosted filament-db instances
- TigerTag temperature fallback when OFD returns zero temperatures (24-hour cache)
- OpenPrintTag subtab in Add Filament dialog: search → select variant → create NFC tag in one workflow
- Smart Spoolman matching: fuzzy-match OPT results against existing filaments; offer update or create new
- Source connectivity test button with latency display (ms)
- 8 new `/api/openprinttag/*` API endpoints (sources CRUD, search, variants)
- `POST /api/nfc/openprinttag-tag` — create filament tag from external source data
- `openprinttag_sources` DB table with seed defaults (OFD enabled, filament-db disabled)

#### NFC Management

- OpenPrintTag mode in Add Filament dialog (fourth mode alongside link/author/unbound)
- Colour variant picker with hex swatches and name labels
- Temperature ranges and material density displayed during colour selection
- Write `nfc_*` custom fields (temperatures, material class) to Spoolman filament on tag creation
- Filament update path: populate colour, temperature, density, weight from external sources on existing Spoolman filaments

#### UI & UX

- Source-aware search placeholder hints adapt to selected adapter type
- 400 ms debounce on OPT search to reduce unnecessary external API calls
- Add Filament modal state fully reset on reopen (search, variants, selection, matched filament)
- Settings → Open Print Tag tab: enable/disable sources, add custom sources, reset to defaults

### Fixed

- Spoolman API: accept both `200 OK` and `201 Created` on filament/spool POST — fixes compatibility with Spoolman version variance (affected `CloneFilament`, `CreateFilament`, `CreateSpool`)

### Changed

- Bambu printer support disabled in UI pending hardware testing — MQTT implementation is complete in code; re-enablement documented in ROADMAP.md and CLAUDE.md

## [v1.0.0] - 2026-06-09

First public release. Forked from FilaBridge v0.3.0; feature-complete and running in production on Odroid N2+ with Prusa CORE One L, Ender 3 V3 SE, and Spoolman. All items below are additions or changes relative to FilaBridge v0.3.0.

### Added

#### NFC & Physical Spool Workflow

- **NFC Phase 1** — generate NDEF `.bin` files for spool tags (URL → `/nfc/spool/{id}`) and location tags (URL → `/nfc/location/{slug}/{index}`); write via NFC Tools Pro on iPhone
- **OpenPrintTag CBOR** — spool tags encode a full OpenPrintTag-compatible CBOR record (materialName, brandName, temperatures, color, weight, UUID, manufacturing/expiry dates, material properties)
- **Spoolman location sync** — bidirectional sync between toolhead assignments and Spoolman spool `location` field; configurable 5-minute poll plus immediate sync on tab open; toggle in Settings → Advanced
- **Spoolman custom fields** — auto-registers 14 `nfc_*` custom fields on startup (temperatures, UUID, weight, dates, country of origin, material properties, transmission distance)
- **QR code generation** — QR codes for spools, filament types, and locations generated alongside NFC `.bin` files

#### Multi-Printer & Toolhead Support

- **Bambu MQTT support** — connects via MQTT over TLS; AMS slots map to toolhead indices; X1C, P1S, A1, A1 Mini (beta, LAN-only)
- **OctoPrint plugin** — push-based events with per-tool filament usage and spool IDs; authentication optional
- **Multi-toolhead session tracking** — all per-tool rows for one print share a `session_id`; history groups them as a single logical job
- **Filament-change tracking** — mid-print spool swaps recorded as separate `change_number` entries
- **Per-printer cost overrides** — wattage, preheat charge, high-temp extra wattage, and depreciation rate all configurable per printer
- **Printer card** — new visual printer card UI in settings for at-a-glance printer status

#### Print History & Camera

- **Print history thumbnails** — G-code thumbnails extracted and stored; displayed in history view
- **Camera snapshots** — configurable interval snapshots during active prints; stored and browsable in print history
- **Snapshot lightbox navigation** — prev/next arrows (‹ ›) step through the snapshot timeline; position indicator shows `3/12 · 25%`; keyboard arrows navigate, ESC closes; works in both history and active print modal
- **Print rename** — rename any history entry post-print
- **Cancelled/failed prints** — generate a cost record and history entry (no longer silently dropped)
- **Print notes, tags, and file attachments** — add freeform notes, custom tags, and upload files to any history entry

#### Cost Engine

- **High-temp material detection** — automatically adds extra wattage cost for ABS, ASA, PA, PC materials identified by Spoolman
- **Pre-print cost fields** — configurable preheat charge and per-printer cost parameters exposed in UI
- **Enhanced cost breakdown** — bgcode thumbnail and weight extraction support
- **Batch cost recalculation** — recalculate costs across selected historical entries when settings change
- **Quick cost calculator** — test cost settings with arbitrary weight and duration without hardware

#### Filament Management

- **Filament calibration tab** — inline weight editing with real-time Spoolman sync
- **OpenPrintTag filament edit dialogues** — edit OpenPrintTag/NFC custom fields (temperatures, dates, material properties) directly from the filament dialog
- **Filament sufficiency warning** — pre-print alert when remaining spool weight may not be enough for the job
- **Filament check indicator** — pre-print check badge on toolhead assignment view
- **Spool trash workflow** — archived spools returned to configured inventory location in Spoolman
- **Negative spool weight support** — negative remaining weight treated as valid; never filtered from assignment lists or dropdowns

#### UI & Operations

- **Purple UI redesign** — new look with improved dashboard layout and print ops organization
- **Real-time WebSocket dashboard** — live printer status and toolhead assignments without page refresh
- **In-app backup/restore/migration** — export full database as JSON; validate integrity before restore; import on another instance
- **Virtual printer import/export** — export a virtual printer with its G-code library as JSON; import on another instance
- **About tab** — version badge links to GitHub changelog; clickable from the main UI
- **Lost spool indicator** — flag orphaned toolhead assignments where the mapped spool no longer exists

### Fixed

- **OctoPrint double-deduction** — `LogOctoPrintRecord` was writing to `pending_spoolman_updates` causing Spoolman to subtract used weight twice when the OctoPrint-Spoolman plugin was also active; removed the write; responsibility boundary is now clean
- **SQLite DATETIME comparison** — wrapped all timestamp comparisons in `julianday()` to fix incorrect ordering with `modernc.org/sqlite` v1.14.x NUMERIC affinity
- **PrusaLink thumbnail extraction** — CORE One L firmware variants now extract correctly
- **PrusaLink `/transfer` endpoint** — missing route added; transfer requests no longer 404
- **PrusaLink JSON parsing** — printer status JSON edge cases fixed for connectivity reliability
- **NFC URL host detection** — uses `c.Request.Host` (adapts across LAN, hostname, and VPN access); no static IP configuration required
- **Dashboard stats** — calculation edge cases fixed for accurate in-progress and completed counts
- **`[RECOVERED]` stub deduplication** — prevents duplicate recovery stubs across dev restarts during an active print
- **Pre-print cost field validation** — cost fields now persist and display correctly before print starts
- **Backup/restore spinners** — UI now shows loading state during backup creation and restore preflight
- **Mobile layout** — printer view layout and dialog scrolling fixed for mobile browsers

### Changed

- Module path updated to `github.com/ThetaSigmaLabs/the-moment`
- Deployment uses bind-mount data directories (not Docker volumes); data lives on host filesystem
- Jenkins CI pipeline: 3-phase local pipeline covering linux-arm64 and windows/amd64
- GitHub Actions: multi-arch Docker images (amd64 + arm64) published to GHCR on release; tested on Prusa CORE One L in production
- ROADMAP.md added outlining planned features

## [v0.3.0] - 2026-04-18

### Changed

- Fork FilaBridge (needo37/filabridge) as The Moment under new maintainer
- Rename project, update module path, env vars, binary name, OctoPrint plugin, and virtual printer

### Fixed

- OctoPrint Spoolman double-deduction: removed `pending_spoolman_updates` writes from `LogOctoPrintRecord`; added `TestLogOctoPrintRecord_NoSpoolmanUpdate` to prevent regression

## [v0.2.4] - 2025-12-08

### Fixed

- enhance error logging and improve DNS resolution timeout for PrusaLink client

## [v0.2.3.1] - 2025-12-05

### Changed

- update Dockerfile to use --no-scripts flag for apk to address Alpine 3.23 trigger script issues

## [v0.2.3] - 2025-12-05

### Changed

- update Dockerfile to include apk update before installing dependencies

## [v0.2.2] - 2025-12-05

### Added

- add URL copy functionality and properly encode NFC URLs.

### Fixed

- Update location management to reflect API limitations

### Changed

- migrate location management from The Moment to Spoolman, removing legacy location functions and updating related API endpoints

## [v0.2.1] - 2025-12-03

### Added

- accept hostnames or IP addresses for Spoolman and Printers.

## [v0.2] - 2025-12-03

### Added

- enhance settings UI with sub-tabs for better organization and add functionality for automatic spool assignment with location selection
- implement auto-assignment of previous spools with configuration options and API endpoints
- add toolhead name management with custom display names and API endpoints for retrieval and updates

### Fixed

- add HTML escaping for toolhead names to prevent XSS vulnerabilities
- handle null values for remaining weight in spool display across dropdowns and NFC tags
- identify and skip virtual printer toolhead locations in location management
- round remaining weight in spool tag details for improved display

### Changed

- improve event listener management for auto-assign previous spool checkbox

## [v0.1.5] - 2025-11-18

### Added

- embed static files into the binary and update routing to serve them

### Changed

- refactor CHANGELOG generation in release workflow to use printf for header and new entry creation

## [v0.1.3] - 2025-11-02

### Fixed

- implement error ID sanitization for URL safety in print error handling

## [v0.1.2] - 2025-10-21

### Fixed

- add copying of static files in Dockerfile to streamline asset deployment

## [v0.1.1] - 2025-10-21

### Added

- add static files directory to Dockerfile for improved asset management

### Changed

- update CHANGELOG and enhance README with additional screenshots

## [v0.1.0] - 2025-10-21

### Added

- implement NFC management features including QR code generation and location tracking

## [v0.0.15] - 2025-10-20

### Added

- add edit button for spools
- filter out spools with 0g remaining weight in GetAllSpools method

### Changed

- enhance changelog generation to categorize commits by type

## [v0.0.14] - 2025-10-15

### Added

- fix: properly encode error ID in fetch request for acknowledging print errors
- feat: add local time conversion for error timestamps in print processing notifications
- chore(release): update CHANGELOG for v0.0.13, removing outdated v0.0.11 entry
- fix: enhance print processing logic in FilamentBridge to prevent duplicate handling and improve state management
- chore(release): update changelog for v0.0.13

### Added

- bug: streamline print completion handling in monitorPrusaLink, removing files/jobs being processed duplicate times.
- fix: reduce Spoolman timeout from 30 seconds to 10 seconds for improved performance
- chore(release): update changelog for v0.0.12

## [v0.0.12] - 2025-10-14

### Added

- bug: fix not being able to dismiss error messages
- docs: Update README to use direct link for dashboard screenshot, improving accessibility
- chore(release): enhance CHANGELOG generation by categorizing commits and improving file handling
- chore(release): update changelog for v0.0.11

### Added

- feat: Add advanced timeout settings for PrusaLink and Spoolman API, enhancing configuration flexibility in the UI
- chore(release): update changelog for v0.0.10
