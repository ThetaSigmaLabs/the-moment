# The Moment

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Version](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go)](https://golang.org/)
[![GitHub release](https://img.shields.io/github/v/release/ThetaSigmaLabs/the-moment)](https://github.com/ThetaSigmaLabs/the-moment/releases)

A high-performance Go microservice that bridges your 3D printers and [Spoolman](https://github.com/Donkie/Spoolman) for automatic filament inventory management, complete print history, and per-print cost tracking across all your printers.

The Moment is derived from [FilaBridge](https://github.com/needo37/filabridge) by [needo37](https://github.com/needo37). This fork adds a full print history, cost accounting, OctoPrint plugin support, virtual test printers, per-printer cost overrides, session tracking, and significantly expands the test suite.

## The Problem

Running multiple 3D printers with Spoolman means manually updating filament usage after every print. With multi-material prints on a Prusa XL this is tedious and error-prone, and you have no record of what it cost or how long it took.

## Features

### Printer Support

- 🔗 **PrusaLink**: Works with any PrusaLink-compatible printer (Prusa CORE One, XL, MK4, Mini, and more)
- 🐙 **OctoPrint**: Plugin pushes print events from any OctoPrint-managed printer directly to The Moment
- 🧪 **Virtual Test Printers**: Upload G-code files and simulate prints to validate Spoolman integration without hardware

### Filament & Inventory

- 🎯 **Multi-Toolhead Support**: Seamlessly handles single and multi-toolhead printers (tested with 5-toolhead Prusa XL)
- 📈 **Smart Usage Tracking**: Automatically parses G-code files to accurately track filament consumption per toolhead
- 🔄 **Filament-Change Tracking**: Records each spool swap mid-print as a separate `change_number` entry in history
- 🔍 **Smart Spool Search**: Search and filter spools by ID, material, brand, or name with real-time filtering
- 📍 **Location Tracking**: Track spools in custom locations (dryboxes, shelves) or printer toolheads

### Cost Accounting

- 💰 **Full Cost Breakdown**: Filament cost (from Spoolman), electricity, maintenance, depreciation, and margin
- 🖨️ **Per-Printer Overrides**: Each printer can have its own wattage, preheat charge, high-temp extra wattage, and depreciation rate
- 🌡️ **High-Temp Detection**: Automatically adds extra wattage cost when Spoolman identifies the material as ABS, ASA, PA, or PC
- 🔥 **Preheat Accounting**: One-time electricity charge per print for bed/hotend warmup
- 🧮 **Quick Calculator**: Test your cost settings with arbitrary filament and time values without printing
- 💱 **Currency Support**: Configurable ISO currency code (USD, CAD, EUR, GBP, etc.)

### Print History

- 📋 **Full History**: Every completed print — from PrusaLink, OctoPrint, or virtual printers — is stored with filament used, print time, cost breakdown, and source
- 🔖 **Session Grouping**: Multi-toolhead prints share a `session_id` so all per-tool rows appear as one logical job
- 📝 **Notes**: Add freeform notes to any history entry after the fact
- 🗑️ **Delete Entries**: Remove individual history records
- 🖼️ **Thumbnails**: Gcode thumbnails are stored and displayed in history (where available)

### Infrastructure & Management

- 💾 **Persistent Storage**: SQLite database — no external DB required
- ⚡ **High Performance**: Single lightweight binary, minimal resource usage
- 🌐 **Real-time Dashboard**: Web interface with live updates via WebSocket
- 🔧 **Web-based Config**: No config files needed — manage everything through the web UI
- ⚠️ **Error Handling**: Print error detection with acknowledgment system
- 🔓 **Stuck Spool Cleanup**: Detect and release spool assignments orphaned by deleted printers
- 📥 **Virtual Printer Import/Export**: Export a virtual printer (with its G-code library) to JSON; import it on another instance
- 🏷️ **NFC / QR Codes**: Generate QR codes and program NFC tags for spools, filaments, and locations
- 📱 **Two-step NFC Workflow**: Tap spool then location (or location then spool) for instant assignment

## Screenshots

![The Moment Dashboard](https://github.com/ThetaSigmaLabs/the-moment/blob/main/.github/screenshots/dashboard.png?raw=true)
*Main dashboard showing printer status and toolhead mappings*

![Spool Tags Management](https://github.com/ThetaSigmaLabs/the-moment/blob/main/.github/screenshots/spool_tags.png?raw=true)
*NFC Management interface for generating QR codes for individual spools*

![Filament Tags Management](https://github.com/ThetaSigmaLabs/the-moment/blob/main/.github/screenshots/filament_tags.png?raw=true)
*Filament type QR code generation for new unopened spools*

![Location Tags Management](https://github.com/ThetaSigmaLabs/the-moment/blob/main/.github/screenshots/location_tags.png?raw=true)
*Location management interface for creating printer toolhead and storage location QR codes*

## Prerequisites

- A PrusaLink-compatible 3D printer **and/or** an OctoPrint instance
- Spoolman running and reachable on your network
- **For building from source**: Go 1.23 or higher
- **(Optional) For NFC features**: NFC-capable smartphone and NFC tags (NTAG213/215/216 recommended)
- **(Recommendation) NFC Tools Pro** mobile app (for programming tags)

## Installation

### Option 1: Docker (Easiest)

1. **Run Spoolman** (if not already running):

   ```bash
   docker run -d --name spoolman -p 8000:8000 \
     -v spoolman-data:/home/spoolman/data \
     ghcr.io/donkie/spoolman:latest
   ```

2. **Run The Moment**:

   ```bash
   docker run -d --name the-moment -p 5000:5000 \
     -v .:/app/data \
     ghcr.io/thetasigmalabs/the-moment:latest
   ```

3. **Configure**: Open `http://localhost:5000` → Settings → Printers → Add Printer

**Using docker-compose (recommended for full stack):**

```bash
git clone https://github.com/ThetaSigmaLabs/the-moment.git
cd the-moment
docker-compose up -d
```

The `docker-compose.yml` sets `THE_MOMENT_DB_PATH=/app/data` so the database persists in the mounted volume.

### Option 2: Pre-built Binary

1. Download the latest release for your platform from the [Releases page](https://github.com/ThetaSigmaLabs/the-moment/releases):
   - Linux (amd64, arm64)
   - macOS (amd64/Intel, arm64/Apple Silicon)
   - Windows (amd64)

2. Make it executable (Linux/macOS):

   ```bash
   chmod +x the-moment
   ```

3. Start Spoolman (if not already running) — see Step 1 above.

4. Start The Moment:

   ```bash
   ./the-moment
   ```

5. Open `http://localhost:5000` and configure via Settings.

### Option 3: Build from Source

```bash
git clone https://github.com/ThetaSigmaLabs/the-moment.git
cd the-moment
go mod download
go build -o the-moment .
./the-moment
```

## OctoPrint Plugin

The Moment ships an OctoPrint plugin (`octoprint-plugin/`) that pushes print events to The Moment API so non-Prusa printers share the same history and cost tracking.

**What the plugin sends:**

- Print start, finish, cancel, and fail events
- Pause events with timestamps and reasons
- Per-tool filament usage (split by spool when a filament change occurs mid-print)
- Spool IDs from OctoPrint-SpoolManager or Spoolman plugin (optional)
- A UUID `session_id` so all tool rows for one job group correctly in history

**Plugin setup:**

1. Copy or install the plugin from `octoprint-plugin/`
2. In OctoPrint Settings → **The Moment**, set:
   - **URL**: `http://<your-the-moment-host>:5000`
   - **Printer ID**: a short identifier, e.g. `ender3-v3-se`
   - **API Key**: leave blank unless you've configured one in The Moment

## Configuration

All configuration lives in the SQLite database. For Docker, set `THE_MOMENT_DB_PATH` to control where the file is stored (defaults to `/app/data` in Docker, current directory otherwise).

### First Run

1. Open `http://localhost:5000`
2. Go to **Settings → Printers → Add Printer**
3. Enter a name, hostname/IP, and PrusaLink API key
4. Optionally set toolhead count and names
5. Go to **Settings → Cost Settings** to configure electricity rate, wattage, maintenance, depreciation, margin, and currency

## Usage

### Running the Service

```bash
# Default (0.0.0.0:5000)
./the-moment

# Custom host and port
./the-moment --host 127.0.0.1 --port 8080
```

### Web Interface Tabs

| Tab | Description |
| --- | --- |
| **Dashboard** | Live printer status, current jobs, toolhead spool assignments |
| **Filament Status** | Assign spools to toolheads; smart search by name, material, brand |
| **History** | Full print history with cost breakdown, notes, thumbnails; grouped by session |
| **NFC Tags** | Generate QR codes for spools, filament types, and locations |
| **Settings** | Printers, cost settings (global + per-printer), advanced timeouts, spool assignment |

### Virtual Test Printers

Virtual printers let you validate Spoolman integration and cost calculations without needing hardware:

1. **Settings → Printers → Add Virtual Test Printer**
2. Upload G-code files to the virtual printer
3. Click **Process** to simulate the print — filament usage is parsed from the G-code, Spoolman is updated, and a history entry is created with full cost breakdown
4. Export/import virtual printers (with their G-code libraries) as JSON for sharing or backup

### Cost Settings

**Global settings** (Settings → Cost Settings):

- Electricity rate ($/kWh)
- Default printer wattage (W)
- Maintenance rate ($/hour)
- Depreciation rate ($/hour)
- Profit margin (%)
- Currency (ISO code)

**Per-printer overrides** (Settings → Cost Settings → Per-Printer Overrides):

- Print wattage — overrides global default for this printer
- Preheat wattage + time — one-time electricity charge per print
- High-temp extra wattage — automatically applied when Spoolman material is ABS, ASA, PA, or PC
- Purchase cost + estimated lifespan — derives depreciation per hour
- Direct depreciation per hour — overrides the derived value

### NFC Tag Management

1. Go to the **NFC Tags** tab
2. Generate QR codes for spools, filament types, or locations
3. Scan the QR code with NFC Tools Pro to write the URL to an NFC tag
4. To assign: tap the spool tag, then the location tag (or location then spool) within 5 minutes

## API Reference

### Printers & Config

| Method | Endpoint | Description |
| --- | --- | --- |
| `GET` | `/api/status` | Current printer status and spool mappings |
| `GET` | `/api/printers` | List all configured printers |
| `POST` | `/api/printers` | Add a printer |
| `PUT` | `/api/printers/:id` | Update a printer |
| `DELETE` | `/api/printers/:id` | Delete a printer |
| `POST` | `/api/printers/virtual` | Add a virtual test printer |
| `GET` | `/api/printers/:id/export` | Export virtual printer to JSON |
| `POST` | `/api/printers/import` | Import virtual printer from JSON |
| `GET` | `/api/detect_printer` | Auto-detect printer type from URL |
| `GET` | `/api/config` | Get all config key/value pairs |
| `POST` | `/api/config` | Update config |

### Virtual Printer Files

| Method | Endpoint | Description |
| --- | --- | --- |
| `GET` | `/api/printers/:id/files` | List uploaded G-code files |
| `POST` | `/api/printers/:id/files` | Upload a G-code file |
| `DELETE` | `/api/printers/:id/files/:file_id` | Delete a file |
| `POST` | `/api/printers/:id/files/:file_id/process` | Simulate print and log history |
| `GET` | `/api/printers/:id/files/:file_id/download` | Download G-code |

### Filament & Spools

| Method | Endpoint | Description |
| --- | --- | --- |
| `GET` | `/api/spools` | All spools from Spoolman |
| `GET` | `/api/filaments` | All filament types from Spoolman |
| `GET` | `/api/available_spools` | Spools available for a toolhead |
| `POST` | `/api/map_toolhead` | Assign a spool to a toolhead |
| `GET` | `/api/orphaned-mappings` | Find spool assignments from deleted printers |
| `DELETE` | `/api/orphaned-mappings` | Release all orphaned spool assignments |

### History & Sessions

| Method | Endpoint | Description |
| --- | --- | --- |
| `GET` | `/api/history` | Full print history (flat, newest first) |
| `GET` | `/api/history/:id` | Single history entry with filament usage breakdown |
| `PATCH` | `/api/history/:id/note` | Add or update a note on a history entry |
| `DELETE` | `/api/history/:id` | Delete a history entry |
| `GET` | `/api/sessions` | Print history grouped by session |

### Cost

| Method | Endpoint | Description |
| --- | --- | --- |
| `GET` | `/api/cost-settings` | Get global cost settings |
| `POST` | `/api/cost-settings` | Save global cost settings |
| `GET` | `/api/cost-settings/printers` | Get all per-printer overrides |
| `GET` | `/api/printers/:id/cost-settings` | Get per-printer override |
| `POST` | `/api/printers/:id/cost-settings` | Save per-printer override |
| `POST` | `/api/cost/calculate` | Calculate cost for given filament/time/spool |

### OctoPrint Integration

| Method | Endpoint | Description |
| --- | --- | --- |
| `POST` | `/api/prints` | Receive a print record from the OctoPrint plugin |

### Print Errors, NFC & Locations

| Method | Endpoint | Description |
| --- | --- | --- |
| `GET` | `/api/print-errors` | Unacknowledged print errors |
| `POST` | `/api/print-errors/:id/acknowledge` | Acknowledge a print error |
| `GET` | `/api/nfc/assign` | Handle NFC tag scan |
| `GET` | `/api/nfc/urls` | All NFC URLs with QR codes |
| `GET` | `/api/nfc/session/status` | NFC session state |
| `GET` | `/api/locations` | All locations |
| `POST` | `/api/locations` | Create a location |
| `PUT` | `/api/locations/:name` | Rename a location |
| `DELETE` | `/api/locations/:name` | Delete a location |
| `WS` | `/ws/status` | WebSocket — real-time status updates |

## Project Structure

```text
the-moment/
├── main.go                          # Entry point, CLI flags, startup goroutines
├── bridge.go                        # Core monitoring, DB, business logic
├── web.go                           # HTTP server, all route handlers
├── nfc.go                           # NFC NDEF binary generation, session management
├── nfc_routes.go                    # HTTP handlers for NFC/spool assignment API
├── spoolman.go                      # Spoolman API client
├── prusalink.go                     # PrusaLink API client
├── cost.go                          # Cost calculation and per-printer overrides
├── config.go                        # Configuration management
├── constants.go                     # Application constants
├── version.go                       # Version string
├── templates/                       # HTML templates (dashboard, history, settings, NFC pages)
├── static/                          # CSS, JS, images
│   └── js/
│       ├── cost-calculator.js       # Cost settings UI and quick calculator
│       ├── history.js               # Print history and session grouping UI
│       └── nfc.js                   # NFC & spool assignment UI
├── octoprint-plugin/                # OctoPrint plugin source
│   └── octoprint_the_moment/
│       └── __init__.py
├── scripts/                         # Developer and packaging scripts
│   ├── package-octoprint-plugin.sh  # Packages the OctoPrint plugin as an installable zip
│   └── test_stack.sh                # Starts the full test stack (Spoolman + The Moment)
├── contrib/                         # Contributed configs for related tools
│   └── moonraker_spoolman.cfg       # Moonraker macro for Spoolman filament tracking
├── *_test.go                        # Unit tests (go test ./...)
├── *_integration_test.go            # Integration tests (go test -tags=integration ./...)
└── README.md
```

## Troubleshooting

### Printers not accessible

- Verify the IP/hostname in Settings → Printers
- Ensure PrusaLink is enabled on the printer
- Check network connectivity and firewall rules

### Spoolman connection failed

- Confirm Spoolman is running and accessible at the configured URL
- Use Settings → Basic Configuration → Test Connection

### Filament usage not tracked

- Confirm spools are mapped to toolheads before printing
- Check that prints are completing, not just pausing
- Verify PrusaLink is returning filament usage data (check logs)

### OctoPrint plugin not sending

- Confirm the URL in OctoPrint Settings → The Moment includes the correct host and port
- Check the OctoPrint log (`octoprint.log`) for connection errors
- Ensure The Moment is reachable from the OctoPrint host

### WebSocket connection issues

- Check the browser console for WebSocket errors
- The interface falls back to periodic polling if WebSocket fails
- Ensure no reverse proxy is stripping the `Upgrade` header

### Stuck spool assignments after deleting a printer

- Settings → Printers → Check for Stuck Assignments
- Release them with "Release All Stuck Spools"

### Logs

The service logs to stdout. Key events to look for:

- Printer status updates and job completions
- Filament usage calculations and Spoolman update confirmations
- Cost calculation results (including high-temp flag)
- WebSocket connection state
- Print processing errors

## Development

```bash
# Download dependencies
go mod download

# Build
go build -o the-moment .

# Run all tests
go test ./...

# Run with race detector
go run -race .

# Run specific test suites
go test ./... -v -run TestOctoPrint
go test ./... -v -run TestCost
go test ./... -v -run TestSession
```

## Contributing

Contributions are welcome!

- 🐛 **Report bugs**: Open an issue with details
- 💡 **Suggest features**: Share your ideas
- 🔧 **Submit PRs**: Fix bugs or add features (open an issue first for major changes)
- 📖 **Improve docs**: Help make documentation clearer
- ⭐ **Star the repo**: Show your support

## Roadmap

- [ ] Support for additional printer APIs
- [ ] Mobile-responsive UI improvements
- [x] OctoPrint plugin
- [x] Full print history with cost breakdown
- [x] Per-printer cost overrides
- [x] High-temp material detection
- [x] Session grouping for multi-toolhead prints
- [x] Filament-change tracking (`change_number`)
- [x] Virtual test printers with import/export
- [x] Stuck spool assignment cleanup
- [x] Docker image
- [x] Real-time WebSocket updates
- [x] NFC tag support

## Acknowledgments

The Moment is derived from [FilaBridge](https://github.com/needo37/filabridge) by [needo37](https://github.com/needo37), licensed under GPL v3. The core bridging logic, PrusaLink client, Spoolman integration, NFC workflow, and web interface originate from that project. This fork continues development under a new name and maintainer.

## License

The Moment is free software licensed under the GNU General Public License v3.0 — see the [LICENSE](LICENSE) file for details.

The Moment is derived from FilaBridge, Copyright (C) 2025 needo37. Both works are distributed under the same GPL v3 license.

## Support

- **PrusaLink issues**: Check Prusa's official documentation
- **Spoolman issues**: [Spoolman GitHub](https://github.com/Donkie/Spoolman)
- **This project**: Open an issue in this repository
