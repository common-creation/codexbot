# Architecture

## Trust and process model

The public `codexbot` service owns the SQLite database, HTTP session, scheduler,
and React application. It calls `runtime-manager` over a private Compose
network. Only runtime-manager can access the dedicated rootless Docker socket.
It accepts agent lifecycle operations rather than arbitrary Docker options.
The public service also joins a separate non-internal `edge` network so Docker
can publish its loopback HTTP port; Runtime Manager remains on the internal
`control` network only.

For each agent, runtime-manager creates:

1. An internal-only network.
2. A profile volume mounted at `/var/lib/codexbot/profile`.
3. An egress proxy attached to the internal network and the global egress
   network.
4. An agent container attached only to the internal network.

Creating an Agent eagerly starts this runtime. A successful create response is
`running`; a provisioning failure is persisted as `error` so the UI does not
misrepresent a failed runtime as merely offline.

The agent container runs XFCE, KasmVNC, Chromium, the MCP tools, and
`agent-worker`. Agent-worker owns a stdio `codex app-server` child and exposes a
small authenticated internal API. App Server TCP WebSockets and raw JSON-RPC
are never exposed to the browser.

## Storage

The fixed host directory is mounted read-write at `/home/agent` in every agent
container. This sharing is intentional and is not an isolation boundary. Codex
uses its default `/home/agent/.codex` home there, so authentication, global
configuration, and session storage are shared by all agents. Credential storage
is forced to the file-backed `~/.codex/auth.json` so it cannot silently move to
an agent-local keyring.

The per-agent profile volume contains:

- `chromium/`: the agent's persistent Chromium profile.
- `xdg/`: XFCE and application config/data/cache/state.
- `kasmvnc/`: server credentials and X11 state.
- `outbox/`: durable Codex event batches and terminal run status.
- `steering/`: durable per-message steer delivery records preventing duplicate input.

The private volume separates storage locations, not mutually hostile agents.
Because every agent can modify shared `~/.codex/config.toml`, all agents belong
to the same trust domain and can influence configuration loaded by one another.

The container root is read-only. `/tmp` and `/run` are bounded tmpfs mounts.
The agent seccomp profile allows `mount`, `umount2`, and `pivot_root` so Codex's
Bubblewrap sandbox can set up its filesystem inside an unprivileged user
namespace. Docker isolation remains in place, with dropped capabilities and
`no-new-privileges`; no `SYS_ADMIN` capability or unconfined seccomp profile is
required. The host must support unprivileged user namespaces.

## Conversation mapping

Each agent has one current Conversation and one current Codex App Server
thread, which **New chat** can replace. Agent names remain application metadata. A role prompt is combined
with fixed runtime guidance and sent as `developerInstructions`; the working
agent keeps Codex `baseInstructions`.

Manual messages, delegated tasks, and chat desktop continuations all use
that same Conversation and resume its thread. Queueing a task starts a later
turn in the target's existing context. It does not create a separate task chat.
When the agent is already executing, a manual message adds information through
`turn/steer`, including any submitted attachments. This allows a person to add
context such as a directory to inspect while delegated work is in progress.
Only one turn or human input lease may own a given agent desktop at a time.

Role, model, effort, and Permission changes increment the role version but
preserve the Conversation and thread. Subsequent turns apply the saved settings;
steering retains the current turn's settings. **New chat** calls
`POST /api/agents/{agentID}/conversations` to create a fresh current Conversation
with no thread ID, so the next input starts a new App Server thread. Previous
messages stay visible in the agent timeline behind a reset marker, but are not
replayed into the new context. New chat is blocked during an active run or human
desktop ownership. Pending tasks use the current context when dispatched.

`GET /api/agents/{agentID}/timeline` exposes the agent's durable input, execution
output, and delegation status in a single chronological timeline. Canonical
`message.user` events record each initial input and each accepted steer; raw
App Server user-message notifications are not displayed a second time. Tool
progress, command output, and assistant responses stay on the executing agent's
timeline. The requester receives a delegation status entry with a link to the
target agent, and can retrieve output using the collaboration MCP tools.

The browser polls the timeline every 750 ms even while idle, so work submitted
by another agent appears without selecting or reloading a run. Scheduled work
has a separate execution history and does not feed this timeline.
Responses include the canonical Conversation ID, active Run ID, and a durable
global sequence cursor. The initial page contains the latest 100 events; older
pages can be loaded with `before`, and live pages use `after`. Pagination and
reconnection use the stored sequence rather than a single run's event stream.

Migration retains historical inputs and outputs from all previous Conversation
kinds in the timeline. It chooses one existing context for future work, favoring
an active run's Conversation, otherwise the latest manual Conversation, then
another existing Conversation. A new agent gets a new canonical Conversation.
Historical model threads are not merged: displaying their prior events does
not add their context to the selected App Server thread.

App Server notifications are written to the worker outbox, relayed through
runtime-manager, and projected into SQLite run events and the agent timeline.
A worker restart during a turn marks that run `unknown`; it is not automatically
replayed because an external side effect may already have happened.

## Permission decisions

Agent discovery and delegated tasks use a separate authenticated collaboration
path through the worker and runtime manager. A durable SQLite task queue feeds
subsequent turns in the target's conversation or steers its active turn while
retaining that turn's settings. See
[agent collaboration](agent-collaboration.md) for delivery, results and recovery.

Each Agent stores `permission` as `auto` (default, including migrated agents) or
`full-access`. The control plane forwards this value on every turn, including
resumed threads and desktop continuations. No HTTP or UI path accepts a human
permission decision.

Auto sets `approvalPolicy: "on-request"`, `approvalsReviewer: "auto_review"`,
and the `workspace-write` sandbox on both new and resumed threads. Every turn
also carries the policy, reviewer, and a `workspaceWrite` sandbox policy with
`/home/agent` as its writable root and sandbox network access disabled. This
prevents resumed threads from retaining a previous Full Access configuration.

Codex owns automatic approval decisions and selects its reviewer model.
Operations already allowed by the sandbox run without an approval review.
Codexbot relays the native `item/autoApprovalReview/started` and `completed`
notifications through its durable event pipeline to the activity history.
Review failures are declined by Codex; any approval request that still reaches
the worker in Auto is declined without opening a user prompt.

Full Access sets `approvalPolicy: "never"`, `approvalsReviewer: "user"`, and
`danger-full-access`, and accepts permission requests immediately. Generic MCP
forms requesting data are declined in both modes rather than answered with
invented input. Docker isolation, outbound proxy filtering, and desktop leases
remain independent of the Permission setting. Codex's command sandbox does not
sandbox separately running browser and desktop MCP servers.

## Desktop control

KasmVNC is reachable only through the authenticated control-plane proxy.
Agent-active sessions use a read-only Kasm user. Human takeover interrupts the
turn, increments the fencing generation, writes the atomic lease file, and
reconnects as the Kasm owner. Releasing control increments the generation again.
The WebUI renews a human lease every minute; an abandoned lease expires after
15 minutes and returns control to the agent. A fresh continuation turn starts
only when takeover actually interrupted an active Run; manual desktop use with
no prior Run releases the lease without creating a Thread or Turn.

Browser and desktop MCP mutating tools require the current agent-held lease
generation. Screenshots and snapshots remain available for observation. Browser
tools use Playwright and the visible persistent Chromium profile; X11 tools are
the fallback for non-DOM interfaces.

Deleting an agent is a recoverable archive operation: its runtime is stopped,
its schedules and desktop leases are disabled, and it is removed from the UI.
Shared files and the private profile are retained on the host.

## Scheduling semantics

- Supported forms: RFC3339 one-time execution and standard five-field cron.
- Default timezone: `Asia/Tokyo`.
- A run is unique by schedule source and scheduled timestamp.
- Claiming an occurrence, advancing the schedule, and inserting its fresh
  scheduled Conversation and Run audit record occur in one SQLite transaction.
- One pending occurrence waits up to 15 minutes while an agent is busy; later
  overlap is recorded as skipped.
- Every scheduled execution starts a fresh App Server thread. It neither reads
  nor resets the current manual/delegated chat context. Its input, output and
  status are retained in the schedule's execution history, outside chat.
- Schedule detail screens show each run in a paginated list with dates, status,
  errors, and expandable live output. Runs are ordered by scheduled time, newest
  first. Failed, interrupted, unknown and skipped occurrences remain visible.
- Chat inputs do not steer scheduled runs. During a schedule, manual submission
  is unavailable and preserves the draft; delegated inputs remain queued until
  the chat can execute. The saved model, effort and Permission still apply.
- A run that might have crossed a process failure becomes `unknown` and is not
  automatically retried.

## Network boundary

Agents have no direct default route. HTTP and HTTPS clients use the per-agent
Squid proxy. The proxy denies loopback, private, link-local, reserved,
documentation, multicast, and metadata destinations after DNS resolution.
Non-HTTP protocols are outside the MVP.

The runtime manager should use a Docker daemon dedicated to Codexbot. Even a
rootless Docker socket gives the manager complete control over that daemon's
containers and volumes.

If Codex App Server exits unexpectedly, agent-worker marks the active run
`unknown`, fails health, exits, and lets Docker's `unless-stopped` policy restart
the Agent container. User-initiated stops first terminalize any active Run and
cancel its event relay.
