# How to Use Virtual Test Printers

Virtual test printers let you validate Spoolman integration, G-code parsing, and cost
calculations without any physical hardware. They accept `.gcode` and `.bgcode` file uploads
and simulate a complete print when you click **Process**.

---

## Before you start

- Spoolman must be running and reachable
- At least one spool must exist in Spoolman with a toolhead assignment if you want filament
  usage deducted automatically
- No IP address, API key, or network access required — virtual printers are software-only

---

## Create a virtual printer

1. Go to **Settings → Printers**
2. Click **Add Virtual Test Printer**
3. Give it a name (e.g. `Test Ender 3`) and set the number of toolheads
4. Save — the printer appears in the dashboard immediately with status `virtual`

No IP address or API key is needed. A virtual printer ID is auto-generated (e.g.
`virtual_1748402941234567890`).

_Source: `web.go` → `addVirtualPrinterHandler`_

---

## Upload a G-code file

1. Open the virtual printer card in the dashboard
2. Click **Upload G-code**
3. Select a `.gcode` or `.bgcode` file — both formats are accepted
4. The file is stored in The Moment's data directory and appears in the file list

Files are stored by printer ID. The maximum upload size is not enforced in the API but is
subject to the host system's available disk space.

_Source: `web.go` → `uploadVirtualFileHandler`_

---

## Process a file (simulate a print)

Processing parses the G-code metadata, updates Spoolman, and creates a print history entry.

1. Find the uploaded file in the printer card's file list
2. Click **Process**
3. The Moment extracts:
   - Filament used per toolhead (in grams) from `;Filament used:` slicer comments
   - Print time from `;TIME:` slicer comments
   - Thumbnails from embedded base64 image blocks (PrusaSlicer / OrcaSlicer format)
4. For each toolhead with usage, Spoolman's `used_weight` is incremented
5. A print history entry is saved with cost breakdown

The response includes a breakdown of grams per toolhead and a `skipped_toolheads` list for
any toolhead that had usage but no spool assigned. Skipped toolheads do not deduct from
Spoolman — assign a spool first and process again.

_Source: `web.go` → `processVirtualFileHandler`, `bridge.go` → `ProcessVirtualFile`_

### What happens if no spool is assigned to a toolhead?

If toolhead 0 used 12.3 g but has no spool mapped, the usage is skipped silently and reported
in `skipped_toolheads`. Spoolman is not touched for that toolhead. The print history entry is
still created — cost uses $0 filament cost for the unassigned toolhead.

---

## Assign spools to toolheads

Before processing, assign Spoolman spools to the virtual printer's toolhead slots the same way
as any other printer:

- **NFC Management tab** → find the virtual printer → assign spools per slot
- Or use the toolhead mapping UI on the printer card in the dashboard

A virtual printer's toolhead slots are real spool assignments stored in
`toolhead_spool_assignments` — the same table as physical printers.

---

## Export a virtual printer

Exporting creates a complete JSON snapshot: printer config, toolhead names, current spool
mappings, and all uploaded G-code files (base64-encoded).

```
GET /api/printers/:id/export
```

The downloaded file contains everything needed to recreate the printer on another instance.
File sizes can be large if the G-code library is big — consider your download tool's timeout.

**Note:** spool IDs in the export reference the source Spoolman instance. When importing on
another machine, those IDs must exist in the target Spoolman or you will get skipped toolheads
on first process.

_Source: `web.go` → `exportVirtualPrinterHandler`, `VirtualPrinterExport`_

---

## Import a virtual printer

```
POST /api/printers/import
Content-Type: application/json
Body: <the exported JSON file>
```

Import creates a new virtual printer with a fresh ID. It does not overwrite an existing
printer. All uploaded G-code files are restored. Toolhead names and spool mappings are
restored from the export (spool IDs must exist in the target Spoolman).

---

## Delete a file

```
DELETE /api/printers/:id/files/:file_id
```

Deletes the stored G-code file. Print history entries created from that file are not affected.

---

## Notes

- Virtual printers are excluded from the printer polling loop — they never show `offline` or
  `connecting` states. `IsVirtual: true` is checked explicitly throughout the codebase.
- The dashboard shows virtual printers with a `🧪` indicator.
- You can have multiple virtual printers, one per G-code workflow you want to test.
- Processing the same file twice creates two history entries and deducts filament twice.
  There is no deduplication guard.

_See also: [Spool Lifecycle](spool-lifecycle.md), [Cost Tracking](cost-tracking.md)_
