# Security model

Codexbot's current release is a single-administrator, single-host system. Agent
containers isolate agents from the host and from each other's private profiles;
they are not a boundary for mutually hostile tenants.

## Desktop takeover limitation

The desktop lease generation is enforced by the bundled browser and desktop MCP
tools. It prevents delayed or stale actions traveling through those supported
tool paths after a human takes control. Taking control also interrupts the
active Codex turn.

This is a coordination fence, not a hostile-code security boundary. Codex and
the desktop currently run in the same agent container. A command that
deliberately bypasses the MCP tools can address X11 or Chromium CDP directly,
and a previously detached background process may outlive a turn interruption.
Do not use takeover while running untrusted shell workloads and assume it
provides adversarial isolation.

Hard multi-tenant fencing requires splitting Codex and the desktop into separate
containers or microVMs, placing X11/CDP behind an authenticated desktop broker,
and allowing no direct display or browser-control path from the Codex runtime.

## Automatic permissions and external actions

Chats and schedules both use the agent's Permission setting, with no human
approval prompts. Auto uses Codex's `workspace-write` sandbox rooted at
`/home/agent`, `on-request` approval policy, and native `auto_review` reviewer.
Review failures and unexpected human approval requests are declined. Generic
MCP forms requiring user input are declined in both modes. Full Access uses
`danger-full-access` and `never`, allowing permission requests immediately.

Both modes retain the agent container and outbound proxy. Bubblewrap runs
inside the container using unprivileged user namespaces; the agent seccomp
profile permits `mount`, `umount2`, and `pivot_root` for sandbox setup. The
container retains its read-only root filesystem, dropped capabilities, and
`no-new-privileges`; it does not receive `SYS_ADMIN` or an unconfined seccomp
profile.

Auto-review is an additional decision layer, not a guarantee against prompt
injection. Commands already allowed by the Codex sandbox execute without review.
The sandbox controls Codex command execution; it does not wrap independently
running browser or desktop MCP servers. Prompt injection from a page can
cause writes or exfiltrate data from the shared home, including the shared
`~/.codex` credentials. The egress proxy
blocks host/private/metadata destinations but does not restrict public origins.
Use a dedicated Codex account, use dedicated low-privilege external accounts,
and keep unrelated sensitive host files outside the shared-home path.

All agents are one mutual-trust domain. The shared `~/.codex/config.toml` is
writable by every agent; an agent can change Codex configuration, MCP commands,
or other shared state that a different agent loads later. Per-agent browser and
desktop volumes prevent accidental profile mixing, but they are not a security
boundary against another Codexbot agent.

## Reporting

Do not include API keys, ChatGPT device codes, browser cookies, runtime tokens,
Kasm credentials, or shared-home contents in a report. Reproduce issues with
fresh disposable credentials.
