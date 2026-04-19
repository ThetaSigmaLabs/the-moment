# The Moment

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Version](https://img.shields.io/badge/Go-1.23-00ADD8?logo=go)](https://golang.org/)
[![GitHub release](https://img.shields.io/github/v/release/maudy2u/the-moment)](https://github.com/maudy2u/the-moment/releases)

A high-performance Go microservice that bridges PrusaLink-compatible printers and Spoolman for automatic filament inventory management. Originally designed for Prusa printers (CORE One, XL, MK4, etc.) but works with any printer that supports the PrusaLink API.

## The Problem

Running multiple 3D printers with Spoolman means manually updating filament usage after every print. With multi-material prints on a Prusa XL, this is tedious and error-prone.

## Features

- 🔗 **PrusaLink Compatibility**: Works with any PrusaLink-compatible printer (Prusa CORE One, XL, MK4, Mini, and more)
- 📊 **Real-time Dashboard**: Web interface with live updates via WebSocket connections
- 🎯 **Multi-Toolhead Support**: Seamlessly handles single and multi-toolhead printers (tested with 5-toolhead Prusa XL)
- 📈 **Smart Usage Tracking**: Automatically parses G-code files to accurately track filament consumption per toolhead
- 💾 **Persistent Storage**: SQLite database stores toolhead mappings and complete print history
- ⚡ **High Performance**: Single lightweight binary, minimal resource usage, fast execution
- 🔧 **Web-based Config**: No config files needed - manage everything through the web UI
- 🔍 **Smart Spool Search**: Search and filter spools by ID, material, brand, or name with real-time filtering
- ⚠️ **Error Handling**: Print error detection with acknowledgment system for failed filament tracking
- 🔄 **Auto-mapping**: Automatic spool assignment when selecting from dropdown menus
- 🌐 **Live Updates**: Real-time status updates without page refreshes using WebSocket technology
- 🏷️ **NFC Tag Support**: Generate QR codes and program NFC tags for spools, filaments, and locations
- 📱 **Smart Scanning**: Two-step NFC workflow - scan spool + location (or location + spool) for instant assignment
- 📍 **Location Tracking**: Track spools in custom locations (dryboxes) or printer toolheads

## Screenshots

![The Moment Dashboard](https://github.com/maudy2u/the-moment/blob/main/.github/screenshots/dashboard.png?raw=true)
*Main dashboard showing printer status and toolhead mappings*

![Spool Tags Management](https://github.com/maudy2u/the-moment/blob/main/.github/screenshots/spool_tags.png?raw=true)
*NFC Management interface for generating QR codes for individual spools*

![Filament Tags Management](https://github.com/maudy2u/the-moment/blob/main/.github/screenshots/filament_tags.png?raw=true)
*Filament type QR code generation for new unopened spools*

![Location Tags Management](https://github.com/maudy2u/the-moment/blob/main/.github/screenshots/location_tags.png?raw=true)
*Location management interface for creating printer toolhead and storage location QR codes*

## Prerequisites

- A PrusaLink-compatible 3D printer (Prusa or any printer with PrusaLink API)
- PrusaLink enabled on your printer(s) for local network access
- Spoolman
- **For building from source**: Go 1.23 or higher
- **(Optional) For NFC features**: NFC-capable smartphone and NFC tags (NTAG213/215/216 recommended)
- **(Recommendation) NFC Tools Pro** mobile app (for programming tags)

## Installation

### Option 1: Docker (Easiest)

1. **Run Spoolman** (if not already running):

   ```bash
   docker run -d --name spoolman -p 8000:8000 -v spoolman-data:/home/spoolman/data ghcr.io/donkie/spoolman:latest
   ```

2. **Run The Moment**:

   ```bash
   docker run -d --name the-moment -p 5000:5000 \
     -v .:/app/data \
     ghcr.io/maudy2u/the-moment:latest
   ```

3. **Configure**: Open `http://localhost:5000` and click "⚙️ Configuration"

**Using docker-compose (recommended for full stack):**

```bash
git clone https://github.com/maudy2u/the-moment.git
cd the-moment
docker-compose up -d
```

The docker-compose.yml automatically sets the `THE_MOMENT_DB_PATH` environment variable to `/app/data` to ensure the database persists in the mounted volume.

### Option 2: Pre-built Binary

1. **Download the latest release** for your platform from the [Releases page](https://github.com/maudy2u/the-moment/releases)
   - Linux (amd64, arm64)
   - macOS (amd64/Intel, arm64/Apple Silicon)
   - Windows (amd64)

2. **Make it executable** (Linux/macOS):

   ```bash
   chmod +x the-moment
   ```

3. **Run Spoolman** (if not already running):

   ```bash
   docker run -d --name spoolman -p 8000:8000 -v spoolman-data:/home/spoolman/data ghcr.io/donkie/spoolman:latest
   ```

4. **Start The Moment**:

   ```bash
   ./the-moment
   ```

5. **Configure**: Open `http://localhost:5000` and click "⚙️ Configuration"

### Option 3: Build from Source

1. **Clone and build**:

   ```bash
   git clone https://github.com/maudy2u/the-moment.git
   cd the-moment
   go mod download
   go build -o the-moment .
   ```

2. **Run Spoolman** (if not already running):

   ```bash
   docker run -d --name spoolman -p 8000:8000 -v spoolman-data:/home/spoolman/data ghcr.io/donkie/spoolman:latest
   ```

3. **Start The Moment**:

   ```bash
   ./the-moment
   ```

## Configuration

The system stores all configuration in the SQLite database. For Docker deployments, you can optionally set the `THE_MOMENT_DB_PATH` environment variable to specify where the database should be stored (defaults to `/app/data` in Docker).

### First Run

1. Start the application
2. Open the web interface at `http://localhost:5000`
3. Click "Start Configuration" button
4. Enter a name for your Printer.
5. Enter your PrusaLink IP Address and API key
6. Choose the number of toolheads your printer has.
7. Click "Save Configuration"
8. The service will automatically restart with new settings

## Usage

### Running the Service

```bash
# Run both bridge service and web interface (recommended)
./the-moment

# Custom host and port
./the-moment --host 0.0.0.0 --port 8080
```

### Web Interface

The web interface provides:

- **Printer Status**: Real-time view of printer states and current jobs with live WebSocket updates
- **Toolhead Mapping**: Assign filament spools to specific toolheads with smart search functionality
- **Progress Monitoring**: Visual progress bars for active prints
- **Live Updates**: Real-time status updates without page refreshes
- **Spool Search**: Search and filter spools by ID, material, brand, or name
- **Error Management**: View and acknowledge print processing errors
- **Auto-mapping**: Automatic spool assignment when selecting from dropdowns

### Filament Management

1. **Add spools to Spoolman**: Use Spoolman's web interface to add your filament spools
2. **Map spools to toolheads**: Use the web interface to assign spools with smart search
3. **Monitor usage**: The system automatically tracks and updates filament usage
4. **Handle errors**: Acknowledge any print processing errors that require manual intervention

### NFC Tag Management

1. **Generate QR Codes**: Navigate to NFC Management tab in the web interface
2. **Create Tags**:
   - **Spool Tags**: Generate QR codes for individual spools
   - **Filament Tags**: Generate QR codes for filament types (for new unopened spools)
   - **Location Tags**: Create and generate QR codes for printer toolheads and custom locations (dryboxes, storage shelves, etc.)
3. **Program NFC Tags**: Use NFC Tools Pro to scan QR codes and write URLs to NFC tags
4. **Assign Spools**: Tap spool tag, then location tag (location then spool works as well) to instantly assign and update inventory

## API Endpoints

- `GET /api/status` - Get current printer status and mappings
- `GET /api/spools` - Get all spools from Spoolman
- `POST /api/map_toolhead` - Map a spool to a toolhead
- `POST /api/unmap_toolhead` - Unmap a spool from a toolhead
- `GET /api/print-errors` - Get all unacknowledged print errors
- `POST /api/print-errors/{id}/acknowledge` - Acknowledge a print error
- `GET /api/nfc/assign` - Handle NFC tag scans (spool or location)
- `GET /api/nfc/urls` - Get all NFC URLs with QR codes
- `GET /api/nfc/session/status` - Check NFC session status
- `GET /api/locations` - Get all locations
- `POST /api/locations` - Create custom location
- `PUT /api/locations/{name}` - Rename location
- `DELETE /api/locations/{name}` - Delete location
- `WS /ws/status` - WebSocket endpoint for real-time status updates

## Project Structure

```text
the-moment/
├── main.go                 # Application entry point
├── bridge.go              # Core monitoring and tracking logic
├── config.go              # Configuration management
├── constants.go           # Application constants
├── nfc.go                 # NFC session management and tag handling
├── prusalink.go           # PrusaLink API client
├── spoolman.go            # Spoolman API client
├── web.go                 # HTTP server and web interface
├── templates/             # HTML templates
├── static/                # CSS, JS, and image assets
├── contrib/               # Example configs for related tools
├── scripts/               # Development and testing scripts
├── go.mod                 # Go module definition
└── README.md              # Documentation
```

## Troubleshooting

### Common Issues

1. **Printers not accessible**:
   - Check IP addresses in the web interface configuration
   - Ensure PrusaLink is enabled on your printers
   - Verify network connectivity

2. **Spoolman connection failed**:
   - Make sure Spoolman is running
   - Check the Spoolman URL in the web interface configuration
   - Verify Spoolman is accessible at the specified URL

3. **Filament usage not tracked**:
   - Ensure spools are mapped to toolheads
   - Check that prints are completing (not just pausing)
   - Verify PrusaLink API is returning filament usage data

4. **WebSocket connection issues**:
   - Check browser console for WebSocket connection errors
   - Ensure no firewall is blocking WebSocket connections
   - The interface will fall back to periodic polling if WebSocket fails

5. **Print processing errors**:
   - Check the error notifications in the web interface
   - Acknowledge errors after manually updating Spoolman
   - Review logs for detailed error information

6. **NFC tag issues**:
   - Ensure NFC tags are NTAG213, NTAG215, or NTAG216 format
   - Use NFC Tools Pro to verify tag is properly formatted
   - QR codes encode the full URL - scan with NFC Tools Pro to program tags
   - Sessions expire after 5 minutes - complete both scans within the timeout

### Logs

The service logs important events to the console. Look for:

- Printer status updates
- Filament usage calculations
- Spoolman update confirmations
- WebSocket connection status
- Print processing errors

## Development

### Building from Source

```bash
# Download dependencies
go mod download

# Build the application
go build -o the-moment .

# Run tests
go test ./...

# Run with race detection
go run -race .
```

## Contributing

Contributions are welcome! Here's how you can help:

- 🐛 **Report bugs**: Open an issue with details about the problem
- 💡 **Suggest features**: Share your ideas for improvements
- 🔧 **Submit PRs**: Fix bugs or add features (please open an issue first for major changes)
- 📖 **Improve docs**: Help make the documentation clearer
- ⭐ **Star the repo**: Show your support!

See [CONTRIBUTING.md](CONTRIBUTING.md) for detailed guidelines.

## Roadmap

- [ ] Support for additional printer APIs
- [x] Provide a Docker Image
- [x] Real-time WebSocket updates
- [x] Enhanced spool search functionality
- [x] Print error handling and acknowledgment
- [x] NFC Support
- [ ] Mobile-responsive UI improvements

## Acknowledgments

The Moment is derived from [FilaBridge](https://github.com/needo37/filabridge) by [needo37](https://github.com/needo37), licensed under GPL v3. The core bridging logic, PrusaLink client, Spoolman integration, NFC workflow, and web interface originate from that project. This fork continues development under a new name and maintainer.

## License

The Moment is free software licensed under the GNU General Public License v3.0 - see the [LICENSE](LICENSE) file for details.

The Moment is derived from FilaBridge, Copyright (C) 2025 needo37. Both works are distributed under the same GPL v3 license.

## Support

For issues specific to:

- **PrusaLink**: Check Prusa's documentation
- **Spoolman**: Visit the [Spoolman GitHub repository](https://github.com/pdrd/spoolman)
- **This project**: Open an issue in this repository
