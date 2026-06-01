# OctoPrint Plugin — The Moment

The Moment plugin for OctoPrint is a push connector: it fires print events from OctoPrint directly to The Moment's API. Every print — started, paused, resumed, finished, cancelled, or failed — is captured with exact timestamps, filament used per toolhead, spool IDs, and a thumbnail if one is embedded in the G-code.

![Dashboard showing OctoPrint printer](../.github/screenshots/dashboard.png)
*OctoPrint-managed printers appear in the dashboard alongside PrusaLink and Bambu printers*

<!-- screenshot needed: OctoPrint plugin settings panel in OctoPrint UI -->

---

## Installation

### Prerequisites

- OctoPrint running on your printer (Ender 3 V3 SE or equivalent Marlin-based machine)
- The Moment running and reachable on your LAN (e.g. `http://192.168.1.100:5000`)
- The `octoprint-the-moment.zip` file from the repo root

### Install from file

1. In OctoPrint, go to **Settings → Plugin Manager → Get More…**
2. Click **Upload plugin…** and select `octoprint-the-moment.zip`
3. OctoPrint will install it and prompt you to restart — do so
4. After restart, go to **Settings → The Moment** to configure

### Rebuild the zip (after source changes)

```bash
cd octoprint-plugin && rm -f ../octoprint-the-moment.zip && \
  zip -r ../octoprint-the-moment.zip setup.py octoprint_the_moment/ \
      -x "octoprint_the_moment/__pycache__/*"
```

---

## Configuration

All settings live under **OctoPrint Settings → The Moment**.

| Setting | Description |
| --- | --- |
| **The Moment URL** | LAN address of The Moment, e.g. `http://192.168.1.100:5000`. Must be reachable from the OctoPrint device. |
| **API Key** | Matches the key in The Moment → Config → The Moment API Key. Leave blank if no key is configured. |
| **Printer ID** | Short identifier included in every payload, e.g. `ender3-v3-se`. This becomes the printer name shown in The Moment's print history. |
| **Spoolman Integration** | Controls who deducts filament from Spoolman — see section below. |
| **Debug Logging** | Writes verbose logs for every event, full JSON payload, and server response. |

### Test Connection button

Click **Send Test Ping to The Moment** to verify the URL and API key are correct. No print data is sent. The server version appears next to the button on success.

---

## Spoolman Integration Setting

This checkbox controls which system deducts filament from Spoolman after a print. Getting this wrong results in double-deduction.

**Checked — OctoPrint manages Spoolman (default)**

The OctoPrint [Spoolman plugin](https://github.com/mdziekon/octoprint-spoolman) or SpoolManager is installed and active. It automatically deducts filament when a print finishes. The Moment records history and calculates cost but does **not** touch Spoolman inventory.

Use this when you want OctoPrint to be the source of spool-selection truth (you pick the spool in OctoPrint before printing).

**Unchecked — The Moment manages Spoolman**

No OctoPrint Spoolman plugin is active. The Moment deducts the filament used from the spool assigned to that toolhead in its own toolhead assignment table. Before printing, assign the correct spool in The Moment's NFC & Spools tab or Printers view.

---

## What the Plugin Sends

On every print end event the plugin sends a JSON payload to `POST /api/prints` containing:

| Field | Detail |
| --- | --- |
| `printer_id` | The Printer ID setting |
| `file_name` | G-code filename |
| `status` | `completed`, `cancelled`, or `failed` |
| `started_at` / `ended_at` | UTC timestamps, second precision |
| `total_duration_sec` | Wall-clock time start→end |
| `print_duration_sec` | Excludes all pause time |
| `pause_duration_sec` | Total time paused |
| `pauses` | List of pause events with timestamps and reason |
| `filament` | Per-toolhead, per-spool breakdown (see below) |
| `spoolman_managed` | Passed through from settings so The Moment knows who owns deduction |
| `thumbnail_base64` | Largest JPG thumbnail embedded in the G-code, if present |

After the print record is created, the G-code file is uploaded in the background to `POST /api/history/{id}/gcode`.

### Filament breakdown

The plugin tracks filament position (mm extruded per tool) throughout the print. Consecutive segments on the same spool are merged; a spool change during a pause creates a new entry with an incremented `change_number`.

```json
"filament": [
  {"tool_index": 0, "change_number": 0, "spool_id": 42,
   "filament_used_mm": 3241.5, "filament_used_grams": 9.671}
]
```

Grams are estimated using 1.75 mm diameter and 1.24 g/cm³ (PLA default). For cost-accurate multi-material prints, use The Moment's toolhead assignment to ensure the correct spool ID is resolved per tool.

### Spool ID resolution

The plugin tries three sources in order:

1. **OctoPrint-SpoolManager plugin API** — `get_selected_spools()`
2. **OctoPrint Spoolman plugin API** — `get_current_spool_ids()`
3. **Event-driven cache** — populated when `plugin_spoolmanager_spool_selected` fires

If none are available the spool ID is `0` (unknown). The Moment still records the print; spool cost will be missing unless you manually correct it via the history modal.

---

## Behaviour During Network Interruptions

If The Moment is unreachable when a print ends, the plugin saves the payload to a local disk queue:

```
~/.octoprint/data/the_moment/pending_prints/
```

The queue drains automatically on OctoPrint startup and after any successful send. Entries older than 7 days are discarded.

---

## What to Expect in The Moment

After a successful push, the print appears in **Print History** within seconds.

- **Thumbnail** — shown in the history row if the G-code had one embedded (PrusaSlicer and OrcaSlicer embed JPG thumbnails by default)
- **Duration** — print time excludes pauses; pause count and total pause time are shown in the detail modal
- **Cost** — calculated from filament used × cost-per-gram configured in The Moment's cost settings; zero if spool ID is unknown
- **Filament breakdown** — per-spool usage in the detail modal; multiple entries appear if a spool swap occurred
- **G-code** — downloadable from the detail modal after upload completes in background

### Pause reasons

| Reason shown | Meaning |
| --- | --- |
| `user` | Manual pause via OctoPrint UI or physical button |
| `filament_change` | OctoPrint reported a filament-change pause |
| `runout` | Runout sensor triggered |

---

## Troubleshooting

### Prints not appearing in The Moment

1. Check the URL and API key — use the **Test Connection** button
2. Verify The Moment is running and reachable from the OctoPrint host (`curl http://<moment-ip>:5000/api/octoprint/ping`)
3. Enable **Debug Logging** and watch **Settings → Logging → octoprint.log** for `[DEBUG]` lines

### Filament shows 0 g

- No spool is selected in OctoPrint's Spoolman or SpoolManager plugin, and no spool is assigned via The Moment either
- For Marlin-based printers (Ender 3), OctoPrint sometimes clears job data before `PRINT_DONE` fires — the plugin's 30-second polling fallback handles this, but a zero reading in the log means OctoPrint's counters were already cleared

### Double-deduction from Spoolman

The **Spoolman Integration** checkbox is misconfigured. If the OctoPrint Spoolman plugin is active, the box must be **checked**. If it is not active, the box must be **unchecked**.

### Connection test passes but prints still queue

The ping endpoint (`/api/octoprint/ping`) is reachable but `POST /api/prints` fails. Check The Moment's container logs:

```bash
docker logs the-moment 2>&1 | tail -50
```

---

## Version

Current plugin version: **v1.2.0**

Both version strings must be updated together when bumping:

1. `octoprint-plugin/octoprint_the_moment/__init__.py` — `__plugin_version__ = "x.y.z"` (line ~835)
2. `octoprint-plugin/octoprint_the_moment/templates/tab_the_moment.jinja2` — hardcoded version string in the Version row

After editing both, rebuild the zip as shown in the Installation section above.
