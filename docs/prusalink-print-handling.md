# PrusaLink Print Handling

This page describes how The Moment tracks prints on PrusaLink printers (Prusa CORE One L and compatible), what it does automatically, and what requires manual attention after a print.

---

## How Print Tracking Works

The Moment polls PrusaLink every 30 seconds. It does not receive push events — it observes state transitions.

| Transition | What The Moment does |
|---|---|
| `IDLE` → `PRINTING` | Records the job filename and print start time |
| `PRINTING` → `ATTENTION` | Records a pending attention event with the current progress % |
| `ATTENTION` → `PRINTING` | No action — print continues normally |
| `PRINTING` → `FINISHED` / `IDLE` | Downloads the G-code, parses filament usage, creates print history records, updates Spoolman |
| `PRINTING` → `STOPPED` | Records a cancelled print with the progress % at cancellation |

**Note on timing:** Because polling is every 30 seconds, the recorded progress at any state transition is the last observed value before the transition — accurate to within one poll interval.

---

## Normal Print (No Interruptions)

1. Print starts — The Moment snapshots current toolhead assignments
2. Print finishes — The Moment:
   - Downloads the `.gcode` file from the printer
   - Parses filament usage per toolhead from G-code metadata (`;Filament used:` comments from PrusaSlicer / OrcaSlicer)
   - Creates one `print_history` row per toolhead (single-toolhead = one row, T0)
   - Pushes `used_weight` to Spoolman for each assigned spool
   - Stores a thumbnail (if the G-code contains one)

---

## Attention Events During a Print

PrusaLink reports `state=ATTENTION` when the printer requires operator intervention. On the CORE One L this includes:

- **Filament runout** — the most common cause
- **Opening the enclosure door** — also triggers ATTENTION
- Any other pause-with-action the firmware requests

The Moment **cannot distinguish** between these causes from the PrusaLink API. It treats every ATTENTION event the same way.

### What happens automatically

On first transition to `ATTENTION`, The Moment records a pending event with the current progress percentage. No assignments are changed. The print continues tracking as normal.

When the print finishes, The Moment detects the recorded event(s) and **splits the print into virtual toolhead segments**:

| Segment | Virtual toolhead | Filament assigned |
|---|---|---|
| Before first ATTENTION | T0 | Original spool × (progress % / 100) |
| After first ATTENTION | T1 | Original spool × remaining fraction |
| After second ATTENTION | T2 | Original spool × remaining fraction |

Example: Print of 2.86 g, ATTENTION at 38% progress:

- **T0** — 1.09 g — Spool #42 (original)
- **T1** — 1.77 g — Spool #42 (original — needs correction if a swap occurred)

Both segments are created with **the original spool**. Spoolman will show a larger deduction than the spool actually consumed. The spool balance may go negative. This is expected and correct — see the next section for how to fix it.

### What requires manual action

If you actually swapped to a different spool during the ATTENTION pause:

1. Open **Print History** in The Moment
2. Find the print — it shows a **2 tools** (or **3 tools**) badge
3. Expand the session to see sub-rows for T0, T1, T2…
4. Click on the T1 row to open its detail modal
5. The modal shows a note: *"Attention-event segment. If a different spool was loaded, reassign it below."*
6. In the **Filament by Tool** table, click **Reassign** on the T1 row
7. Select the spool you actually used and enter the correct grams if you know them
8. Confirm — The Moment credits the original spool and debits the replacement

If no spool swap occurred (door open, brief attention, same spool resumed), no action is needed. Both T0 and T1 point to the same spool and together sum to the correct total weight.

---

## Multiple Attention Events

Two ATTENTION events during a single print produce three virtual toolheads: T0, T1, T2. Repeat the reassignment process above for each segment that used a different spool.

Example: 5-day print, two runouts:

| Segment | Progress range | Spool to assign |
|---|---|---|
| T0 | 0% → 31% | Spool A (original — no change needed) |
| T1 | 31% → 67% | Click Reassign → select Spool B |
| T2 | 67% → 100% | Click Reassign → select Spool C |

---

## What PrusaLink Does Not Expose

Some information is simply not available from the PrusaLink API:

| Item | Status |
|---|---|
| Cause of ATTENTION (runout vs. door open) | Not available — assume runout if a spool was swapped |
| Layer number at event time | Not available — only progress % (0–100) |
| Time elapsed at event time | Not available on CORE One L (always 0) |
| Runout sensor state directly | Not available |
| Mid-print filament weight consumed | Not available — G-code estimate only |

The filament split is always proportional to progress %, not layer count or time. For most prints this is a good approximation.

---

## Cancelled Prints

When a print is stopped (state transitions to `STOPPED`), The Moment records it as `status=cancelled` with the progress % at the time of cancellation. Filament usage is estimated proportionally from the G-code total × progress fraction. Spoolman is updated with this estimate.

Attention events that occurred before a cancellation are still recorded and the split logic applies the same way.

---

## G-Code File Availability

The Moment downloads the G-code file from the printer's USB storage at print completion. If the file is not available (USB removed, file deleted before processing), the download is queued for retry. A warning appears in the dashboard until the file is retrieved or the retry is exhausted.

**If the G-code file is permanently unavailable**, the print record shows `gcode_unavailable=true` and filament usage will be 0. You can manually set the filament used via the Reassign flow in the detail modal.

---

## Summary: What The Moment Does vs. What You Do

| Scenario | Automatic | Manual |
|---|---|---|
| Normal single-spool print | Full — history + Spoolman update | Nothing |
| ATTENTION event, same spool continued | Full — print splits into T0/T1 with same spool | Nothing — totals are correct |
| ATTENTION event, spool swapped | Partial — splits at correct progress % | Reassign T1+ in print history to the replacement spool |
| Multiple ATTENTION events | Partial — creates T0/T1/T2… | Reassign each segment that used a different spool |
| Print cancelled | Full — proportional usage logged | Nothing (or adjust grams via Reassign if estimate is wrong) |
