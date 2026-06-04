"""
OctoPrint plugin for The Moment — pushes print events to The Moment API
so it becomes the single source of print history and cost across all printers.

Tracks:
  - Print start / finish / cancel / fail
  - Pause events with timestamps and reasons
  - Per-tool filament usage (split by spool when a filament change occurs)
  - Spool IDs via the OctoPrint-SpoolManager or Spoolman plugin (optional)

Settings (configured in OctoPrint Settings → The Moment):
  - url             The Moment base URL, e.g. http://192.168.1.10:5000
  - api_key         API key set in The Moment (leave blank if not configured)
  - printer_id      Identifier sent in every payload, e.g. "ender3-v3-se"
  - spoolman_managed  True when the OctoPrint Spoolman/SpoolManager plugin is
                    installed and deducts filament from Spoolman automatically.
                    When False, The Moment will deduct from Spoolman instead.
  - debug_mode      Enable verbose logging to OctoPrint's system log
"""

import datetime
import glob
import json
import logging
import math
import os
import threading
import uuid

import flask
import octoprint.plugin
import requests

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_MAX_PENDING_AGE_DAYS = 7


def _utc_now() -> str:
    return datetime.datetime.utcnow().replace(microsecond=0).isoformat() + "Z"


def _iso(dt: datetime.datetime) -> str:
    return dt.replace(microsecond=0).isoformat() + "Z"


# ---------------------------------------------------------------------------
# Plugin
# ---------------------------------------------------------------------------

class TheMomentPlugin(
    octoprint.plugin.SettingsPlugin,
    octoprint.plugin.EventHandlerPlugin,
    octoprint.plugin.TemplatePlugin,
    octoprint.plugin.AssetPlugin,
    octoprint.plugin.SimpleApiPlugin,
):
    def __init__(self):
        self._logger = logging.getLogger(__name__)
        # Persists across prints — updated by plugin_spoolmanager_spool_selected events.
        # Fallback when neither SpoolManager nor Spoolman plugin exposes a direct API.
        self._spool_cache: dict[int, int] = {}
        # Set in _send() after a print record is created; used by the Spoolman usage
        # commit event handler to patch filament data onto the record.
        self._pending_print_id: int | None = None
        # True when the last print was sent with filament=[]; drives the patch decision.
        self._sent_empty_filament: bool = False
        self._reset_state()
        self._drain_lock = threading.Lock()

    # ── State management ────────────────────────────────────────────────────

    def _reset_state(self):
        self._print_started_at: datetime.datetime | None = None
        self._session_id: str = ""
        self._current_file: str = ""
        self._current_file_path: str = ""
        self._current_file_origin: str = "local"
        # Each entry: {"paused_at": datetime, "resumed_at": datetime|None,
        #              "duration_sec": float, "reason": str,
        #              "spool_snapshot": dict[int, int]}   # tool→spoolID at pause
        self._pauses: list[dict] = []
        self._active_pause: dict | None = None
        # Filament segments: list of {"tool_index": int, "spool_id": int,
        #                              "start_mm": float}
        # Closed at each pause/end with {"end_mm": float}
        self._filament_segments: list[dict] = []
        # Spool IDs at last known moment: tool_index → spool_id
        self._current_spools: dict[int, int] = {}
        # Filament position (mm) per tool at the moment tracking began / last resumed
        self._segment_start_mm: dict[int, float] = {}
        # Last polled filament position — used as fallback if PRINT_DONE reads zero
        self._last_filament_mm: dict[int, float] = {}
        self._lock = threading.Lock()
        # Background polling thread for filament position
        self._stop_polling: threading.Event | None = None
        self._polling_thread: threading.Thread | None = None
        # Progress snapshot state
        self._progress_snapshot_config: dict = {}   # fetched from The Moment at print start
        self._progress_snapshots: list[dict] = []   # [{progress_pct, jpeg_base64}, ...]
        self._last_snapshot_pct: float = 0.0

    # ── Debug logging ────────────────────────────────────────────────────────

    def _debug_log(self, msg: str, *args):
        """Log only when debug_mode is enabled. Always uses INFO so OctoPrint
        shows it without requiring the user to change log levels."""
        try:
            if self._settings.get_boolean(["debug_mode"]):
                self._logger.info("[DEBUG] " + msg, *args)
        except Exception:
            pass  # settings not yet initialised during startup

    # ── Progress snapshot helpers ────────────────────────────────────────────

    def _snapshot_targets(self, cfg: dict) -> list[float]:
        """Return sorted target percentages from a ProgressSnapshotConfig dict."""
        mode = cfg.get("mode", "none")
        if mode == "interval":
            interval = float(cfg.get("interval") or 0)
            if interval <= 0 or interval >= 100:
                return []
            targets = []
            v = interval
            while v < 100:
                targets.append(v)
                v += interval
            return targets
        if mode == "milestones":
            milestones = [float(m) for m in (cfg.get("milestones") or []) if 0 < float(m) < 100]
            return sorted(set(milestones))
        return []

    def _crossed_targets(self, targets: list[float], last_pct: float, current_pct: float) -> list[float]:
        """Return targets strictly in (last_pct, current_pct]."""
        if not targets or current_pct <= last_pct:
            return []
        return [t for t in targets if last_pct < t <= current_pct]

    def _fetch_webcam_snapshot(self) -> bytes | None:
        """Capture a JPEG from OctoPrint's configured webcam snapshot URL."""
        try:
            snap_url = self._settings.global_get(["webcam", "snapshot"])
            if not snap_url:
                return None
            resp = requests.get(snap_url, timeout=10)
            if resp.status_code == 200 and resp.content:
                return resp.content
        except Exception as exc:
            self._logger.debug("Webcam snapshot failed: %s", exc)
        return None

    def _fetch_progress_snapshot_config(self, url: str, api_key: str, printer_id: str) -> dict:
        """Fetch progress_snapshot_config for this printer from The Moment."""
        if not url or not printer_id:
            return {}
        try:
            headers = {"X-Api-Key": api_key} if api_key else {}
            resp = requests.get(f"{url}/api/printers", headers=headers, timeout=5)
            if resp.status_code == 200:
                data = resp.json()
                printers = data.get("printers") or {}
                p = printers.get(printer_id) or {}
                return p.get("progress_snapshot_config") or {}
        except Exception as exc:
            self._logger.debug("Failed to fetch progress_snapshot_config: %s", exc)
        return {}

    # ── OctoPrint SettingsPlugin ─────────────────────────────────────────────

    def get_settings_defaults(self):
        return dict(
            url="",
            api_key="",
            printer_id="ender3",
            # True = OctoPrint Spoolman/SpoolManager plugin deducts filament from
            # Spoolman automatically; The Moment will NOT deduct a second time.
            # False = No Spoolman plugin is installed; The Moment will deduct.
            spoolman_managed=True,
            debug_mode=False,
        )

    # ── OctoPrint lifecycle ──────────────────────────────────────────────────

    def on_after_startup(self):
        """Drain any prints that were queued while The Moment was unreachable."""
        threading.Thread(target=self._drain_pending_queue, daemon=True).start()

    # ── OctoPrint SimpleApiPlugin ────────────────────────────────────────────

    def get_api_commands(self):
        return {"test_connection": []}

    def on_api_command(self, command, data):
        if command == "test_connection":
            return self._test_connection()

    def _test_connection(self):
        """Send GET /api/octoprint/ping to The Moment and relay the result."""
        url = (self._settings.get(["url"]) or "").rstrip("/")
        api_key = self._settings.get(["api_key"]) or ""

        if not url:
            self._logger.warning("Test connection failed: URL is not configured")
            return flask.jsonify({"success": False, "message": "URL is not configured."})

        endpoint = url + "/api/octoprint/ping"
        headers = {}
        if api_key:
            headers["X-API-Key"] = api_key

        self._debug_log("Test connection → %s", endpoint)
        try:
            resp = requests.get(endpoint, headers=headers, timeout=5)
            self._debug_log("Test connection response: HTTP %d — %s", resp.status_code, resp.text[:200])
            if resp.status_code == 200:
                data = resp.json()
                self._logger.info("Test connection succeeded: %s", data.get("message", "ok"))
                return flask.jsonify({
                    "success": True,
                    "message": data.get("message", "Connected successfully."),
                    "server_time": data.get("timestamp", ""),
                    "server_version": data.get("version", ""),
                })
            elif resp.status_code == 401:
                self._logger.warning("Test connection failed: unauthorized — check API key")
                return flask.jsonify({
                    "success": False,
                    "message": "Unauthorized — check your API key in The Moment settings.",
                })
            else:
                self._logger.warning("Test connection failed: HTTP %d", resp.status_code)
                return flask.jsonify({
                    "success": False,
                    "message": "The Moment returned HTTP {}: {}".format(resp.status_code, resp.text[:120]),
                })
        except requests.exceptions.ConnectionError:
            self._logger.warning("Test connection failed: could not reach %s", url)
            return flask.jsonify({
                "success": False,
                "message": "Could not connect to {}. Is The Moment running?".format(url),
            })
        except Exception as exc:
            self._logger.error("Test connection error: %s", exc)
            return flask.jsonify({"success": False, "message": "Error: {}".format(exc)})

    # ── OctoPrint TemplatePlugin ─────────────────────────────────────────────

    def get_template_configs(self):
        return [
            dict(type="settings", name="The Moment", template="tab_the_moment.jinja2", custom_bindings=False)
        ]

    # ── OctoPrint AssetPlugin ────────────────────────────────────────────────

    def get_assets(self):
        return {}

    # ── Spool helpers ────────────────────────────────────────────────────────

    def _get_current_spools(self) -> dict[int, int]:
        """Return {tool_index: spool_id} from SpoolManager or Spoolman plugin if available."""
        spools: dict[int, int] = {}
        try:
            # Try OctoPrint-SpoolManager first
            sm = self._plugin_manager.get_plugin("spoolmanager")
            if sm and hasattr(sm, "get_selected_spools"):
                for tool_idx, spool in (sm.get_selected_spools() or {}).items():
                    if spool and spool.get("databaseId"):
                        spools[int(tool_idx)] = int(spool["databaseId"])
                return spools
        except Exception:
            pass
        try:
            # Try Spoolman plugin
            spoolman = self._plugin_manager.get_plugin("spoolman")
            if spoolman and hasattr(spoolman, "get_current_spool_ids"):
                for tool_idx, spool_id in (spoolman.get_current_spool_ids() or {}).items():
                    if spool_id:
                        spools[int(tool_idx)] = int(spool_id)
                return spools
        except Exception:
            pass
        # Fall back to event-driven cache populated by plugin_spoolmanager_spool_selected.
        if self._spool_cache:
            return dict(self._spool_cache)
        return spools

    def _get_filament_position_mm(self) -> dict[int, float]:
        """Return {tool_index: filament_used_mm} from OctoPrint's current job data."""
        result: dict[int, float] = {}
        try:
            data = self._printer.get_current_data()
            filament = (data.get("job") or {}).get("filament") or {}
            for tool_key, values in filament.items():
                if tool_key.startswith("tool") and values:
                    idx = int(tool_key.replace("tool", ""))
                    result[idx] = float(values.get("length") or 0)
        except Exception:
            pass
        return result

    def _close_open_segments(self, end_mm_per_tool: dict[int, float]):
        """Close any open filament segments using the given end positions."""
        for seg in self._filament_segments:
            if "end_mm" not in seg:
                tool = seg["tool_index"]
                start = seg["start_mm"]
                end = end_mm_per_tool.get(tool, start)
                seg["end_mm"] = end
                seg["used_mm"] = max(0.0, end - start)

    def _open_new_segments(self, start_mm_per_tool: dict[int, float], spools: dict[int, int]):
        """Open a new filament segment for every tool, using current spool assignments."""
        for tool_idx, spool_id in spools.items():
            self._filament_segments.append(
                dict(tool_index=tool_idx, spool_id=spool_id,
                     start_mm=start_mm_per_tool.get(tool_idx, 0.0))
            )
        # Handle tools present in position data but missing from spools (no spool assigned)
        for tool_idx in start_mm_per_tool:
            if tool_idx not in spools:
                self._filament_segments.append(
                    dict(tool_index=tool_idx, spool_id=0,
                         start_mm=start_mm_per_tool.get(tool_idx, 0.0))
                )

    def _build_filament_payload(self, final_mm_per_tool: dict[int, float]) -> list[dict]:
        """
        Close open segments and return the filament list for the API payload.

        Consecutive segments on the same tool with the same spool_id are merged
        (a pause without a filament swap). When the spool changes for a tool, a
        new entry is emitted with an incrementing change_number (0 = initial load,
        1 = first manual swap, …).  Multi-toolhead prints use distinct tool_index
        values, all with change_number=0.

        mm → grams uses 1.75 mm diameter, 1.24 g/cm³ (PLA default).
        """
        self._close_open_segments(final_mm_per_tool)

        # Build ordered runs per tool: list of [spool_id, used_mm], merging
        # consecutive same-spool segments so pauses don't inflate change_number.
        tool_runs: dict[int, list[list]] = {}
        for seg in self._filament_segments:
            tool = seg["tool_index"]
            spool = seg["spool_id"]
            used = seg.get("used_mm", 0.0)
            if used <= 0:
                continue
            if tool not in tool_runs:
                tool_runs[tool] = []
            runs = tool_runs[tool]
            if runs and runs[-1][0] == spool:
                runs[-1][1] += used
            else:
                runs.append([spool, used])

        radius_cm = 0.175 / 2
        cross_section = math.pi * radius_cm ** 2
        density = 1.24

        entries = []
        for tool_idx in sorted(tool_runs.keys()):
            for change_number, (spool_id, used_mm) in enumerate(tool_runs[tool_idx]):
                used_cm = used_mm / 10.0
                grams = used_cm * cross_section * density
                entries.append(dict(
                    tool_index=tool_idx,
                    change_number=change_number,
                    spool_id=spool_id,
                    filament_used_mm=round(used_mm, 2),
                    filament_used_grams=round(grams, 3),
                ))
        return entries

    # ── Filament position polling ─────────────────────────────────────────────

    def _start_filament_polling(self):
        """Start a background thread that samples filament position every 30 s.

        OctoPrint sometimes clears job data before PRINT_DONE fires, which would
        make the final reading appear as zero.  The polling thread keeps a
        rolling snapshot so _on_print_ended can fall back to the last good value.
        """
        self._stop_polling = threading.Event()
        self._polling_thread = threading.Thread(
            target=self._filament_poll_loop, daemon=True, name="tm-filament-poll"
        )
        self._polling_thread.start()

    def _stop_filament_polling(self):
        if self._stop_polling is not None:
            self._stop_polling.set()
        self._polling_thread = None
        self._stop_polling = None

    def _filament_poll_loop(self):
        """Poll every 30 s and keep _last_filament_mm up to date.
        Also checks print progress and captures webcam snapshots at configured thresholds."""
        while not self._stop_polling.wait(30):
            if self._print_started_at is None:
                break
            reading = self._get_filament_position_mm()
            if any(v > 0 for v in reading.values()):
                with self._lock:
                    self._last_filament_mm = reading
                self._debug_log("Filament poll snapshot: %s", reading)

            # Progress snapshot check
            try:
                data = self._printer.get_current_data()
                completion = (data.get("progress") or {}).get("completion") or 0
                current_pct = float(completion)
            except Exception:
                current_pct = 0.0

            with self._lock:
                cfg = self._progress_snapshot_config
                last_pct = self._last_snapshot_pct

            targets = self._snapshot_targets(cfg)
            crossed = self._crossed_targets(targets, last_pct, current_pct)
            for tgt in crossed:
                jpeg = self._fetch_webcam_snapshot()
                if jpeg:
                    import base64
                    b64 = base64.b64encode(jpeg).decode("ascii")
                    with self._lock:
                        self._progress_snapshots.append({"progress_pct": tgt, "jpeg_base64": b64})
                        self._last_snapshot_pct = tgt
                    self._logger.info("Progress snapshot captured at %.0f%%", tgt)
                else:
                    with self._lock:
                        self._last_snapshot_pct = tgt
                    self._debug_log("No webcam snapshot at %.0f%% (URL not configured or failed)", tgt)

    # ── Event handling ───────────────────────────────────────────────────────

    def on_event(self, event, payload):
        Events = octoprint.events.Events

        self._debug_log("Event received: %s  payload=%s", event, payload)

        if event == Events.PRINT_STARTED:
            self._on_print_started(payload)

        elif event == Events.PRINT_PAUSED:
            self._on_print_paused(payload)

        elif event == Events.PRINT_RESUMED:
            self._on_print_resumed(payload)

        elif event == Events.PRINT_DONE:
            self._on_print_ended(payload, status="completed", cancel_reason=None)

        elif event == Events.PRINT_CANCELLED:
            self._on_print_ended(payload, status="cancelled", cancel_reason="user")

        elif event == Events.PRINT_FAILED:
            self._on_print_ended(payload, status="failed", cancel_reason="error")

        elif event == "plugin_spoolmanager_spool_selected":
            # Keep _spool_cache and active _current_spools current so that
            # _get_current_spools() has a value even when the plugin API is absent.
            tool_id = payload.get("toolId")
            db_id = payload.get("databaseId")
            if tool_id is not None and db_id:
                self._spool_cache[int(tool_id)] = int(db_id)
                with self._lock:
                    self._current_spools[int(tool_id)] = int(db_id)

        elif event == "plugin_Spoolman_spool_usage_committed":
            # Fires ~600 ms after PrintDone with the actual Spoolman spool ID and
            # measured extrusion length.  When the print record was sent with empty
            # filament data (because _get_current_spools returned {}), patch it now.
            if not self._sent_empty_filament:
                return
            tool_idx = int(payload.get("toolIdx") or 0)
            spoolman_id_raw = payload.get("spoolId")
            extrusion_mm = float(payload.get("extrusionLength") or 0)
            print_id = self._pending_print_id
            if not (print_id and spoolman_id_raw and extrusion_mm > 0):
                return
            url = (self._settings.get(["url"]) or "").rstrip("/")
            api_key = self._settings.get(["api_key"]) or ""
            if url:
                threading.Thread(
                    target=self._patch_filament_usage,
                    args=(url, api_key, print_id, tool_idx, int(spoolman_id_raw), extrusion_mm),
                    daemon=True,
                ).start()

    def _on_print_started(self, payload):
        self._pending_print_id = None
        self._sent_empty_filament = False
        with self._lock:
            self._reset_state()
            self._print_started_at = datetime.datetime.utcnow()
            self._session_id = str(uuid.uuid4())
            self._current_file = payload.get("name", "")
            self._current_file_path = payload.get("path", "")
            self._current_file_origin = payload.get("origin", "local")
            self._current_spools = self._get_current_spools()
            start_mm = self._get_filament_position_mm()
            self._last_filament_mm = dict(start_mm)  # prime the fallback
            # For Marlin (Ender 3), job.filament.tool0.length is the constant
            # G-code estimate — same at start and end, so delta = 0. Open segments
            # at 0 so the final estimate (end_mm) is used directly as consumed.
            # Klipper/Prusa real-time counters also start at 0, so this is correct
            # for both firmware types.
            self._open_new_segments({}, self._current_spools)
            self._logger.info(
                "Print started: %s  spools=%s", self._current_file, self._current_spools
            )
            self._debug_log(
                "Print started detail — session=%s file=%r spools=%s start_mm=%s",
                self._session_id, self._current_file, self._current_spools, start_mm,
            )
        self._start_filament_polling()

        # Fetch progress snapshot config in background (best-effort, don't block start).
        threading.Thread(
            target=self._load_progress_snapshot_config, daemon=True, name="tm-psc-fetch"
        ).start()

    def _load_progress_snapshot_config(self):
        url = (self._settings.get(["url"]) or "").rstrip("/")
        api_key = self._settings.get(["api_key"]) or ""
        printer_id = self._settings.get(["printer_id"]) or ""
        cfg = self._fetch_progress_snapshot_config(url, api_key, printer_id)
        with self._lock:
            self._progress_snapshot_config = cfg
        self._debug_log("Progress snapshot config loaded: %s", cfg)

    def _on_print_paused(self, payload):
        with self._lock:
            if self._print_started_at is None:
                return
            now = datetime.datetime.utcnow()
            current_mm = self._get_filament_position_mm()
            # Update fallback snapshot before closing segments
            if any(v > 0 for v in current_mm.values()):
                self._last_filament_mm = dict(current_mm)
            self._close_open_segments(current_mm)

            reason = self._classify_pause_reason(payload)
            self._active_pause = dict(
                paused_at=now,
                resumed_at=None,
                duration_sec=0.0,
                reason=reason,
            )
            self._logger.info("Print paused: reason=%s", reason)
            self._debug_log("Pause detail — time=%s reason=%s filament_mm=%s", _iso(now), reason, current_mm)

    def _on_print_resumed(self, payload):
        with self._lock:
            if self._print_started_at is None or self._active_pause is None:
                return
            now = datetime.datetime.utcnow()
            self._active_pause["resumed_at"] = now
            self._active_pause["duration_sec"] = (
                now - self._active_pause["paused_at"]
            ).total_seconds()
            self._pauses.append(self._active_pause)
            self._active_pause = None

            # New spools may have been loaded during the pause (filament change/runout)
            new_spools = self._get_current_spools()
            resume_mm = self._get_filament_position_mm()
            if any(v > 0 for v in resume_mm.values()):
                self._last_filament_mm = dict(resume_mm)
            self._open_new_segments(resume_mm, new_spools)
            self._current_spools = new_spools
            self._logger.info("Print resumed: spools=%s", new_spools)
            self._debug_log("Resume detail — time=%s new_spools=%s resume_mm=%s", _iso(now), new_spools, resume_mm)

    def _on_print_ended(self, payload, status: str, cancel_reason):
        with self._lock:
            if self._print_started_at is None:
                return
            ended_at = datetime.datetime.utcnow()

            # Close any open pause that was never resumed (e.g. print failed while paused)
            if self._active_pause is not None:
                self._active_pause["resumed_at"] = ended_at
                self._active_pause["duration_sec"] = (
                    ended_at - self._active_pause["paused_at"]
                ).total_seconds()
                self._pauses.append(self._active_pause)
                self._active_pause = None

            final_mm = self._get_filament_position_mm()

            # OctoPrint sometimes clears job data before PRINT_DONE fires, producing
            # an all-zero reading.  Fall back to the last polled snapshot so we still
            # have an accurate segment length.
            if not any(v > 0 for v in final_mm.values()) and self._last_filament_mm:
                self._logger.info(
                    "Final filament reading was zero — using last polled snapshot: %s",
                    self._last_filament_mm,
                )
                final_mm = self._last_filament_mm

            filament_entries = self._build_filament_payload(final_mm)

            total_sec = (ended_at - self._print_started_at).total_seconds()
            pause_sec = sum(p["duration_sec"] for p in self._pauses)
            print_sec = max(0.0, total_sec - pause_sec)

            spoolman_managed = self._settings.get_boolean(["spoolman_managed"])

            # Extract thumbnail and capture file path before state is cleared.
            thumbnail_b64 = self._extract_thumbnail(
                self._current_file_origin, self._current_file_path
            )
            file_origin = self._current_file_origin
            file_path = self._current_file_path

            progress_snapshots = list(self._progress_snapshots)

            body = dict(
                session_id=self._session_id,
                source="octoprint",
                printer_id=self._settings.get(["printer_id"]),
                file_name=self._current_file,
                status=status,
                started_at=_iso(self._print_started_at),
                ended_at=_iso(ended_at),
                total_duration_sec=round(total_sec, 1),
                print_duration_sec=round(print_sec, 1),
                pause_duration_sec=round(pause_sec, 1),
                pause_count=len(self._pauses),
                pauses=[
                    dict(
                        paused_at=_iso(p["paused_at"]),
                        resumed_at=_iso(p["resumed_at"]) if p["resumed_at"] else _utc_now(),
                        duration_sec=round(p["duration_sec"], 1),
                        reason=p["reason"],
                    )
                    for p in self._pauses
                ],
                cancel_reason=cancel_reason,
                filament=filament_entries,
                # Tell The Moment whether OctoPrint is already deducting from Spoolman.
                # True  = OctoPrint Spoolman/SpoolManager plugin handles inventory;
                #         The Moment must NOT deduct again.
                # False = No Spoolman plugin active; The Moment should deduct.
                spoolman_managed=spoolman_managed,
                time_precision="exact",
                filament_precision="measured",
            )
            if thumbnail_b64:
                body["thumbnail_base64"] = thumbnail_b64
            if progress_snapshots:
                body["progress_snapshots"] = progress_snapshots

            self._logger.info(
                "Print ended: status=%s file=%s duration=%.0fs filament=%s spoolman_managed=%s thumbnail=%s",
                status, self._current_file, total_sec, filament_entries, spoolman_managed,
                "yes" if thumbnail_b64 else "no",
            )
            self._reset_state()

        self._stop_filament_polling()
        # Send outside the lock to avoid holding it during network I/O.
        # Gcode upload runs in a background thread after the record is created.
        self._send(body, file_origin=file_origin, file_path=file_path)

    # ── Pause reason classification ─────────────────────────────────────────

    def _classify_pause_reason(self, payload) -> str:
        # OctoPrint sets payload["reason"] for some pause types
        reason = (payload.get("reason") or "").lower()
        if "filament" in reason or "change" in reason:
            return "filament_change"
        if "runout" in reason:
            return "runout"
        return "user"

    # ── HTTP send ─────────────────────────────────────────────────────────────

    def _extract_thumbnail(self, origin: str, path: str) -> str:
        """Return the largest JPG thumbnail from a gcode file as a data URI, or ''."""
        try:
            disk_path = self._file_manager.path_on_disk(origin, path)
            best_pixels, best_b64 = 0, ""
            block_lines: list[str] = []
            in_block = False
            width = height = 0
            with open(disk_path, "r", errors="replace") as fh:
                for raw in fh:
                    line = raw.rstrip("\n")
                    if not line.startswith(";"):
                        if in_block:
                            in_block = False
                        continue
                    stripped = line[1:].lstrip()
                    if stripped.startswith("thumbnail_JPG begin ") or stripped.startswith("thumbnail begin "):
                        # e.g. "thumbnail_JPG begin 96x96 1234"
                        in_block = True
                        block_lines = []
                        try:
                            dims = stripped.split()[2]  # "96x96"
                            width, height = (int(x) for x in dims.split("x"))
                        except Exception:
                            width = height = 0
                    elif (stripped.startswith("thumbnail_JPG end") or stripped.startswith("thumbnail end")) and in_block:
                        in_block = False
                        b64 = "".join(block_lines)
                        pixels = width * height
                        if pixels > best_pixels:
                            best_pixels = pixels
                            prefix = "data:image/jpeg;base64," if "JPG" in line else "data:image/png;base64,"
                            best_b64 = prefix + b64
                    elif in_block:
                        block_lines.append(stripped)
            if best_b64:
                self._logger.debug("Extracted thumbnail (%dx%d) from %s", width, height, path)
            return best_b64
        except Exception as exc:
            self._logger.warning("Could not extract thumbnail from %s: %s", path, exc)
            return ""

    def _upload_gcode(self, url: str, api_key: str, print_id: int, origin: str, path: str):
        """Upload the gcode file to The Moment in a background thread."""
        try:
            disk_path = self._file_manager.path_on_disk(origin, path)
            endpoint = url + "/api/history/" + str(print_id) + "/gcode"
            headers = {}
            if api_key:
                headers["X-API-Key"] = api_key
            with open(disk_path, "rb") as fh:
                resp = requests.post(
                    endpoint,
                    files={"file": (path.split("/")[-1], fh, "application/octet-stream")},
                    headers=headers,
                    timeout=120,
                )
            if resp.status_code in (200, 201):
                self._logger.info("Uploaded gcode to The Moment for print %d", print_id)
            else:
                self._logger.warning(
                    "Gcode upload returned %s: %s", resp.status_code, resp.text[:200]
                )
        except Exception as exc:
            self._logger.warning("Could not upload gcode to The Moment: %s", exc)

    def _send(self, body: dict, file_origin: str = "local", file_path: str = ""):
        url = (self._settings.get(["url"]) or "").rstrip("/")
        api_key = self._settings.get(["api_key"]) or ""
        if not url:
            self._logger.warning("The Moment URL is not configured — skipping push")
            return

        endpoint = url + "/api/prints"
        headers = {"Content-Type": "application/json"}
        if api_key:
            headers["X-API-Key"] = api_key

        import json as _json
        self._debug_log(
            "Sending print payload to %s — %s",
            endpoint, _json.dumps(body, default=str),
        )

        try:
            resp = requests.post(endpoint, json=body, headers=headers, timeout=10)
            if resp.status_code == 201:
                print_id = resp.json().get("id")
                self._logger.info("Sent print record to The Moment (id=%s)", print_id)
                self._debug_log("Response: HTTP 201 — %s", resp.text[:500])
                if print_id:
                    self._pending_print_id = print_id
                    self._sent_empty_filament = not body.get("filament")
                # Upload gcode file in background if we have a file path.
                if print_id and file_path:
                    t = threading.Thread(
                        target=self._upload_gcode,
                        args=(url, api_key, print_id, file_origin, file_path),
                        daemon=True,
                    )
                    t.start()
                # Drain any prints queued while The Moment was previously unreachable.
                threading.Thread(target=self._drain_pending_queue, daemon=True).start()
            else:
                self._logger.warning(
                    "The Moment returned %s: %s", resp.status_code, resp.text[:200]
                )
                self._debug_log("Full response body: %s", resp.text)
                self._save_to_pending_queue(body, file_origin, file_path)
        except Exception as exc:
            self._logger.error("Failed to send print record to The Moment: %s", exc)
            self._save_to_pending_queue(body, file_origin, file_path)


    # ── Pending-print disk queue ─────────────────────────────────────────────

    def _pending_queue_dir(self) -> str:
        d = os.path.join(self.get_plugin_data_folder(), "pending_prints")
        os.makedirs(d, exist_ok=True)
        return d

    def _save_to_pending_queue(self, body: dict, file_origin: str, file_path: str):
        """Persist a failed print push to disk so it can be retried on next startup or reconnect."""
        try:
            d = self._pending_queue_dir()
            session_id = body.get("session_id") or str(uuid.uuid4())
            ts = datetime.datetime.utcnow().strftime("%Y%m%dT%H%M%SZ")
            fname = os.path.join(d, "{}_{}.json".format(ts, session_id))
            entry = {
                "queued_at": _utc_now(),
                "attempts": 0,
                "file_origin": file_origin,
                "file_path": file_path,
                "payload": body,
            }
            with open(fname, "w") as fh:
                json.dump(entry, fh)
            self._logger.warning(
                "Print queued for retry (The Moment unreachable): %s → %s",
                body.get("file_name", "?"), fname,
            )
        except Exception as exc:
            self._logger.error("Failed to save print to pending queue: %s", exc)

    def _send_queued(self, entry: dict) -> bool:
        """Send one queued entry. Returns True on 201, False otherwise. Never re-queues."""
        url = (self._settings.get(["url"]) or "").rstrip("/")
        api_key = self._settings.get(["api_key"]) or ""
        if not url:
            return False
        body = entry.get("payload", {})
        file_origin = entry.get("file_origin", "local")
        file_path = entry.get("file_path", "")
        endpoint = url + "/api/prints"
        headers = {"Content-Type": "application/json"}
        if api_key:
            headers["X-API-Key"] = api_key
        try:
            resp = requests.post(endpoint, json=body, headers=headers, timeout=10)
            if resp.status_code == 201:
                print_id = resp.json().get("id")
                self._logger.info(
                    "Sent queued print to The Moment (id=%s, file=%s)",
                    print_id, body.get("file_name", "?"),
                )
                if print_id and file_path:
                    threading.Thread(
                        target=self._upload_gcode,
                        args=(url, api_key, print_id, file_origin, file_path),
                        daemon=True,
                    ).start()
                return True
            self._logger.warning(
                "Queued print returned %s: %s", resp.status_code, resp.text[:200],
            )
            return False
        except Exception as exc:
            self._logger.warning("Queued print send failed: %s", exc)
            return False

    def _drain_pending_queue(self):
        """Send all queued prints to The Moment, oldest first. Safe to call from any thread."""
        if not self._drain_lock.acquire(blocking=False):
            return  # another drain is already in progress
        try:
            d = self._pending_queue_dir()
            files = sorted(glob.glob(os.path.join(d, "*.json")))
            if not files:
                return
            self._logger.info("Draining %d queued print(s)…", len(files))
            cutoff = datetime.datetime.utcnow() - datetime.timedelta(days=_MAX_PENDING_AGE_DAYS)
            for fpath in files:
                try:
                    with open(fpath) as fh:
                        entry = json.load(fh)
                    try:
                        queued_at = datetime.datetime.strptime(
                            entry.get("queued_at", ""), "%Y-%m-%dT%H:%M:%SZ"
                        )
                    except ValueError:
                        queued_at = datetime.datetime.utcnow()
                    if queued_at < cutoff:
                        self._logger.warning(
                            "Discarding expired queued print (>%d days old): %s",
                            _MAX_PENDING_AGE_DAYS, fpath,
                        )
                        os.remove(fpath)
                        continue
                    if self._send_queued(entry):
                        os.remove(fpath)
                    else:
                        entry["attempts"] = entry.get("attempts", 0) + 1
                        with open(fpath, "w") as fh:
                            json.dump(entry, fh)
                except Exception as exc:
                    self._logger.error("Error processing queued print %s: %s", fpath, exc)
        finally:
            self._drain_lock.release()

    def _patch_filament_usage(self, url: str, api_key: str, print_id: int,
                               tool_idx: int, spoolman_id: int, extrusion_mm: float):
        """POST filament usage to /api/prints/:id/filament after a Spoolman commit event."""
        radius_cm = 0.175 / 2
        cross_section = math.pi * radius_cm ** 2
        grams = round((extrusion_mm / 10.0) * cross_section * 1.24, 3)

        endpoint = "{}/api/prints/{}/filament".format(url, print_id)
        headers = {"Content-Type": "application/json"}
        if api_key:
            headers["X-API-Key"] = api_key
        body = dict(
            tool_index=tool_idx,
            change_number=0,
            spool_id=spoolman_id,
            filament_used_mm=round(extrusion_mm, 2),
            filament_used_grams=grams,
        )
        try:
            resp = requests.post(endpoint, json=body, headers=headers, timeout=10)
            if resp.status_code in (200, 201, 204):
                self._logger.info(
                    "Patched filament for print %d: spool=%d tool=%d %.2fmm=%.3fg",
                    print_id, spoolman_id, tool_idx, extrusion_mm, grams,
                )
            else:
                self._logger.warning(
                    "Filament patch for print %d returned %d: %s",
                    print_id, resp.status_code, resp.text[:200],
                )
        except Exception as exc:
            self._logger.error("Failed to patch filament for print %d: %s", print_id, exc)


__plugin_name__ = "The Moment"
__plugin_identifier__ = "the_moment"
__plugin_version__ = "1.2.0"
__plugin_description__ = "Sends print events to The Moment for unified print history and cost tracking."
__plugin_pythoncompat__ = ">=3.9,<4"
__plugin_implementation__ = TheMomentPlugin()
