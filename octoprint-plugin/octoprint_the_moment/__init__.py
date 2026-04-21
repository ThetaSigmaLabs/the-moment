"""
OctoPrint plugin for The Moment — pushes print events to The Moment API
so it becomes the single source of print history and cost across all printers.

Tracks:
  - Print start / finish / cancel / fail
  - Pause events with timestamps and reasons
  - Per-tool filament usage (split by spool when a filament change occurs)
  - Spool IDs via the OctoPrint-SpoolManager or Spoolman plugin (optional)

Settings (configured in OctoPrint Settings → The Moment):
  - url        The Moment base URL, e.g. http://192.168.1.10:5000
  - api_key    API key set in The Moment (leave blank if not configured)
  - printer_id Identifier sent in every payload, e.g. "ender3-v3-se"
"""

import datetime
import logging
import threading

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
):
    def __init__(self):
        self._logger = logging.getLogger(__name__)
        self._reset_state()

    # ── State management ────────────────────────────────────────────────────

    def _reset_state(self):
        self._print_started_at: datetime.datetime | None = None
        self._current_file: str = ""
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
        self._lock = threading.Lock()

    # ── OctoPrint SettingsPlugin ─────────────────────────────────────────────

    def get_settings_defaults(self):
        return dict(
            url="http://localhost:5000",
            api_key="",
            printer_id="ender3",
        )

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
        Segments for the same (tool_index, spool_id) pair are merged.
        mm is converted to grams using 1 cm³ ≈ 1.24 g (PLA density average).
        Callers that have exact gram values from Spoolman can override this.
        """
        self._close_open_segments(final_mm_per_tool)

        # Merge by (tool_index, spool_id)
        merged: dict[tuple[int, int], float] = {}
        for seg in self._filament_segments:
            key = (seg["tool_index"], seg["spool_id"])
            merged[key] = merged.get(key, 0.0) + seg.get("used_mm", 0.0)

        # Diameter 1.75 mm, density 1.24 g/cm³ (PLA) — reasonable default.
        import math
        radius_cm = 0.175 / 2  # 1.75mm → 0.175cm
        density = 1.24
        cross_section = math.pi * radius_cm ** 2

        entries = []
        for (tool_idx, spool_id), used_mm in sorted(merged.items()):
            if used_mm <= 0:
                continue
            used_cm = used_mm / 10.0
            grams = used_cm * cross_section * density
            entries.append(dict(
                tool_index=tool_idx,
                spool_id=spool_id,
                filament_used_mm=round(used_mm, 2),
                filament_used_grams=round(grams, 3),
            ))
        return entries

    # ── Event handling ───────────────────────────────────────────────────────

    def on_event(self, event, payload):
        Events = octoprint.events.Events

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
            self._current_file = payload.get("name", "")
            self._current_spools = self._get_current_spools()
            start_mm = self._get_filament_position_mm()
            self._open_new_segments(start_mm, self._current_spools)
            self._logger.info(
                "Print started: %s  spools=%s", self._current_file, self._current_spools
            )

    def _on_print_paused(self, payload):
        with self._lock:
            if self._print_started_at is None:
                return
            now = datetime.datetime.utcnow()
            current_mm = self._get_filament_position_mm()
            self._close_open_segments(current_mm)

            reason = self._classify_pause_reason(payload)
            self._active_pause = dict(
                paused_at=now,
                resumed_at=None,
                duration_sec=0.0,
                reason=reason,
            )
            self._logger.info("Print paused: reason=%s", reason)

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
            self._open_new_segments(resume_mm, new_spools)
            self._current_spools = new_spools
            self._logger.info("Print resumed: spools=%s", new_spools)

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
            filament_entries = self._build_filament_payload(final_mm)

            total_sec = (ended_at - self._print_started_at).total_seconds()
            pause_sec = sum(p["duration_sec"] for p in self._pauses)
            print_sec = max(0.0, total_sec - pause_sec)

            body = dict(
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
                time_precision="exact",
                filament_precision="measured",
            )

            self._logger.info(
                "Print ended: status=%s file=%s duration=%.0fs filament=%s",
                status, self._current_file, total_sec, filament_entries,
            )
            self._reset_state()

        # Send outside the lock to avoid holding it during network I/O
        self._send(body)

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

    def _send(self, body: dict):
        url = (self._settings.get(["url"]) or "").rstrip("/")
        api_key = self._settings.get(["api_key"]) or ""
        if not url:
            self._logger.warning("The Moment URL is not configured — skipping push")
            return

        endpoint = url + "/api/prints"
        headers = {"Content-Type": "application/json"}
        if api_key:
            headers["X-API-Key"] = api_key

        try:
            resp = requests.post(endpoint, json=body, headers=headers, timeout=10)
            if resp.status_code == 201:
                self._logger.info("Sent print record to The Moment (id=%s)", resp.json().get("id"))
            else:
                self._logger.warning(
                    "The Moment returned %s: %s", resp.status_code, resp.text[:200]
                )
        except Exception as exc:
            self._logger.error("Failed to send print record to The Moment: %s", exc)


__plugin_name__ = "The Moment"
__plugin_version__ = "1.0.0"
__plugin_description__ = "Sends print events to The Moment for unified print history and cost tracking."
__plugin_pythoncompat__ = ">=3.7,<4"
__plugin_implementation__ = TheMomentPlugin()
