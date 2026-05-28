# The Moment — Documentation

**The Moment** is a Go microservice that bridges 3D printers to [Spoolman](https://github.com/Donkie/Spoolman) for filament inventory tracking, cost estimation, and print history logging. It adds NFC tag support so physical filament spools carry OpenPrintTag-compatible data and can be assigned to printer toolheads by scanning an NFC tag.

## Contents

| Page | Description |
|---|---|
| [Spool Lifecycle](spool-lifecycle.md) | Complete lifecycle of a filament spool: from setup through printing to archiving |

## Overview

The Moment runs as a Docker container alongside Spoolman. Key capabilities:

- **Print history** — logs every print with duration, filament used, and cost per printer
- **Toolhead assignment** — tracks which spool is loaded on which toolhead across all printers
- **NFC tags** — generates `.bin` files for spool tags (ICODE SLIX2, OpenPrintTag CBOR + URL) and location tags (NTAG215, URL only), written via NFC Tools Pro
- **Spoolman sync** — pushes filament usage back to Spoolman after each print; maintains `nfc_*` custom fields for OpenPrintTag compatibility
- **Multi-printer support** — OctoPrint (Ender, single-head), PrusaLink (Prusa CORE One L); not tested, Bambu (X1C, P1S, A1 via MQTT)

## Quick Links

- [README](../README.md) — setup and deployment
- [CLAUDE.md](../CLAUDE.md) — developer guide and architecture
- [CHANGELOG](../CHANGELOG.md) — release history
