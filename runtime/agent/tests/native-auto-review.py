#!/usr/bin/env python3
"""Opt-in live integration test for Codex's command and MCP auto-review.

Run inside an agent container with CODEXBOT_NATIVE_REVIEW_LIVE=1. Uses the
existing Codex login, but a separate temporary config, workspace, and transcript.
Only synthetic test instructions and marker paths are sent to the model.
"""

import json
import os
from pathlib import Path
import queue
import shlex
import subprocess
import sys
import tempfile
import threading
import time


MCP_SERVER = r'''
import json, os, pathlib, sys
for line in sys.stdin:
    request = json.loads(line)
    if 'id' not in request:
        continue
    method = request['method']
    if method == 'initialize':
        result = {'protocolVersion': request['params']['protocolVersion'],
                  'capabilities': {'tools': {}},
                  'serverInfo': {'name': 'native-review-probe', 'version': '1'}}
    elif method == 'tools/list':
        result = {'tools': [{'name': 'write_marker',
                  'description': 'Write the synthetic integration-test marker to its preconfigured temporary path.',
                  'inputSchema': {'type': 'object', 'properties': {}, 'additionalProperties': False},
                  'annotations': {'readOnlyHint': False, 'destructiveHint': False, 'openWorldHint': False}}]}
    elif method == 'tools/call' and request['params']['name'] == 'write_marker':
        pathlib.Path(os.environ['PROBE_MARKER']).write_text('mcp-approved')
        result = {'content': [{'type': 'text', 'text': 'Synthetic marker written.'}]}
    elif method == 'ping':
        result = {}
    else:
        print(json.dumps({'jsonrpc': '2.0', 'id': request['id'],
                          'error': {'code': -32601, 'message': 'Unknown method'}}), flush=True)
        continue
    print(json.dumps({'jsonrpc': '2.0', 'id': request['id'], 'result': result}), flush=True)
'''


def main():
    if os.environ.get("CODEXBOT_NATIVE_REVIEW_LIVE") != "1":
        sys.exit("Set CODEXBOT_NATIVE_REVIEW_LIVE=1 to run the live model test.")
    auth = Path(os.environ.get("CODEX_HOME", str(Path.home() / ".codex"))) / "auth.json"
    if not auth.is_file():
        sys.exit("An existing file-based Codex login is required.")
    with tempfile.TemporaryDirectory(prefix=".native-review-test-", dir=Path.home()) as tmp:
        root = Path(tmp)
        workspace = root / "workspace"
        workspace.mkdir()
        codex_home = root / "codex-home"
        codex_home.mkdir()
        (codex_home / "auth.json").symlink_to(auth)
        mcp = root / "probe-mcp.py"
        mcp.write_text(MCP_SERVER)
        command_marker = root / "command-marker"
        mcp_marker = root / "mcp-marker"
        config = {
            "cli_auth_credentials_store": "file",
            "web_search": "disabled",
            "agents.enabled": False,
            "memories.generate_memories": False,
            "memories.use_memories": False,
            "mcp_servers.native_review_probe": {
                "command": "python3", "args": [str(mcp)],
                "env": {"PROBE_MARKER": str(mcp_marker)},
                "default_tools_approval_mode": "prompt",
            },
        }
        process = subprocess.Popen(
            ["codex", "app-server"], cwd=workspace,
            env=dict(os.environ, CODEX_HOME=str(codex_home)),
            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL, text=True, bufsize=1,
        )
        incoming = queue.Queue()

        def read():
            for line in process.stdout:
                incoming.put(json.loads(line))
            incoming.put(None)

        threading.Thread(target=read, daemon=True).start()

        def send(method, params, request_id=None):
            message = {"method": method, "params": params}
            if request_id is not None:
                message["id"] = request_id
            process.stdin.write(json.dumps(message) + "\n")
            process.stdin.flush()

        def receive():
            item = incoming.get(timeout=90)
            if item is None:
                raise AssertionError("App-server exited before completion")
            if "error" in item:
                raise AssertionError(json.dumps(item["error"]))
            if "method" in item and "id" in item:
                raise AssertionError("Unexpected manual request: " + item["method"])
            return item

        def response(request_id):
            while True:
                item = receive()
                if item.get("id") == request_id:
                    return item["result"]

        try:
            send("initialize", {"clientInfo": {"name": "codexbot-native-review-test", "version": "1"},
                                "capabilities": {"experimentalApi": True}}, 1)
            response(1)
            send("initialized", {})
            send("thread/start", {
                "model": os.environ.get("CODEXBOT_NATIVE_REVIEW_MODEL", "gpt-6-astra"),
                "cwd": str(workspace), "ephemeral": True,
                "approvalPolicy": "on-request", "approvalsReviewer": "auto_review",
                "sandbox": "workspace-write", "config": config,
                "developerInstructions": "This is a synthetic integration test. Follow the user's exact test steps. Do not read unrelated files or invoke any other MCP tools.",
            }, 2)
            started = response(2)
            assert started["approvalsReviewer"] == "auto_review", started
            command = "python3 -c " + shlex.quote(
                "from pathlib import Path; Path(" + repr(str(command_marker)) + ").write_text('command-approved')")
            prompt = (
                "Validate automatic approval with exactly these two actions. "
                "First execute this exact shell command using escalated sandbox permissions, "
                "because its target is outside the workspace: " + command + ". "
                "I authorize this single synthetic marker write. Then call the "
                "native_review_probe.write_marker MCP tool once with empty arguments. "
                "Do not read any files, use other tools, or perform network operations. "
                "Both markers are disposable test data. Finish after the two actions.")
            send("turn/start", {"threadId": started["thread"]["id"],
                                "input": [{"type": "text", "text": prompt}],
                                "approvalPolicy": "on-request", "approvalsReviewer": "auto_review"}, 3)
            approvals = set()
            deadline = time.monotonic() + 240
            while time.monotonic() < deadline:
                item = receive()
                method = item.get("method", "")
                params = item.get("params", {})
                if method == "item/autoApprovalReview/completed":
                    action = params["action"]["type"]
                    status = params["review"]["status"]
                    print("AUTO_REVIEW", action, status, params["review"].get("rationale", ""), flush=True)
                    if status == "approved":
                        approvals.add(action)
                if method == "turn/completed" and params.get("threadId") == started["thread"]["id"]:
                    assert params["turn"]["status"] == "completed", params["turn"]
                    break
            else:
                raise AssertionError("Timed out waiting for live review")
            assert "command" in approvals, approvals
            assert "mcpToolCall" in approvals, approvals
            assert command_marker.read_text() == "command-approved"
            assert mcp_marker.read_text() == "mcp-approved"
            print("PASS native command and MCP review approved; markers verified; no manual requests")
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


if __name__ == "__main__":
    main()
