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
import logging
import math
import threading
import uuid

import flask
import octoprint.plugin
import requests

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

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
        self._reset_state()

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

    # ── Debug logging ────────────────────────────────────────────────────────

    def _debug_log(self, msg: str, *args):
        """Log only when debug_mode is enabled. Always uses INFO so OctoPrint
        shows it without requiring the user to change log levels."""
        try:
            if self._settings.get_boolean(["debug_mode"]):
                self._logger.info("[DEBUG] " + msg, *args)
        except Exception:
            pass  # settings not yet initialised during startup

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
        """Poll every 30 s and keep _last_filament_mm up to date."""
        while not self._stop_polling.wait(30):
            if self._print_started_at is None:
                break
            reading = self._get_filament_position_mm()
            if any(v > 0 for v in reading.values()):
                with self._lock:
                    self._last_filament_mm = reading
                self._debug_log("Filament poll snapshot: %s", reading)

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

    def _on_print_started(self, payload):
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
            self._open_new_segments(start_mm, self._current_spools)
            self._logger.info(
                "Print started: %s  spools=%s", self._current_file, self._current_spools
            )
            self._debug_log(
                "Print started detail — session=%s file=%r spools=%s start_mm=%s",
                self._session_id, self._current_file, self._current_spools, start_mm,
            )
        self._start_filament_polling()

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
                # Upload gcode file in background if we have a file path.
                if print_id and file_path:
                    t = threading.Thread(
                        target=self._upload_gcode,
                        args=(url, api_key, print_id, file_origin, file_path),
                        daemon=True,
                    )
                    t.start()
            else:
                self._logger.warning(
                    "The Moment returned %s: %s", resp.status_code, resp.text[:200]
                )
                self._debug_log("Full response body: %s", resp.text)
        except Exception as exc:
            self._logger.error("Failed to send print record to The Moment: %s", exc)


__plugin_name__ = "The Moment"
__plugin_identifier__ = "the_moment"
__plugin_version__ = "1.1.0"
__plugin_description__ = "Sends print events to The Moment for unified print history and cost tracking."
__plugin_pythoncompat__ = ">=3.9,<4"
__plugin_implementation__ = TheMomentPlugin()
