# Changelog

All notable changes to The Moment will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.0.0] - 2026-06-05

First public alpha release. Feature-complete and running in production on real hardware.

### Added

- **NFC Phase 1** — generate NDEF `.bin` files for spool tags (URL → `/nfc/spool/{id}`) and location tags (URL → `/nfc/location/{slug}/{index}`); write via NFC Tools Pro on iPhone
- **OpenPrintTag CBOR** — spool tags encode a full OpenPrintTag-compatible CBOR record (materialName, brandName, temperatures, color, weight, UUID, manufacturing/expiry dates, material properties)
- **Spoolman location sync** — bidirectional sync between toolhead assignments and Spoolman spool `location` field; configurable 5-minute poll plus immediate sync on tab open; toggle in Settings → Advanced
- **Spoolman custom fields** — auto-registers 14 `nfc_*` custom fields on startup (temperatures, UUID, weight, dates, country of origin, material properties, transmission distance)
- **Multi-toolhead session tracking** — all per-tool rows for one print share a `session_id`; history groups them as a single logical job
- **Print history thumbnails** — G-code thumbnails extracted and stored; displayed in history view
- **Filament-change tracking** — mid-print spool swaps recorded as separate `change_number` entries
- **Virtual printer import/export** — export a virtual printer with its G-code library as JSON; import on another instance
- **Per-printer cost overrides** — wattage, preheat charge, high-temp extra wattage, depreciation rate all configurable per printer
- **High-temp material detection** — automatically adds extra wattage cost for ABS, ASA, PA, PC materials
- **OctoPrint plugin** — pushes print start/finish/cancel/fail/pause events with per-tool filament usage and spool IDs
- **Bambu MQTT support** — connects via MQTT over TLS; AMS slots map to toolhead indices
- **Spool trash workflow** — archived spools returned to inventory location in Spoolman
- **QR code generation** — QR codes for spools, filament types, and locations alongside NFC `.bin` files
- **Real-time WebSocket dashboard** — live printer status and toolhead assignments without page refresh

### Fixed

- OctoPrint double-deduction bug: `LogOctoPrintRecord` was writing to `pending_spoolman_updates` causing Spoolman to subtract used weight twice when the OctoPrint-Spoolman plugin was also active. Removed the write; responsibility boundary is now clean (OctoPrint owns Spoolman inventory updates for OctoPrint prints)
- SQLite DATETIME comparison: wrapped all timestamp comparisons in `julianday()` to fix incorrect ordering with `modernc.org/sqlite` v1.14.x NUMERIC affinity
- PrusaLink thumbnail extraction for CORE One L firmware variants
- NFC URL host detection uses `c.Request.Host` (adapts across LAN, hostname, and VPN access) — no static IP configuration required

### Changed

- Module path updated to `github.com/ThetaSigmaLabs/the-moment`
- Deployment uses bind-mount data directories (not Docker volumes) so data lives on the host filesystem

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
