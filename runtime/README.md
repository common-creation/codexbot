# Runtime images

Codexbot launches two image types dynamically through the runtime manager:

- `codexbot-agent`: one non-root XFCE/KasmVNC desktop and Codex App Server worker per agent.
- `codexbot-egress-proxy`: one Squid process per agent, used as the only Internet route.

The runtime manager must create a dedicated internal Docker network for each
agent, attach only that agent and its egress proxy, then attach the proxy to an
egress-capable network. The agent gets `HTTP_PROXY`, `HTTPS_PROXY`, and
`ALL_PROXY` pointing at the proxy and must not get a second, direct Internet
route. KasmVNC (`6901`) and agent-worker (`8082`) are never published on a host
port; the control plane reaches them through runtime-manager authenticated
proxies.

## Mount contract

Each agent receives exactly these writable mounts:

| Container path | Backing | Purpose |
| --- | --- | --- |
| `/home/agent` | fixed host bind | shared files and shared `~/.codex` auth/config/sessions |
| `/var/lib/codexbot/profile` | agent-specific volume | browser profile, XFCE state, KasmVNC state, outbox |
| `/tmp`, `/run` | tmpfs | process-local temporary state |

The image otherwise supports a read-only root filesystem and runs as UID/GID
`1000:1000`. Every agent must use the same UID/GID so files in the shared bind
remain mutually editable. With rootless Docker this UID maps to a subordinate
host UID; the host directory remains inspectable but host-side edits may require
an ownership/ACL policy selected by the administrator.

Shared `~/.codex` makes agents mutually trusted: credentials, global Codex
configuration, and session storage are common. Per-agent browser/XFCE volumes
avoid accidental state mixing but do not protect an agent from another agent
that rewrites shared Codex configuration.

Agent containers must use the vendored `runtime/agent/seccomp_profile.json`
instead of Docker's default seccomp profile. It is Playwright's default-Docker
profile plus `clone`, `setns`, and `unshare` permissions needed for Chromium's
non-root user-namespace sandbox. Runtime-manager has the same file at
`/etc/codexbot/chromium-seccomp.json` and passes it to `docker create`; do not
replace it with `seccomp=unconfined` or `--no-sandbox`.
The profile also allows `close_range`, which GLib uses when spawning XFCE
components. Denying it with `EPERM` leaves the session running without its
window manager, panel, or desktop. After changing the profile, rebuild the
runtime-manager image and recreate existing agent containers while retaining
their profile volumes; restarting a container retains its old seccomp policy.
Keep `no-new-privileges:true`, but do not apply `--cap-drop ALL` to the agent:
Chromium needs Docker's default bounding set while constructing its nested
sandbox namespaces. The image's UID 1000 has no effective initial capabilities,
and `no-new-privileges` prevents it from gaining capabilities in the container's
initial namespace. The egress proxy can and should continue dropping all
capabilities.

Check GLib child-process startup under the actual agent seccomp policy from
the repository root (requires a built `codexbot-agent:local` image):

```sh
docker run --rm -i --network none --read-only \
  --security-opt no-new-privileges:true \
  --security-opt "seccomp=$PWD/runtime/agent/seccomp_profile.json" \
  --entrypoint python3 codexbot-agent:local - < runtime/agent/tests/glib-spawn.py
```

KasmVNC requires separate viewer and owner passwords, supplied as secret files:

```text
KASMVNC_VIEWER_PASSWORD_FILE=/run/secrets/kasmvnc_viewer_password
KASMVNC_OWNER_PASSWORD_FILE=/run/secrets/kasmvnc_owner_password
```

The normal desktop user is read-only. Runtime-manager uses the owner credential
with the KasmVNC developer API to grant/revoke the normal user's write permission
during a fenced takeover. Agent X11 automation does not require browser-client
write permission.

## Built-in MCP servers

The image seeds shared `~/.codex/config.toml` on first boot with:

- `/usr/local/lib/codexbot/browser-mcp.mjs` for visible Playwright/Chromium operations. XFCE starts Chromium through `chromium-visible`; Debian's `/etc/chromium.d/codexbot` also applies the shared profile, proxy and loopback-only CDP settings when the preferred-browser helper or terminal starts Chromium directly. The MCP attaches to that session, so human and agent actions share the same windows and profile. If Chromium is closed, use `desktop_open_app` with `app=browser` to reopen it before retrying a snapshot.
- `/usr/local/lib/codexbot/desktop-mcp.py` for screenshots and X11 pointer/keyboard fallback.
- `/usr/local/lib/codexbot/collaboration-mcp.py` for agent discovery, durable task delivery, steering and results; see [agent collaboration](../docs/agent-collaboration.md).

The browser uses the agent-specific profile but downloads into the shared
`/home/agent/Downloads` directory. KasmVNC clipboard and its own file-transfer
path are disabled.

Versions of KasmVNC, Codex CLI, Node, Playwright Core, and Go are explicit build
arguments in `runtime/agent/Dockerfile`. Production releases should additionally
pin base images by digest and refresh package snapshots through the normal image
update process.
