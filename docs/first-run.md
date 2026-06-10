# First Run Guide

You've deployed The Moment. Here's how to go from running containers to your first automatically-logged print.

**Time required:** about 10 minutes.

---

## Before you start: Spoolman first

The Moment reads filament and spool data from Spoolman — it does not create spool records itself. Before The Moment can track anything, you need at least one spool in Spoolman.

Open Spoolman at `http://<your-server-ip>:7912`:

1. **Add a filament type** — Manufacturer → Filament → Add. Fill in material (PLA, PETG, etc.) and purchase price if you want cost tracking.
2. **Add a spool** — Spools → Add Spool. Select the filament type, enter the starting weight.

You only need one to start. You can add more later.

---

## Step 1 — Add your printer

Open The Moment at `http://<your-server-ip>:5000`.

Go to **Settings → Printers → Add Printer**.

| Printer type | What to enter |
|---|---|
| PrusaLink (CORE One, XL, MK4, Mini+) | Type: PrusaLink · IP address · API key from printer screen · Toolheads: 1 (or 5 for XL) |
| OctoPrint (Ender, Voron, etc.) | Type: OctoPrint · install the plugin first (see Step 1a below) |
| Bambu X1C / P1S / A1 | Type: Bambu · IP · API Key as `serial:accesscode` · Toolheads: AMS slot count |
| No hardware yet | Type: Virtual · lets you simulate prints from G-code files |

**Save.** The Moment will poll PrusaLink printers immediately. OctoPrint printers become active when the plugin sends its first event.

### Step 1a — OctoPrint plugin (OctoPrint printers only)

1. Download `octoprint-the-moment.zip` from the [Releases page](https://github.com/ThetaSigmaLabs/the-moment/releases).
2. OctoPrint → Settings → Plugin Manager → Upload zip.
3. OctoPrint → Settings → The Moment: set URL to `http://<your-server-ip>:5000` and Printer ID to a short name (e.g. `ender3`).
4. Use that exact same name when adding the printer in The Moment.

---

## Step 2 — Assign a spool to a toolhead

Go to **Print Ops** (the spool icon in the nav).

Find your printer. Under the toolhead slot, click the spool dropdown and select the spool you added in Spoolman. Click **Assign**.

The dashboard will now show the spool name next to the toolhead. The Moment knows what's loaded.

---

## Step 3 — Print something

Start a print on your printer normally. The Moment polls PrusaLink every 30 seconds and will detect the print in progress. When it finishes:

- Spoolman's spool weight is updated automatically
- A history entry appears under **Print History** with duration, grams used, and cost
- The thumbnail from your slicer is shown if your G-code had one embedded (PrusaSlicer and OrcaSlicer both embed them by default)

That's it.

---

## Optional: cost settings

Go to **Settings → Cost Settings** to configure your electricity rate, printer wattage, and maintenance rate. These are applied to every future print. You can also set per-printer overrides if your printers have different power draws.

---

## Optional: NFC spool tags

If you want to assign spools by tapping your iPhone instead of using the dropdown:

1. Go to **NFC Tags** and generate a spool tag for each physical spool.
2. Generate a location tag for each printer toolhead.
3. Write the tags using [NFC Tools Pro](https://apps.apple.com/app/nfc-tools/id1252962749).
4. Tap spool → tap printer slot (within 5 minutes) to assign.

Full details: [spoolman-location-sync.md](spoolman-location-sync.md)

---

## Troubleshooting

**Print finished but nothing logged** — confirm the spool was assigned before the print started. Assignments made after a print has begun are not applied retroactively.

**Spoolman connection error** — Settings → Basic Configuration → Test Connection. Verify the URL matches what Spoolman is actually listening on.

**PrusaLink printer shows offline** — check the IP in Settings → Printers. PrusaLink must be enabled on the printer (Prusa Connect → Settings → PrusaLink → Enable).

**OctoPrint not sending events** — check OctoPrint → Settings → The Moment → URL is correct and reachable from the OctoPrint host. Check `octoprint.log` for connection errors.

For more: see the full [Troubleshooting section in README](../README.md#troubleshooting).
