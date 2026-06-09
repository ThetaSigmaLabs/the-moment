# Spool Lifecycle

This page covers the complete lifecycle of a filament spool in The Moment — from the moment it arrives to the moment it is archived.

---

## 1. Add the Spool to Spoolman

Before The Moment can work with a spool, it must exist in Spoolman.

1. Open Spoolman in a browser (or click **🧵 Spoolman** in The Moment's header)
2. Go to **Spools → Add Spool**
3. Select the filament type, enter the lot number and initial weight if known, and save
4. Spoolman assigns the spool a numeric ID (e.g. `42`) — this ID is used throughout The Moment

> The Moment reads all filament and spool data from Spoolman. It never duplicates filament specs. Spoolman is the source of truth.

---

## 2. Program the NFC Tag

Spool tags use **ICODE SLIX2** stickers. See the [docs overview](README.md#overview) for confirmed sticker and app options. Each tag carries two NDEF records:

- **Record 1:** OpenPrintTag CBOR — machine-readable filament data (temperatures, color, weight, UUID, material properties) for hardware NFC readers
- **Record 2:** URL — `http://{moment-host}/nfc/spool/{spoolman-id}` — opens The Moment in a phone browser when scanned

![Spool Tags](../.github/screenshots/spool_tags.png)

### Steps

1. Open The Moment → **NFC Management** → **Spool Tags**
2. Find the spool in the list and click it to select it
3. Click **📲 Edit OpenPrintTag Fields & Download .bin**
4. In the modal, review and fill in the fields:
   - **Spool details:** actual weight, manufacturing date, expiration date
   - **Print settings:** min/max nozzle temp, min/max bed temp, material class, country of origin
   - Pre-filled values come from Spoolman (extruder temp, bed temp, etc.) — adjust as needed
5. Click **Save to Spoolman & Download .bin** — the file saves to your downloads (e.g. `spool-42.bin`)
   - All field values are written back to Spoolman's `nfc_*` custom fields for future use
   - A unique UUID (`nfc_spool_uuid`) is auto-generated if this spool doesn't have one yet
   - The `.bin` file contains a dual-record NDEF message: OpenPrintTag CBOR + URL
6. On iPhone, open **NFC Tools Pro** → **Write** → **Write Dump**
7. Select the downloaded `.bin` file
8. Hold the iPhone near the ICODE SLIX2 sticker — it writes in under a second
9. Affix the sticker to the spool

The tag is now programmed. You only need to redo this if the filament spec changes significantly (new brand, different material). The URL on the tag never changes as long as the Spoolman spool ID stays the same.

---

## 3. Load the Spool onto the Printer

Mount the spool on the printer as normal and thread the filament through to the extruder.

---

## 4. Assign the Spool to a Toolhead

The Moment tracks which spool is loaded on which toolhead. There are two ways to assign:

### Via location tag (recommended for quick loading)

Each toolhead slot has an **NTAG215** location tag sticker. Location tags contain a single URL: `http://{moment-host}/nfc/location/{printer-slug}/{toolhead-index}`.

1. Scan the location tag on the printer (iPhone or Android NFC — browser opens automatically)
2. The page shows the printer name and toolhead index with a list of available spools
3. Tap the spool you just loaded
4. The assignment is saved — The Moment now knows spool 42 is on Ender 3 Toolhead 0

### Via spool tag

1. Scan the spool tag — browser opens the spool's detail page in The Moment
2. The page shows current filament info and an "Assign to..." list of printer toolheads
3. Tap the toolhead you loaded it on
4. Assignment saved

### Via the NFC Management tab

In The Moment dashboard → **NFC Management** → **Spool Tags** — select a spool and assign it directly from the UI without scanning any tags.

### Spoolman location sync (optional)

If **Spoolman location sync** is enabled in Settings → Advanced, assigning a spool also updates its location in Spoolman to `{Printer Name} - T{toolhead_index}` (e.g. `"Ender3 - T0"`). Unassigning resets the location to the configured inventory location. Changes made directly in Spoolman are picked up by The Moment on the next poll (every 5 minutes). See [Spoolman Location Sync](spoolman-location-sync.md) for full details.

---

## 5. Printing

Once a spool is assigned, The Moment tracks its use automatically.

- When a print **starts**, The Moment snapshots the current toolhead assignments
- When a print **completes**, The Moment calculates filament used (from G-code metadata or printer report) and pushes `used_weight` back to Spoolman for each spool that participated
- Spoolman's `remaining_weight` is updated accordingly
- Print history in The Moment shows cost per spool, cost per printer, and a full filament usage breakdown

If you change a spool mid-print (runout, colour change), scan the new spool tag or location tag to update the assignment. The Moment will credit the original spool and track the new one for the remainder of the print.

---

## 6. Spool Depletes

When the spool runs out:

- The Moment automatically updates `used_weight` in Spoolman after the final print
- Unassign the spool from the toolhead by assigning a new spool to that slot, or use the **NFC Management** tab to clear the assignment

> **Remaining weight can reach zero or go negative.** Print weight estimates are not always exact, and some spools arrive with unknown initial weight. The Moment treats a spool with zero or negative remaining weight as fully valid — it still appears in toolhead assignment dropdowns, Print Ops, and history reassignment. A negative value is informational, not an error state. To stop a depleted spool from appearing, explicitly archive it with the 🗑️ action (step 7 below).

---

## 7. Archive the Spool

When the physical spool is empty and ready to discard:

1. Open The Moment → **NFC Management** → **Spool Tags**
2. Find the depleted spool in the list
3. Click the **🗑️** button on the right side of the row
4. A confirmation dialog shows the spool name and current remaining weight
5. Confirm — The Moment will:
   - Set remaining weight to 0 in Spoolman (credits the difference as used)
   - Move the spool to the configured **Trash** location in Spoolman (Settings → NFC → Trash Location, default: `"Trash"`)
   - Set `archived: true` on the spool record in Spoolman
6. The row disappears from all The Moment lists and dropdowns

The spool disappears because Spoolman's standard spool API excludes archived records — The Moment never requests `allow_archived=true` in normal operation. A spool with zero or negative remaining weight that has **not** been archived will still appear in all lists; only the explicit 🗑️ archive action triggers disappearance.

The Spoolman record is **not deleted** — it is archived. All print history, cost data, filament totals, and the `nfc_spool_uuid` remain intact for reporting and reference.

---

## 8. Reuse the NFC Tag

The NFC sticker is reusable. The data on it is completely overwritten each time it is programmed.

1. Peel the sticker off the empty spool core before discarding or recycling it
2. Add the new spool to Spoolman (step 1)
3. Program the sticker with the new spool's data (step 2)
4. Affix the sticker to the new spool

The old spool's UUID and Spoolman ID are fully overwritten. The new spool gets its own UUID on first programming.

---

## What Stays in Spoolman

The archived spool record in Spoolman retains:

| Data | Where |
|---|---|
| Full print history reference | The Moment's `print_spool_events` and `toolhead_spool_assignments` tables |
| Filament used (grams) | Spoolman `used_weight` |
| Cost history | The Moment's `print_history` table |
| OpenPrintTag fields | Spoolman `nfc_*` custom fields on the spool and filament |
| UUID | Spoolman `nfc_spool_uuid` extra field |
| Location | Set to "Trash" (or your configured value) |

Nothing is lost. The archive in Spoolman is the permanent record.

---

## Summary Diagram

```
New spool in Spoolman
       │
       ▼
Program NFC tag (.bin via NFC Tools)
       │
       ▼
Load spool + scan tag to assign toolhead
       │
       ▼
Print (The Moment tracks usage → Spoolman updated)
       │
  ─────┴─────
  │         │
  ▼         ▼
Spool     Spool
change    depletes
  │         │
  ▼         ▼
Rescan    Archive via 🗑️ in The Moment
new spool    │
             ▼
       Spoolman record archived ("Trash")
             │
             ▼
       Peel tag → reuse for next spool
```
