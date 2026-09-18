"""Protocol, gateway boundary, and existing-profile migration regression tests."""

import http.server
import importlib.util
import io
import json
from pathlib import Path
import socketserver
import subprocess
import sys
import tempfile
import threading
import tomllib
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "bin/collaboration-mcp.py"
SPEC = importlib.util.spec_from_file_location("collaboration_mcp", SCRIPT)
mcp = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(mcp)


def rpc(name, arguments=None):
    return mcp.handle_request({"jsonrpc": "2.0", "id": 7, "method": "tools/call",
                               "params": {"name": name, "arguments": arguments or {}}})


class MCPTests(unittest.TestCase):
    def test_stdio_recovers_from_bad_requests_and_ignores_notifications(self):
        requests = ["{bad", "[]", json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}),
                    json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                                "params": {"protocolVersion": "2025-06-18"}}),
                    json.dumps({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})]
        result = subprocess.run([sys.executable, str(SCRIPT)], input="\n".join(requests) + "\n",
                                text=True, capture_output=True, check=True)
        responses = [json.loads(line) for line in result.stdout.splitlines()]
        self.assertEqual(len(responses), 4)
        self.assertEqual(responses[0]["error"]["code"], -32700)
        self.assertEqual(responses[1]["error"]["code"], -32600)
        self.assertEqual(responses[2]["result"]["protocolVersion"], "2025-06-18")
        tools = {tool["name"]: tool for tool in responses[3]["result"]["tools"]}
        self.assertEqual(set(tools), {"agents_list", "agents_get", "tasks_send", "tasks_list", "tasks_get", "tasks_cancel"})
        self.assertTrue(tools["agents_get"]["annotations"]["readOnlyHint"])
        self.assertTrue(tools["tasks_send"]["annotations"]["destructiveHint"])
        self.assertTrue(tools["tasks_send"]["annotations"]["idempotentHint"])

    def test_invalid_arguments_never_reach_gateway(self):
        invalid = [("missing_tool", {}), ("agents_get", {}), ("agents_list", {"sourceAgentId": "spoof"}),
                   ("tasks_send", {"agentId": "a", "prompt": "work"}),
                   ("tasks_send", {"agentId": "a", "prompt": " ", "idempotencyKey": "key"}),
                   ("tasks_list", {"limit": True}), ("tasks_list", {"limit": 101}),
                   ("tasks_list", {"direction": "all"}), ("tasks_list", {"status": "made_up"}),
                   ("agents_list", {"limit": 101}), ("agents_list", {"after": ""}),
                   ("tasks_get", {"taskId": "t", "afterSequence": -1}),
                   ("tasks_get", {"taskId": "t", "afterSequence": 1.5})]
        with patch.object(mcp, "request_gateway") as gateway:
            for name, args in invalid:
                with self.subTest(name=name, args=args):
                    self.assertEqual(rpc(name, args)["error"]["code"], -32602)
            gateway.assert_not_called()

    def test_routes_defaults_and_cursor_encoding(self):
        with patch.object(mcp, "request_gateway", return_value={"ok": True}) as gateway:
            rpc("agents_list")
            gateway.assert_called_with("GET", "/agents?limit=50")
            rpc("agents_list", {"after": "agent&other=value", "limit": 10})
            gateway.assert_called_with("GET", "/agents?after=agent%26other%3Dvalue&limit=10")
            rpc("agents_get", {"agentId": "a?x=1"})
            gateway.assert_called_with("GET", "/agents/a%3Fx%3D1")
            rpc("tasks_send", {"agentId": "a", "prompt": "日本語", "idempotencyKey": "stable"})
            gateway.assert_called_with("POST", "/tasks", {"targetAgentId": "a", "prompt": "日本語",
                                                         "idempotencyKey": "stable", "mode": "queue"})
            rpc("tasks_list", {"after": "cursor&other=value"})
            gateway.assert_called_with("GET", "/tasks?after=cursor%26other%3Dvalue&direction=outgoing&limit=50")
            rpc("tasks_get", {"taskId": "t", "afterSequence": 15, "limit": 5})
            gateway.assert_called_with("GET", "/tasks/t?afterSequence=15&limit=5")
            rpc("tasks_cancel", {"taskId": "t"})
            gateway.assert_called_with("POST", "/tasks/t/cancel", {})

    def test_multibyte_prompt_and_retry_key_obey_gateway_byte_limits(self):
        with patch.object(mcp, "request_gateway", return_value={}) as gateway:
            base = {"agentId": "a", "prompt": "あ" * 21845, "idempotencyKey": "あ" * 42}
            self.assertNotIn("error", rpc("tasks_send", base))
            gateway.reset_mock()
            for key, value in (("prompt", "あ" * 21846), ("idempotencyKey", "あ" * 43)):
                with self.subTest(key=key):
                    self.assertEqual(rpc("tasks_send", {**base, key: value})["error"]["code"], -32602)
            gateway.assert_not_called()

    def test_gateway_failures_are_tool_results(self):
        with patch.object(mcp, "request_gateway", side_effect=RuntimeError("not available")):
            response = rpc("agents_list")
        self.assertTrue(response["result"]["isError"])
        self.assertEqual(response["result"]["content"][0]["text"], "not available")

    def test_response_limit_and_non_json_failures(self):
        class Response(io.BytesIO):
            status = 200

        with patch.object(mcp, "UnixHTTPConnection") as connection:
            connection.return_value.getresponse.return_value = Response(b"x" * 21)
            with patch.object(mcp, "MAX_RESPONSE_BYTES", 20):
                self.assertIn("size limit", rpc("agents_list")["result"]["content"][0]["text"])
            connection.return_value.getresponse.return_value = Response(b"not JSON")
            self.assertIn("invalid JSON", rpc("agents_list")["result"]["content"][0]["text"])
            connection.return_value.close.assert_called()

    def test_http_failure_is_visible(self):
        response = io.BytesIO(b'{"error":"target unavailable"}')
        response.status = 409
        with patch.object(mcp, "UnixHTTPConnection") as connection:
            connection.return_value.getresponse.return_value = response
            result = rpc("tasks_send", {"agentId": "a", "prompt": "work", "idempotencyKey": "stable"})
        self.assertTrue(result["result"]["isError"])
        self.assertIn("HTTP 409: target unavailable", result["result"]["content"][0]["text"])

    def test_real_unix_http_roundtrip_preserves_steer_and_idempotency(self):
        received = []

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                received.append((self.path, dict(self.headers), json.loads(self.rfile.read(int(self.headers["Content-Length"])))))
                payload = b'{"task":{"id":"task-1","status":"queued"}}'
                self.send_response(202)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, *_args):
                pass

        with tempfile.TemporaryDirectory() as directory:
            path = str(Path(directory) / "gateway.sock")
            with socketserver.UnixStreamServer(path, Handler) as server:
                thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01})
                thread.start()
                try:
                    with patch.object(mcp, "SOCKET_PATH", path):
                        args = {"agentId": "peer", "prompt": "追加の指示", "mode": "steer", "idempotencyKey": "retry-key"}
                        first = rpc("tasks_send", args)
                        second = rpc("tasks_send", args)
                finally:
                    server.shutdown()
                    thread.join()
        self.assertEqual(first, second)
        self.assertEqual(received[0][2], {"targetAgentId": "peer", "prompt": "追加の指示",
                                         "mode": "steer", "idempotencyKey": "retry-key"})
        self.assertNotIn("Authorization", received[0][1])
        self.assertNotIn("sourceAgentId", received[0][2])

    def test_oversize_stdio_record_does_not_desynchronize_next_request(self):
        data = b"x" * (mcp.MAX_REQUEST_BYTES + 10) + b'\n{"jsonrpc":"2.0","id":3,"method":"ping"}\n'
        result = subprocess.run([sys.executable, str(SCRIPT)], input=data, capture_output=True, check=True)
        responses = [json.loads(line) for line in result.stdout.splitlines()]
        self.assertEqual(responses[0]["error"]["code"], -32600)
        self.assertEqual(responses[1], {"jsonrpc": "2.0", "id": 3, "result": {}})


class ConfigMigrationTests(unittest.TestCase):
    def migrate(self, directory):
        source = (ROOT / "bin/agent-entrypoint").read_text()
        migration = source[source.index('codex_config="'):source.index("# Verify the actual Codex sandbox")]
        migration = migration.replace("codex_config_template=/usr/local/share/codexbot/codex-config.toml",
                                      "codex_config_template=\"$2\"")
        return subprocess.run(["sh", "-ec", 'codex_home="$1"\n' + migration, "migration", directory,
                               str(ROOT / "config/codex-config.toml")], capture_output=True, text=True)

    def test_existing_custom_settings_survive_and_migration_is_idempotent(self):
        existing = ('cli_auth_credentials_store = "file"\nmodel = "custom-model"\n'
                    '[mcp_servers."codexbot_browser"] # custom formatting\ncommand = "/custom/browser"\n'
                    '[mcp_servers.codexbot_collaboration] # intentional override\nenabled = false\n')
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.toml"
            path.write_text(existing)
            result = self.migrate(directory)
            self.assertEqual(result.returncode, 0, result.stderr)
            first = path.read_text()
            self.assertTrue(first.startswith(existing))
            config = tomllib.loads(first)
            self.assertEqual(config["mcp_servers"]["codexbot_browser"]["command"], "/custom/browser")
            self.assertFalse(config["mcp_servers"]["codexbot_collaboration"]["enabled"])
            self.assertIn("codexbot_desktop", config["mcp_servers"])
            self.assertEqual(self.migrate(directory).returncode, 0)
            self.assertEqual(path.read_text(), first)

    def test_new_and_existing_profiles_receive_collaboration(self):
        for existing in (None, 'model = "custom-model"\n'):
            with self.subTest(existing=existing), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "config.toml"
                if existing is not None:
                    path.write_text(existing)
                result = self.migrate(directory)
                self.assertEqual(result.returncode, 0, result.stderr)
                config = tomllib.loads(path.read_text())
                self.assertEqual(config["cli_auth_credentials_store"], "file")
                self.assertEqual(config["mcp_servers"]["codexbot_collaboration"]["command"],
                                 "/usr/local/lib/codexbot/collaboration-mcp.py")
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)


if __name__ == "__main__":
    unittest.main()
