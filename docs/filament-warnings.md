# Filament Sufficiency Warnings & Pushover Notifications

The Moment can detect that an assigned spool may run out mid-print and push a notification to your phone via [Pushover](https://pushover.net). All estimates come from the slicer's G-code and are **advisory, not definitive** — treat every warning as a heads-up, not a guarantee.

**PrusaLink only.** OctoPrint and Virtual printers are not checked or notified today.

---

## Enabling

Settings → 🔔 Notifications.

1. Create a [Pushover](https://pushover.net) account (one-time purchase for the iOS/Android app).
2. Check **Enable Pushover notifications** and enter your **API Token** (application token) and **User Key** (from your Pushover dashboard).
3. Click **🔔 Send Test Notification** to confirm delivery before saving — this sends straight to Pushover with whatever is currently typed in the form, without needing to save first (`POST /api/notifications/pushover/test`, `web.go`).
4. Click **💾 Save**.

Saved credentials are masked as `***` when the page reloads (`getConfigHandler`, `web.go`) — the same pattern used for the printer API key. Leaving the masked value in place on save does not overwrite the stored credential; only typing a new value replaces it (`updateConfigHandler`, `web.go`).

---

## Warning levels

Every shortage is classified into one of two severities (`PrinterWarning.Severity`, `bridge.go`):

| Severity | Meaning | Pushover priority |
| --- | --- | --- |
| **critical** | The assigned spool does not have enough filament remaining to finish the print at all | High (1) |
| **warning** (low) | The spool will finish the print, but with less than the configured buffer margin left over | Normal (0) |

**Warning Buffer (%)** (`filament_warn_buffer_pct`) controls the low-warning threshold:

```text
threshold = required_grams × (1 + buffer_pct / 100)
```

A spool triggers **low** when `remaining < threshold` but `remaining >= required`. Setting the buffer to `0` disables the low tier entirely — only critical (insufficient to finish) still fires.

`highestSeverity()` (`bridge.go`) reduces a set of per-toolhead warnings to the single worst level, used both for the notification title and for the auto-pause decision.

---

## When the check runs

### At print start

`checkFilamentSufficiency` (`bridge.go`) runs once, in a goroutine, when a new PrusaLink print is detected:

1. Reads required grams per toolhead from the PrusaLink file's metadata; if metadata has no filament data, falls back to downloading and parsing the G-code (`ParseGcodeFilamentUsage`).
2. Looks up the spool assigned to each toolhead — first from NFC/active assignments, falling back to Print Ops `toolhead_mappings` if no NFC assignment exists.
3. Fetches current remaining weight for those spools from Spoolman (`GetAllSpools`).
4. Builds warnings per toolhead and sends a notification for the worst severity found.

If no filament data can be found in either metadata or G-code, the check is skipped entirely (logged as `filament_no_data` in the comm log) — no notification is sent either way.

### Mid-print

`recheckFilamentSufficiency` runs on every monitor cycle while the printer is in `StatePrinting`, scaling the original per-toolhead requirement by how much of the print is left:

```text
remaining_fraction = (100 - progress) / 100
scaled_required     = original_required_grams × remaining_fraction
```

This tightens the check as the print approaches its final layers, when a marginal spool is most likely to actually run out. The mid-print check only re-evaluates toolheads that already had an assignment at print start (a toolhead with no spool assigned is not re-checked).

**Notifications only escalate, never de-escalate.** Once a `critical` notification has been sent for a print, a later `warning`-level result for the same print does not send a second notification — this prevents a flood of contradictory pushes as the print progresses (`lastNotifiedSeverity`, `bridge.go`). A fresh print (new job start) always resets this state.

### Virtual printers

`checkVirtualFilamentSufficiency` runs the same comparison logic for simulated prints in `ProcessVirtualFile` (`virtual.go`). It returns warnings for display in the virtual-printer UI but does not currently trigger Pushover notifications or auto-pause.

---

## Auto-pause on critical shortage (PrusaLink only)

**Auto-pause print on critical filament shortage** (`filament_pause_on_critical`, default off) tells The Moment to call `PausePrint()` on the PrusaLink printer the moment a `critical` warning is raised.

- Fires **at most once per print** — guarded by an internal `hasPaused` flag that resets on the next print start.
- Only triggers off the `critical` tier; a `low`/`warning` result never pauses anything.
- On trigger, The Moment logs a `filament_paused` comm-log event and sends a Pushover notification with an added line: *"Print has been paused. Swap spool and resume."*
- This is **PrusaLink only**. OctoPrint and Virtual printers are never paused by this feature, regardless of the toggle.

> **Caution:** slicer filament estimates are typically ±10–20% off actual consumption. A false-positive critical warning will pause a print that would otherwise have completed successfully. Treat auto-pause as a safety net for unattended, high-risk prints — not something to leave on by default for routine work.

---

## Notification content

Every Pushover message (`sendFilamentWarningNotification`, `bridge.go`) is built from:

- Title: `"CRITICAL Filament — {Printer Name}"` or `"Low Filament — {Printer Name}"`
- One line per active warning, e.g. `T0: ~45g still needed, only 20g remaining (Bambu PLA Black)`
- If the print was auto-paused: `"Print has been paused. Swap spool and resume."`
- Always appended: `"⚠ Filament estimates are approximate. Actual usage may vary."`

Notifications are a no-op (nothing sent, no error surfaced) whenever Pushover is disabled or either credential field is empty — this lets the feature stay half-configured without generating errors in the logs.

---

## Config keys

| Key | Type | Default | Notes |
| --- | --- | --- | --- |
| `pushover_enabled` | string `"true"`/`"false"` | `"false"` | Master toggle for sending notifications |
| `pushover_api_token` | string | `""` | Pushover application token; masked as `***` once saved |
| `pushover_user_key` | string | `""` | Pushover user key; masked the same way |
| `filament_warn_buffer_pct` | string (float) | — | Buffer percentage for the low-warning tier; `0` disables it |
| `filament_pause_on_critical` | string `"true"`/`"false"` | `"false"` | Auto-pause PrusaLink print on first critical warning |

---

## Caveats

- **Estimates come from the slicer**, not a physical filament sensor. A spool with inaccurate `remaining_weight` in Spoolman (see [Spool Lifecycle](spool-lifecycle.md)) will produce inaccurate warnings.
- **No retry queue.** If the Pushover API call fails (network error, bad credentials), the failure is logged but not retried — the next check cycle will try again naturally.
- **OctoPrint and Virtual printers are not covered** by notifications or auto-pause, even though virtual printers get advisory warnings in their own UI.
