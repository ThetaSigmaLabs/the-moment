# Spoolman Location Sync

When this optional feature is enabled, The Moment keeps Spoolman's spool `location` field in sync with toolhead assignments — in both directions.

![Filament Status with location assignments](../.github/screenshots/filament_tags.png)
*Spool location assignments visible in the Filament Status tab*

<!-- screenshot needed: Settings → Advanced showing NFC Spool Locations toggle -->

---

## Enabling

Settings → Advanced → NFC Spool Locations

| Setting | Description |
| --- | --- |
| **Sync spool locations with Spoolman automatically** | Toggle that enables/disables the feature |
| **Inventory Location** | Spoolman location name to use when a spool is unassigned (e.g. `"The Cubes"`, `"Shelf"`) |

The inventory location field is shared with the NFC trash workflow. Whatever you set here is also where archived/trashed spools land in Spoolman.

---

## Location Name Format

The Moment writes and recognises locations in this exact format:

```text
{Printer Name} - T{toolhead_index}
```

- Printer name is the exact name as configured in The Moment (case-sensitive)
- Toolhead index is 0-based
- Examples: `Ender3 - T0`, `Core One L - T0`, `Roci - T2`

Any Spoolman location that does **not** match this pattern is ignored — The Moment never touches it.

---

## What Syncs, and When

### Assign spool → Spoolman updated immediately

When a spool is assigned to a toolhead (via Print Ops, NFC scan, or API), its Spoolman location is set to `{Printer Name} - T{index}` at the moment of assignment.

### Unassign spool → Spoolman reset immediately

When a spool is unassigned from a toolhead, its Spoolman location is set to the configured **Inventory Location**. This applies to all unassign paths:

- Manually clearing a toolhead in Print Ops
- Assigning a different spool to the same slot (implicitly unassigns the previous one)
- Deleting a spool record from The Moment (trash workflow)

### Spoolman location changed manually → The Moment syncs every 5 minutes

If you move a spool's location directly in Spoolman to a value The Moment recognises (e.g. change from `"Shelf"` to `"Roci - T0"`), The Moment will pick it up on the next poll and update its toolhead assignment.

The poll interval is 5 minutes. Opening the Print Ops tab also triggers an immediate sync (`POST /api/nfc/sync-locations-now`), so changes made in Spoolman are typically visible as soon as you navigate back to Print Ops — no wait required.

**Slot reassignment** (e.g. moving spool A off a slot and placing spool B on it in Spoolman) resolves in a single sync cycle. The sync removes stale assignments before adding new ones.

**Conflict handling:** If two spools have Spoolman locations pointing to the same toolhead slot, The Moment logs a warning and keeps the existing DB assignment. Resolve conflicts in Spoolman by correcting one of the locations.

### Toolhead count reduced

If a printer's toolhead count is reduced in Settings, spools assigned to the removed toolhead slots are automatically pushed to the **Inventory Location** in Spoolman and their toolhead assignments are cleared.

Example: Ender 3 configured from 2 toolheads → 1 toolhead. If spool 7 was on T1, its Spoolman location becomes the inventory location.

### Printer deleted

If a printer config is deleted from The Moment, all spools assigned to its toolheads are pushed to the **Inventory Location** in Spoolman and their assignments are cleared.

---

## Caveats

**Existing Spoolman locations are not migrated.** If you have spools in Spoolman already assigned to custom locations (`"Roci -1"`, `"Top shelf"`, etc.) that do not match the `{Name} - T{N}` format, they are left as-is. The Moment will not move them until you explicitly reassign the spool through The Moment — at that point, the sync takes over.

**Virtual printers are excluded.** Virtual printers (test/simulation printers) never participate in location sync, even when the feature is enabled.

**Spoolman location is a plain text field.** The Moment is not the only thing that can write to it. If another tool or user changes a spool's location in Spoolman to something The Moment doesn't recognise, The Moment treats that as "not a toolhead location" and will clear the corresponding toolhead assignment on the next poll if the assignment no longer matches.

---

## Summary

| Event | Spoolman location updated? | The Moment assignment updated? |
| --- | --- | --- |
| Spool assigned in The Moment | ✅ Immediately | — (source of truth) |
| Spool unassigned in The Moment | ✅ Set to Inventory Location | — (source of truth) |
| Spool location changed in Spoolman (known format) | — (already set) | ✅ Next poll (≤5 min) |
| Spool location changed in Spoolman (unknown format) | — | ✅ Assignment cleared next poll |
| Toolhead count reduced | ✅ Removed slots → Inventory Location | ✅ Assignments cleared |
| Printer deleted | ✅ All slots → Inventory Location | ✅ Assignments cleared |
