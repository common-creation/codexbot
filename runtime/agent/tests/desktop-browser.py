import importlib.util
import io
from pathlib import Path
import unittest
from unittest.mock import MagicMock, patch

spec = importlib.util.spec_from_file_location("desktop_mcp", Path(__file__).parents[1] / "bin/desktop-mcp.py")
desktop = importlib.util.module_from_spec(spec)
spec.loader.exec_module(desktop)


class BrowserOpenTest(unittest.TestCase):
    def test_unmanaged_window_does_not_prevent_managed_launch(self):
        opener = MagicMock()
        opener.open.side_effect = OSError("connection refused")
        with patch.object(desktop, "require_agent_lease"), patch.object(desktop.urllib.request, "build_opener", return_value=opener), patch.object(desktop, "run") as run, patch.object(desktop.subprocess, "Popen") as launch:
            desktop.call_tool("desktop_open_app", {"app": "browser", "generation": 1})
            run.assert_not_called()
            self.assertEqual(launch.call_args.args[0], ["/usr/local/lib/codexbot/chromium-visible", "about:blank"])

    def test_connected_browser_is_focused_without_new_window(self):
        opener = MagicMock()
        opener.open.return_value.__enter__.return_value = io.StringIO('{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/test"}')
        with patch.object(desktop, "require_agent_lease"), patch.object(desktop.urllib.request, "build_opener", return_value=opener), patch.object(desktop, "run", return_value=b"123\n") as run, patch.object(desktop.subprocess, "Popen") as launch:
            desktop.call_tool("desktop_open_app", {"app": "browser", "generation": 1})
            launch.assert_not_called()
            self.assertEqual(run.call_args.args[0], ["xdotool", "windowactivate", "--sync", "123"])


if __name__ == "__main__":
    unittest.main()
