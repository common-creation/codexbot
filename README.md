# Codexbot

| ![https://i.imgur.com/RgHxOCM.png](https://i.imgur.com/RgHxOCM.png) | ![https://i.imgur.com/SGeZB25.png](https://i.imgur.com/SGeZB25.png) |
|---|---|

Codexbot is a single-host, self-hosted workspace for persistent Codex agents.
Every agent receives an isolated XFCE/KasmVNC desktop, Codex App Server process,
browser profile, and runtime network. All agents intentionally share one
administrator-selected home directory.

## What is implemented

- Local administrator setup and Argon2id-backed sessions.
- Agent creation with versioned role instructions, model/reasoning-effort settings, and per-agent permissions.
- Persistent sidebar ordering with drag-and-drop and named agent sections.
- A shared current chat context per agent for manual messages, delegated work,
  and supplemental input. Only the user's **New chat** action resets it.
- ChatGPT device-code and OpenAI API-key login shared through `~/.codex`.
- XFCE/KasmVNC desktop viewing with exclusive human/agent input leases.
- Playwright-first browser automation and X11 desktop fallback MCP tools.
- [Agent collaboration](docs/agent-collaboration.md) through MCP discovery,
  durable task queues, live steering and result retrieval.
- One-time and five-field cron schedules with isolated runs, a detail screen,
  and execution history with output lists separate from chat.
- Shared `/home/agent` bind mount, including `~/.codex`, with per-agent browser/XFCE volumes.
- Rootless Docker runtime manager and per-agent outbound proxy isolation.

At server startup, Codexbot fetches the model catalog from
[Codex models.json](https://raw.githubusercontent.com/openai/codex/main/codex-rs/models-manager/models.json)
once, with a five-second timeout. The agent dialogs use its visible models and
their supported reasoning efforts. If fetching or parsing fails, the built-in
choices remain available. Restart the server to refresh the catalog; custom
model IDs and runtime defaults are also available in both dialogs.

Use **Add section** beside the sidebar's Agents heading to create a group.
Drag an agent's handle to reorder it or move it between sections; clicking the
handle opens a section and position picker for keyboard and touch use. Section
settings support renaming, moving, and deleting sections. Deleting a section
moves its agents to **Unsectioned**. Organization is saved in the workspace
database and shared across browser sessions.

## Agent permissions

Create and edit dialogs offer **Permission: Auto** (default) or **Full Access**.
Both modes run without human permission prompts, for chats and schedules alike.

- **Auto** uses Codex's `workspace-write` sandbox with
  `approval_policy = "on-request"` and `approvals_reviewer = "auto_review"`.
  Codex reviews requests to cross its permission boundary; operations already
  allowed by the sandbox do not require a review. The workspace is `/home/agent`.
- **Full Access** uses `danger-full-access` with `approval_policy = "never"`
  and allows permission requests without a model review.

Native Auto-review activity and decisions appear in the activity history.
Codex selects the reviewer; Codexbot does not pin a separate review model.
Review failures and unexpected requests for human approval are declined;
generic MCP forms requiring user input are also declined. Existing agents
migrate to Auto. Permission changes apply to subsequent turns in the same
conversation and cannot be made while the agent is running or its desktop is
under human control.

## Quick start

Requirements are Linux/amd64, Docker Compose, and a dedicated absolute path for
the shared home. A rootless Docker daemon is the default; rootful Docker is an
explicit, higher-risk opt-in described in the deployment guide.

```sh
cp deploy/.env.example deploy/.env
# Edit deploy/.env with the Docker socket and shared-home paths.
./deploy/scripts/prepare-host.sh
./deploy/scripts/build-images.sh
./deploy/scripts/up.sh
```

Open `http://127.0.0.1:8080`. The one-time bootstrap token is written to
`deploy/secrets/bootstrap-token`. Loopback HTTP uses
`CODEXBOT_SECURE_COOKIES=false`; non-loopback access must use HTTPS and secure
cookies.

See [deployment guidance](deploy/README.md) and
[the architecture](docs/architecture.md) before exposing the service. The
[security model](SECURITY.md) documents the important desktop-takeover and
scheduled-action boundaries.

## Local development

```sh
go test ./...
cd web && npm ci && npm test -- --run && npm run build
go run ./cmd/runtime-manager
go run ./cmd/codexbot
```

The runtime manager requires `CODEXBOT_RUNTIME_TOKEN`, a rootless Docker CLI,
and the configured shared-home path. The public API never receives the Docker
socket.

## Important security boundary

This MVP is for one trusted administrator on one host. Agent containers are not
a suitable boundary for mutually untrusted tenants. Agent operations execute
without human permission prompts. Auto combines Codex's sandbox with its native
reviewer; Full Access grants permission requests without that sandbox. Both
modes retain Docker isolation. Model review is not a containment boundary: a
malicious page can still cause external side effects or data exfiltration. The egress proxy blocks the host, private networks, link-local
addresses, and cloud metadata, but it does not restrict public origins.

The shared home is concurrently writable by every agent. It includes
`~/.codex`, so Codex authentication, global configuration, and session files are
shared. Browser and XFCE state remain in per-agent volumes. Arbitrary
applications may create conflicting dotfiles or edit the same shared file.

## Validation

```sh
make test
make build
docker compose --env-file deploy/.env.example -f deploy/compose.yaml config --quiet
```

Container image builds additionally verify the pinned KasmVNC package checksum
and compile the agent worker into the desktop image.
