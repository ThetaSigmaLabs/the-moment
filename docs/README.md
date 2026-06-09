# The Moment — Documentation

**The Moment** is a Go microservice that bridges 3D printers to [Spoolman](https://github.com/Donkie/Spoolman) for filament inventory tracking, cost estimation, and print history logging. It adds NFC tag support so physical filament spools carry OpenPrintTag-compatible data and can be assigned to printer toolheads by scanning an NFC tag.

![The Moment Dashboard](../.github/screenshots/dashboard.png)

## Contents

| Page | Description |
| --- | --- |
| [Deployment](deployment.md) | How to run The Moment: Docker, pre-built binary, VS Code dev mode, and full environment variable reference |
| [Spool Lifecycle](spool-lifecycle.md) | Complete lifecycle of a filament spool: from setup through printing to archiving |
| [Spoolman Location Sync](spoolman-location-sync.md) | Optional bidirectional sync between toolhead assignments and Spoolman spool locations |
| [PrusaLink Print Handling](prusalink-print-handling.md) | How The Moment tracks PrusaLink prints, attention events (runouts), and what requires manual correction in print history |
| [OctoPrint Plugin](octoprint-plugin.md) | How to install, configure, and use the OctoPrint plugin; what data it sends; troubleshooting |
| [Cost Tracking](cost-tracking.md) | How print cost is calculated: the formula, global settings, per-printer overrides, high-temp detection, and filament pricing |
| [Virtual Test Printers](virtual-printers.md) | How to create a virtual printer, upload G-code, simulate prints, and export/import printer libraries |
| [Backup, Restore & Migration](backup-restore.md) | How to back up data, restore from a backup, and migrate to a new PC (Docker and native) |

## Overview

The Moment runs as a Docker container alongside Spoolman. Key capabilities:

- **Print history** — logs every print with duration, filament used, and cost per printer
- **Toolhead assignment** — tracks which spool is loaded on which toolhead across all printers
- **NFC tags** — generates `.bin` files for spool tags (ICODE SLIX2, OpenPrintTag CBOR + URL) and location tags (NTAG215, URL only), written via [NFC Tools Pro](https://apps.apple.com/app/id1252962749) on iPhone. Confirmed stickers: [ICODE SLIX2 (spool tags)](https://www.amazon.ca/dp/B0755WF6CK), [NTAG215 (location tags)](https://www.amazon.ca/dp/B0BV25WG13) — other compatible stickers may also work
- **Spoolman sync** — pushes filament usage back to Spoolman after each print; maintains `nfc_*` custom fields for OpenPrintTag compatibility
- **Multi-printer support** — OctoPrint (Ender, single-head), PrusaLink (Prusa CORE One L); not tested, Bambu (X1C, P1S, A1 via MQTT)

## Quick Links

- [README](../README.md) — setup and deployment
- [CONTRIBUTING](../CONTRIBUTING.md) — developer guide, architecture, and contributing
- [CHANGELOG](../CHANGELOG.md) — release history
