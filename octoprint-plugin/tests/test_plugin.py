"""
Unit tests for the The Moment OctoPrint plugin.

Runs without a real OctoPrint installation — all OctoPrint framework classes
are stubbed out so only the plugin's own logic is tested.

Run with:
    pip install requests pytest
    python -m pytest octoprint-plugin/tests/ -v
"""

import os
import sys
import types
import unittest
from unittest.mock import MagicMock, patch

# Make the plugin package importable when running from anywhere.
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


# ---------------------------------------------------------------------------
# Minimal OctoPrint stubs — must be installed before the plugin is imported.
# ---------------------------------------------------------------------------

def _make_octoprint_stubs():
    octoprint = types.ModuleType("octoprint")
    plugin_mod = types.ModuleType("octoprint.plugin")
    events_mod = types.ModuleType("octoprint.events")

    class SettingsPlugin:
        pass

    class EventHandlerPlugin:
        pass

    class TemplatePlugin:
        pass

    class AssetPlugin:
        pass

    class SimpleApiPlugin:
        pass

    plugin_mod.SettingsPlugin = SettingsPlugin
    plugin_mod.EventHandlerPlugin = EventHandlerPlugin
    plugin_mod.TemplatePlugin = TemplatePlugin
    plugin_mod.AssetPlugin = AssetPlugin
    plugin_mod.SimpleApiPlugin = SimpleApiPlugin

    class _Events:
        PRINT_STARTED = "PrintStarted"
        PRINT_PAUSED = "PrintPaused"
        PRINT_RESUMED = "PrintResumed"
        PRINT_DONE = "PrintDone"
        PRINT_CANCELLED = "PrintCancelled"
        PRINT_FAILED = "PrintFailed"

    events_mod.Events = _Events

    octoprint.plugin = plugin_mod
    octoprint.events = events_mod

    sys.modules["octoprint"] = octoprint
    sys.modules["octoprint.plugin"] = plugin_mod
    sys.modules["octoprint.events"] = events_mod


_make_octoprint_stubs()

# Also stub flask before importing the plugin.
flask_mod = types.ModuleType("flask")
flask_mod.jsonify = lambda *args, **kw: args[0] if args else kw  # return dict for easy assertion
sys.modules["flask"] = flask_mod

from octoprint_the_moment import TheMomentPlugin  # noqa: E402 (after stubs)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_plugin(url="http://moment:5000", api_key="", printer_id="test-printer",
                 debug_mode=False):
    """Return a TheMomentPlugin with settings and logger stubbed out."""
    plugin = TheMomentPlugin.__new__(TheMomentPlugin)
    plugin._logger = MagicMock()

    settings = MagicMock()
    settings.get = lambda keys, **_: {
        "url": url,
        "api_key": api_key,
        "printer_id": printer_id,
    }.get(keys[-1], "")
    settings.get_boolean = lambda keys, **_: debug_mode if keys[-1] == "debug_mode" else False
    plugin._settings = settings

    plugin._reset_state()
    return plugin


# ---------------------------------------------------------------------------
# Debug logging
# ---------------------------------------------------------------------------

class TestDebugLog(unittest.TestCase):
    def test_debug_log_silent_when_disabled(self):
        plugin = _make_plugin(debug_mode=False)
        plugin._debug_log("should not appear")
        plugin._logger.info.assert_not_called()

    def test_debug_log_emits_when_enabled(self):
        plugin = _make_plugin(debug_mode=True)
        plugin._debug_log("hello %s", "world")
        plugin._logger.info.assert_called_once()
        call_args = plugin._logger.info.call_args[0]
        assert "[DEBUG]" in call_args[0]
        assert "hello %s" in call_args[0]

    def test_debug_log_survives_settings_exception(self):
        plugin = TheMomentPlugin.__new__(TheMomentPlugin)
        plugin._logger = MagicMock()
        plugin._settings = MagicMock(side_effect=Exception("no settings"))
        # Should not raise.
        plugin._debug_log("safe call")


# ---------------------------------------------------------------------------
# Test connection
# ---------------------------------------------------------------------------

class TestTestConnection(unittest.TestCase):
    def test_no_url_returns_failure(self):
        plugin = _make_plugin(url="")
        result = plugin._test_connection()
        assert result["success"] is False
        assert "not configured" in result["message"].lower()

    def test_success_200(self):
        plugin = _make_plugin(url="http://moment:5000")
        mock_resp = MagicMock()
        mock_resp.status_code = 200
        mock_resp.json.return_value = {
            "status": "ok",
            "message": "Connection successful.",
            "timestamp": "2026-04-24T12:00:00Z",
            "version": "1.0.0",
        }
        with patch("octoprint_the_moment.requests.get", return_value=mock_resp) as mock_get:
            result = plugin._test_connection()
        mock_get.assert_called_once_with(
            "http://moment:5000/api/octoprint/ping",
            headers={},
            timeout=5,
        )
        assert result["success"] is True
        assert result["message"] == "Connection successful."
        assert result["server_time"] == "2026-04-24T12:00:00Z"
        assert result["server_version"] == "1.0.0"

    def test_success_sends_api_key_header(self):
        plugin = _make_plugin(url="http://moment:5000", api_key="secret123")
        mock_resp = MagicMock()
        mock_resp.status_code = 200
        mock_resp.json.return_value = {"message": "ok", "timestamp": ""}
        with patch("octoprint_the_moment.requests.get", return_value=mock_resp) as mock_get:
            plugin._test_connection()
        _, kwargs = mock_get.call_args
        assert kwargs["headers"]["X-API-Key"] == "secret123"

    def test_unauthorized_401(self):
        plugin = _make_plugin(url="http://moment:5000", api_key="bad")
        mock_resp = MagicMock()
        mock_resp.status_code = 401
        with patch("octoprint_the_moment.requests.get", return_value=mock_resp):
            result = plugin._test_connection()
        assert result["success"] is False
        assert "api key" in result["message"].lower()

    def test_unexpected_status(self):
        plugin = _make_plugin(url="http://moment:5000")
        mock_resp = MagicMock()
        mock_resp.status_code = 500
        mock_resp.text = "Internal Server Error"
        with patch("octoprint_the_moment.requests.get", return_value=mock_resp):
            result = plugin._test_connection()
        assert result["success"] is False
        assert "500" in result["message"]

    def test_connection_error(self):
        import requests as req_mod
        plugin = _make_plugin(url="http://moment:5000")
        with patch("octoprint_the_moment.requests.get", side_effect=req_mod.exceptions.ConnectionError("refused")):
            result = plugin._test_connection()
        assert result["success"] is False
        assert "could not connect" in result["message"].lower()

    def test_generic_exception(self):
        plugin = _make_plugin(url="http://moment:5000")
        with patch("octoprint_the_moment.requests.get", side_effect=RuntimeError("boom")):
            result = plugin._test_connection()
        assert result["success"] is False
        assert "boom" in result["message"]


# ---------------------------------------------------------------------------
# _send() debug logging
# ---------------------------------------------------------------------------

class TestSendDebugLogging(unittest.TestCase):
    def _send_with_debug(self, debug: bool):
        plugin = _make_plugin(url="http://moment:5000", debug_mode=debug)
        mock_resp = MagicMock()
        mock_resp.status_code = 201
        mock_resp.json.return_value = {"id": 42}
        mock_resp.text = '{"id":42}'
        with patch("octoprint_the_moment.requests.post", return_value=mock_resp):
            plugin._send({"printer_id": "p", "file_name": "f.gcode", "status": "completed"})
        return plugin._logger

    def test_no_debug_log_when_disabled(self):
        logger = self._send_with_debug(debug=False)
        # Only the success info log should appear; no [DEBUG] lines.
        for call in logger.info.call_args_list:
            assert "[DEBUG]" not in call[0][0], "unexpected [DEBUG] log when debug disabled"

    def test_debug_log_appears_when_enabled(self):
        logger = self._send_with_debug(debug=True)
        debug_calls = [c for c in logger.info.call_args_list if "[DEBUG]" in c[0][0]]
        assert len(debug_calls) >= 1, "expected at least one [DEBUG] log when debug enabled"


# ---------------------------------------------------------------------------
# get_settings_defaults
# ---------------------------------------------------------------------------

class TestSettingsDefaults(unittest.TestCase):
    def test_debug_mode_default_false(self):
        plugin = TheMomentPlugin.__new__(TheMomentPlugin)
        defaults = plugin.get_settings_defaults()
        assert defaults["debug_mode"] is False

    def test_required_keys_present(self):
        plugin = TheMomentPlugin.__new__(TheMomentPlugin)
        defaults = plugin.get_settings_defaults()
        for key in ("url", "api_key", "printer_id", "debug_mode"):
            assert key in defaults, "missing default for '{}'".format(key)


# ---------------------------------------------------------------------------
# get_api_commands
# ---------------------------------------------------------------------------

class TestApiCommands(unittest.TestCase):
    def test_test_connection_registered(self):
        plugin = TheMomentPlugin.__new__(TheMomentPlugin)
        cmds = plugin.get_api_commands()
        assert "test_connection" in cmds

    def test_on_api_command_dispatches(self):
        plugin = _make_plugin(url="")
        result = plugin.on_api_command("test_connection", {})
        assert result["success"] is False  # url not configured


if __name__ == "__main__":
    unittest.main()
