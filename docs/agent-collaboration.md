# Agent collaboration

Every agent image includes the `codexbot_collaboration` stdio MCP server for
discovery, durable task delivery, live steering and completion notifications. Delegated
work retains the target's role, model, effort and Permission. Every task delegates
input into the target's existing chat Conversation and Codex App Server thread.
Manual messages and delegated tasks share that agent's current context and
chat timeline. Context resets only when the user chooses **New chat**.
Schedules execute independently and have their own history and output lists.

| Tool | Purpose |
| --- | --- |
| `agents_list` | Agent IDs, names, role prompts, configuration, active run summaries, pending counts and human desktop ownership; includes `selfAgentId`. Page with `after` and `limit`. |
| `agents_get` | One agent's details plus `systemInstructions`: the platform and role text sent as `developerInstructions`, not Codex's built-in base prompt. |
| `tasks_send` | Persist an instruction and return its task ID. Requires `agentId`, `prompt`, `idempotencyKey`; `mode` defaults to `queue`, `completionMode` to `poll`. |
| `tasks_list` | Page through incoming/outgoing tasks, optionally filtering status; pass `nextCursor` as `after`. |
| `tasks_get` | Task/run status and output events; pass `nextSequence` as `afterSequence` while `hasMore` is true. |
| `tasks_cancel` | Cancel a task the caller sent while it is still queued. |

Prompts accept at most 65,536 UTF-8 bytes; idempotency keys at most 128 bytes.
Reuse a key only to retry the same target, mode, completion mode and prompt:
the original receipt is returned even after completion. A conflicting retry
receives HTTP 409.
Self-delegation is rejected. At most 100 queued, dispatching or running tasks
may target one agent; further submissions receive HTTP 429. Task/event pages
accept `limit` from 1 to 100. Agent lists use ID order; task lists are newest
first. Both return `nextCursor`, passed as `after` to continue.

## Queue and steer behavior

`queue` starts a subsequent turn in the target's existing Conversation and
App Server thread when the target becomes idle.
Tasks run in acceptance order, one at a time per target. The pending task table
is separate from `runs`, preserving the one-active-run database constraint.
Different targets dispatch concurrently. New submissions wake the dispatcher;
newly idle targets are also checked every 500 ms. Human desktop ownership pauses
delivery.

`steer` sends a time-sensitive update to the active turn, passing pending
queued tasks when necessary. The worker calls `turn/steer` with the current
`threadId` and `expectedTurnId`. This appends input without starting a new turn,
as specified by the [official app-server documentation](https://learn.chatgpt.com/docs/app-server).
If no turn is active, or a turn definitively rejects the input, the task waits
for a subsequent turn in the same Conversation instead. A rejected steer is not
redirected into a later active turn; its original requested mode remains visible with
`steerFallback: true`.

Task states are `queued`, `dispatching`, `running`, `completed`, `failed`,
`interrupted`, `unknown` and `cancelled`. A receipt is acceptance, not completion.
With the default `completionMode: "poll"`, check results between useful
independent work using `tasks_get` or `tasks_list`. Use `completionMode: "notify"`
to resume from a harness completion message instead.

A task with its own turn has `outputScope: task`. A steered task shares its host
turn's completion and subsequent output (`outputScope: shared_run`), rather
than receiving an independent answer. Events before the worker's steer
submission boundary are excluded. Unconfirmed steer delivery does not expose
the host run's events. `deliveryConfirmed` distinguishes confirmed input from
the final execution outcome, so a confirmed steer still exposes output when its
host run fails. Results include assistant messages and tool progress as
app-server events with a monotonic cursor. Pages are additionally bounded by
encoded byte size. Event payloads above 256 KiB are represented with
`truncated`, `originalBytes` and a 16 KiB `preview`; original events stay in the
database. Continue with the returned cursor even when an event was truncated.

The queue survives restart. In-flight deliveries with uncertain outcomes become
`unknown` and are not automatically resent. The worker fsyncs a per-message
steer ledger before/after its RPC to prevent duplicate accepted input. A task
whose turn start response was lost can recover its final state from its
own run's outbox. A host run's completion cannot prove an unconfirmed steer was
delivered.

Stopping or archiving a target cancels its queued work and uses the existing
stop behavior for its current run. A new task submitted after a stop can start
the target again, like a manual message. Queued work uses the target settings
at dispatch; steering retains the active turn's settings.

## Completion notifications

`completionMode` is independent of `mode`: either queued or steered work can
request a notification. For example:

```json
{
  "agentId": "target-agent-id",
  "prompt": "Inspect the failing test and report the cause.",
  "mode": "queue",
  "completionMode": "notify",
  "idempotencyKey": "inspect-failing-test-1"
}
```

The MCP call returns the receipt immediately; it does not block until the
target finishes. The requester can continue independent work, or finish its
current turn when it is only waiting. The harness sends a message containing
the original task ID and terminal status when that task becomes `completed`,
`failed`, `interrupted`, `unknown` or `cancelled`. On receiving that message,
use `tasks_get` to retrieve the final result, following `nextSequence` while
`hasMore` is true. These reads retrieve available output after the notification;
repeated readiness polling is unnecessary. A terminal notification reports an
outcome, not necessarily success: `failed` reports a task/run failure,
`interrupted` a stopped execution, `cancelled` work cancelled before execution,
and `unknown` an uncertain outcome. In particular, an unconfirmed steer may
never have reached the target, even if its host run subsequently completed.

Notify mode is accepted only from an active manual or collaboration chat run.
Scheduled runs cannot request it. The harness captures the requester's current
Conversation when the task is accepted. If that chat has an active turn when
the notification is dispatched, the message uses `turn/steer`; if it is idle,
the message starts a new turn in the same Conversation and App Server thread.
A definitively rejected steer falls back to a queued turn. Human desktop
ownership postpones delivery. An active scheduled run also postpones delivery
until the chat can resume; notifications never enter a scheduled run.

Notifications are durable reverse tasks with `mode: "steer"` and
`completionMode: "poll"`, so they survive restart without creating notification
loops. The original receipt's `completionTaskId` links to its notification
receipt; that receipt's `notificationForTaskId` links back to the original.
Notification delivery has its own status, distinct from the original task's
outcome. Reading a notification task with `tasks_get` returns
`outputScope: "notification"` and the delivery receipt, without the requester's
continuation output. Use its `notificationForTaskId` to read the original
delegated result. Uncertain notification delivery becomes `unknown` and is not resent
automatically, because the input may already have reached the requester.
These harness-generated receipts cannot be cancelled with `tasks_cancel`;
stopping the requester cancels its pending continuations.

Choosing **New chat** prevents an old notification from entering the new
context; its notification task is cancelled. Stopping or archiving the
requester (including **Stop run**) suppresses obsolete callbacks, including callbacks for delegated
work that finishes later. This does not turn a failed or suppressed callback
into successful delivery: inspect the task receipts when diagnosing a missing
continuation. An explicit new delegation can request notifications again.

## Chat timeline and supplemental input

Open an agent's chat to see its manual and delegated inputs together
with the executing agent's tool activity and assistant responses. Incoming tasks
show their sender and task ID. Both agents have a delegation status entry that
links to the other agent's timeline. Target output remains in the target's chat.
A completion notification carries the task ID and status rather than copying
all output; the requester uses `tasks_get` to read the result and continue its
own work.

The chat stays live while the agent is idle and discovers newly delegated
work automatically. Earlier events are available through **Load
earlier messages**. An active chat keeps **Send message** available alongside
**Stop run**. Sending supplemental text or attachments adds input to the active
App Server turn using `turn/steer`; for example, a person can specify another
directory to inspect while a delegated task runs. Accepted supplemental inputs
are recorded in the timeline; an unsuccessful submission leaves the draft and
attachments available to retry. The MCP `tasks_send` interface accepts text;
attachment submission is available through the chat composer.

Each agent retains its current Conversation when its role or model settings
change. **New chat** explicitly resets that context using the conversation-
creation API: the next manual or delegated input starts a fresh
thread, and subsequent inputs share it. Pending delegated instructions use the
current context at dispatch; completion notifications are bound to the
originating context and are cancelled after a reset. Previous messages stay
visible behind a reset marker but are not included in the new model context.
Reset is blocked during active work or human
desktop control. A stale browser submission against the previous conversation
is rejected so the draft can be reviewed and resent after refreshing.
Existing manual/delegated context is preserved, including its thread ID.
Historical scheduled entries are excluded from chat but retained in schedule
execution history. Old model threads are not merged or reset automatically.

## Scheduled execution history

Schedules use a fresh conversation/thread on every execution and do not change
the user's chat context. While a scheduled run owns the agent, chat Send is
unavailable (the draft and attachments stay editable). A delegated steer waits
in the queue instead of entering the schedule's context. Completion does not
automatically send the user's draft or start a new chat.

Select a schedule name to open its details and execution history. Each list row
shows its scheduled time, status and errors; expanding it displays the original
input and the output/tool activity, updating while the run is active. Older
executions and long outputs are paginated. Output truncation is explicit.

The authenticated APIs are `GET /api/schedules/{id}`,
`GET /api/schedules/{id}/runs?before={runId}&limit=20`, and
`GET /api/schedules/{id}/runs/{runId}/events?after=0&limit=100`. Run output and
history cursors are scoped to the requested schedule. Worker events remain
durable even though they are not projected into the chat timeline.

## Transport and authority

MCP sends HTTP over `/run/codexbot/collaboration.sock` in an agent-owned bounded
tmpfs. The worker adds its own identity and in-memory token. The runtime manager
verifies the token for that sender, then forwards only collaboration routes to
the control plane using its runtime credential. MCP receives neither token.
Internal requests bypass the Internet proxy and do not follow redirects.

Discovery exposes role text by design. Task/result access is limited to the
authenticated sender or recipient; only the sender can cancel pending work.
Browser cookies do not authorize internal routes. Shared `/home/agent` and
Codex configuration remain a common trust domain, not hostile-agent isolation.

## Rollout and verification

Build/deploy control-plane, runtime-manager and agent images together. Compose
sets `CODEXBOT_CONTROL_PLANE_URL=http://codexbot:8080` on the manager; the manager
injects `CODEXBOT_COLLABORATION_URL` and the private socket tmpfs into agents.
Existing agents need recreation with the new image and mounts. Ordinary restart
retains old configuration. Preserve profile volumes/shared home; image
reconciliation performs normal recreation on the next agent start.

Startup adds absent MCP sections to shared `~/.codex/config.toml` and preserves
existing overrides, including `enabled=false`. Verify the recreated agent has
`codexbot_collaboration` and six tools in its `tools/list` response.

Tests cover store reopen/FIFO/idempotency, authentication, queue/steer/results,
canonical Conversation reuse, history migration and pagination, idle timeline
updates, supplemental input, real Unix socket forwarding, and app-server wire
behavior with durable delivery replay. Completion-mode schema/default/validation
and retry payload forwarding are exercised through the MCP bridge. Store and
control-plane tests exercise notification creation and dispatch, requester
context binding and obsolete callback suppression. These local tests and
simulated app-server tests do not establish live model adherence, deployed
continuation behavior or production deployment.
