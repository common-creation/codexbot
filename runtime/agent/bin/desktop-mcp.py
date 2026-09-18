#!/usr/bin/env python3
"""Small stdio MCP server for deterministic X11 desktop operations."""

import base64
import json
import os
import subprocess
import sys
import time
import urllib.request
from typing import Any


SERVER_INFO = {"name": "codexbot-desktop", "version": "0.1.0"}
LEASE_FILE = os.environ.get("CODEXBOT_DESKTOP_LEASE_FILE", "/var/lib/codexbot/profile/desktop-lease.json")
GENERATION = {"type": "integer", "minimum": 1, "description": "Generation returned by desktop_screenshot."}
TOOLS = [
    {
        "name": "desktop_screenshot",
        "description": "Capture the complete current X11 desktop as a PNG.",
        "inputSchema": {"type": "object", "properties": {}, "additionalProperties": False},
    },
    {
        "name": "desktop_click",
        "description": "Move the pointer and click a mouse button at desktop coordinates.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "x": {"type": "integer", "minimum": 0},
                "y": {"type": "integer", "minimum": 0},
                "button": {"type": "integer", "minimum": 1, "maximum": 3, "default": 1},
                "generation": GENERATION,
            },
            "required": ["x", "y", "generation"],
            "additionalProperties": False,
        },
    },
    {
        "name": "desktop_drag",
        "description": "Drag from one desktop coordinate to another.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "fromX": {"type": "integer", "minimum": 0},
                "fromY": {"type": "integer", "minimum": 0},
                "toX": {"type": "integer", "minimum": 0},
                "toY": {"type": "integer", "minimum": 0},
                "durationMs": {"type": "integer", "minimum": 0, "maximum": 5000, "default": 300},
                "generation": GENERATION,
            },
            "required": ["fromX", "fromY", "toX", "toY", "generation"],
            "additionalProperties": False,
        },
    },
    {
        "name": "desktop_scroll",
        "description": "Scroll vertically at the current pointer position.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "delta": {"type": "integer", "minimum": -100, "maximum": 100},
                "generation": GENERATION,
            },
            "required": ["delta", "generation"],
            "additionalProperties": False,
        },
    },
    {
        "name": "desktop_type",
        "description": "Type literal text into the currently focused control.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "text": {"type": "string", "maxLength": 20000},
                "delayMs": {"type": "integer", "minimum": 0, "maximum": 1000, "default": 10},
                "generation": GENERATION,
            },
            "required": ["text", "generation"],
            "additionalProperties": False,
        },
    },
    {
        "name": "desktop_keypress",
        "description": "Press an X11 key chord such as ctrl+l or Return.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "keys": {"type": "string", "pattern": "^[A-Za-z0-9_+ -]{1,100}$"},
                "generation": GENERATION,
            },
            "required": ["keys", "generation"],
            "additionalProperties": False,
        },
    },
    {
        "name": "desktop_wait",
        "description": "Wait briefly for a desktop action to settle.",
        "inputSchema": {
            "type": "object",
            "properties": {"milliseconds": {"type": "integer", "minimum": 0, "maximum": 30000}},
            "required": ["milliseconds"],
            "additionalProperties": False,
        },
    },
    {
        "name": "desktop_open_app",
        "description": "Open an allowlisted desktop application.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "app": {"type": "string", "enum": ["browser", "files", "terminal"]},
                "generation": GENERATION,
            },
            "required": ["app", "generation"],
            "additionalProperties": False,
        },
    },
]


def run(argv: list[str], timeout: float = 15.0) -> bytes:
    env = dict(os.environ)
    env.setdefault("DISPLAY", ":1")
    # Codex filters the environment inherited by stdio MCP servers. KasmVNC
    # stores its X11 cookie in the private profile, outside the shared HOME.
    env.setdefault("XAUTHORITY", os.path.join(
        env.get("KASMVNC_HOME", "/var/lib/codexbot/profile/kasmvnc"), ".Xauthority"
    ))
    completed = subprocess.run(
        argv,
        env=env,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=timeout,
        check=False,
    )
    if completed.returncode != 0:
        message = completed.stderr.decode("utf-8", errors="replace").strip()
        raise RuntimeError(message or f"command failed with exit code {completed.returncode}")
    return completed.stdout


def text_result(message: str) -> dict[str, Any]:
    return {"content": [{"type": "text", "text": message}]}


def read_lease() -> dict[str, Any]:
    try:
        with open(LEASE_FILE, "r", encoding="utf-8") as handle:
            lease = json.load(handle)
    except (OSError, ValueError) as exc:
        raise RuntimeError("desktop lease is unavailable") from exc
    if lease.get("holder") not in ("agent", "human") or not isinstance(lease.get("generation"), int):
        raise RuntimeError("desktop lease is invalid")
    return lease


def require_agent_lease(arguments: dict[str, Any]) -> dict[str, Any]:
    lease = read_lease()
    expected = arguments.get("generation")
    if lease["holder"] != "agent":
        raise RuntimeError("desktop control is held by a human")
    if expected != lease["generation"]:
        raise RuntimeError("stale desktop lease generation; capture a new screenshot")
    return lease


def call_tool(name: str, arguments: dict[str, Any]) -> dict[str, Any]:
    if name == "desktop_screenshot":
        data = run(["import", "-window", "root", "png:-"])
        lease = read_lease()
        return {
            "content": [
                {"type": "image", "data": base64.b64encode(data).decode("ascii"), "mimeType": "image/png"},
                {"type": "text", "text": json.dumps({"desktopLease": lease}, separators=(",", ":"))},
            ]
        }
    if name == "desktop_click":
        require_agent_lease(arguments)
        run(["xdotool", "mousemove", "--sync", str(arguments["x"]), str(arguments["y"]), "click", str(arguments.get("button", 1))])
        return text_result("click completed")
    if name == "desktop_drag":
        require_agent_lease(arguments)
        duration_ms = int(arguments.get("durationMs", 300))
        steps = max(1, min(100, duration_ms // 10))
        run([
            "xdotool", "mousemove", "--sync", str(arguments["fromX"]), str(arguments["fromY"]),
            "mousedown", "1", "mousemove", "--sync", "--delay", str(max(1, duration_ms // steps)),
            str(arguments["toX"]), str(arguments["toY"]), "mouseup", "1",
        ])
        return text_result("drag completed")
    if name == "desktop_scroll":
        require_agent_lease(arguments)
        delta = int(arguments["delta"])
        if delta:
            button = "4" if delta > 0 else "5"
            run(["xdotool", "click", "--repeat", str(abs(delta)), "--delay", "20", button])
        return text_result("scroll completed")
    if name == "desktop_type":
        require_agent_lease(arguments)
        run(["xdotool", "type", "--clearmodifiers", "--delay", str(arguments.get("delayMs", 10)), "--", arguments["text"]], timeout=30.0)
        return text_result("text entered")
    if name == "desktop_keypress":
        require_agent_lease(arguments)
        keys = arguments["keys"]
        if not all(character.isalnum() or character in "_+ -" for character in keys):
            raise ValueError("unsupported key chord")
        run(["xdotool", "key", "--clearmodifiers", keys])
        return text_result("key press completed")
    if name == "desktop_wait":
        time.sleep(int(arguments["milliseconds"]) / 1000.0)
        return text_result("wait completed")
    if name == "desktop_open_app":
        require_agent_lease(arguments)
        if arguments["app"] == "browser":
            try:
                # A visible Chromium window may belong to a session started
                # without CDP. Only reuse a browser we can also control.
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                with opener.open("http://127.0.0.1:9222/json/version", timeout=2) as response:
                    if not json.load(response).get("webSocketDebuggerUrl"):
                        raise ValueError("browser debugging endpoint is unavailable")
                windows = run(["xdotool", "search", "--onlyvisible", "--class", "[Cc]hromium"]).decode("ascii").split()
                if windows:
                    run(["xdotool", "windowactivate", "--sync", windows[-1]])
                    return text_result("focused browser")
            except (RuntimeError, OSError, ValueError):
                pass
            subprocess.Popen(
                ["/usr/local/lib/codexbot/chromium-visible", "about:blank"],
                env=dict(os.environ),
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
            return text_result("opened browser")
        commands = {
            "files": ["thunar"],
            "terminal": ["xfce4-terminal"],
        }
        subprocess.Popen(commands[arguments["app"]], env=dict(os.environ), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return text_result(f"opened {arguments['app']}")
    raise ValueError(f"unknown tool: {name}")


def respond(identifier: Any, result: Any = None, error: Any = None) -> None:
    response: dict[str, Any] = {"jsonrpc": "2.0", "id": identifier}
    if error is None:
        response["result"] = result
    else:
        response["error"] = error
    sys.stdout.write(json.dumps(response, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def main() -> None:
    for raw_line in sys.stdin:
        try:
            request = json.loads(raw_line)
            identifier = request.get("id")
            method = request.get("method")
            if identifier is None:
                continue
            if method == "initialize":
                protocol = request.get("params", {}).get("protocolVersion", "2025-06-18")
                respond(identifier, {"protocolVersion": protocol, "capabilities": {"tools": {}}, "serverInfo": SERVER_INFO})
            elif method == "ping":
                respond(identifier, {})
            elif method == "tools/list":
                respond(identifier, {"tools": TOOLS})
            elif method == "tools/call":
                params = request.get("params", {})
                try:
                    respond(identifier, call_tool(params.get("name", ""), params.get("arguments", {})))
                except Exception as exc:  # MCP tool failures are regular tool results.
                    respond(identifier, {"content": [{"type": "text", "text": str(exc)}], "isError": True})
            else:
                respond(identifier, error={"code": -32601, "message": "method not found"})
        except Exception as exc:
            respond(None, error={"code": -32700, "message": str(exc)})


if __name__ == "__main__":
    main()
