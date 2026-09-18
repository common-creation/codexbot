import type { AgentEvent } from "./types";

export type ChatEntry =
  | { id: string; kind: "context-reset"; text: string; at?: string }
  | { id: string; kind: "collaboration"; title: string; text?: string; status?: string; taskId: string; agentId?: string; at?: string }
  | { id: string; kind: "user" | "assistant"; text: string; streaming?: boolean; at?: string; senderAgentId?: string; taskId?: string }
  | {
      id: string;
      kind: "activity";
      title: string;
      detail?: string;
      status: "running" | "done" | "failed" | "waiting";
      at?: string;
    }
  | { id: string; kind: "notice"; text: string; severity?: "warning" | "error"; at?: string };

export interface StreamState {
  entries: ChatEntry[];
  lastSequence: number;
  runActive: boolean;
}

export const initialStreamState: StreamState = {
  entries: [],
  lastSequence: 0,
  runActive: false,
};
export const entriesFromHistory = (events: AgentEvent[], agentId?: string): ChatEntry[] =>
  events.reduce((state, event) => applyAgentEvent(state, event, agentId), initialStreamState).entries;

const textFrom = (payload: Record<string, unknown>): string => {
  const item = typeof payload.item === "object" && payload.item ? payload.item as Record<string, unknown> : undefined;
  const value = payload.text ?? payload.delta ?? payload.message ?? payload.content ?? item?.text ?? item?.content ?? item?.aggregatedOutput ?? item?.output ?? "";
  return typeof value === "string" ? value : JSON.stringify(value);
};

const idFrom = (event: AgentEvent): string =>
  `${event.runId}:` + String(event.payload.itemId ?? nestedItem(event.payload)?.id ?? event.payload.approvalId ?? event.payload.id ?? `event-${event.sequence}`);

const nestedItem = (payload: Record<string, unknown>): Record<string, unknown> | undefined =>
  typeof payload.item === "object" && payload.item ? payload.item as Record<string, unknown> : undefined;

export function applyAgentEvent(state: StreamState, event: AgentEvent, agentId?: string): StreamState {
  if (event.sequence <= state.lastSequence) return state;
  const type = event.type.toLowerCase();
  const id = idFrom(event);
  const item = nestedItem(event.payload);
  const itemType = String(item?.type ?? "").toLowerCase();
  const itemStatus = String(item?.status ?? "").toLowerCase();
  let entries = state.entries;
  let runActive = state.runActive;

  if (type === "conversation.reset") {
    entries = appendUnique(entries, { id, kind: "context-reset", text: textFrom(event.payload), at: event.createdAt });
  } else if (["collaboration.sent", "collaboration.received", "collaboration.status"].includes(type)) {
    const taskId = String(event.payload.taskId ?? "");
    const received = type === "collaboration.received" || (agentId !== undefined && agentId === event.payload.targetAgentId);
    const taskEntryId = `collaboration-${taskId}`;
    const previous = entries.find((entry) => entry.id === taskEntryId && entry.kind === "collaboration");
    const next: ChatEntry = {
      id: taskEntryId, kind: "collaboration", taskId,
      title: received ? "Task received" : "Task delegated",
      text: String(event.payload.error || event.payload.prompt || (previous?.kind === "collaboration" ? previous.text : "") || ""),
      status: typeof event.payload.status === "string" ? event.payload.status : undefined,
      agentId: String((received ? event.payload.senderAgentId : event.payload.targetAgentId) ?? ""),
      at: previous?.at ?? event.createdAt,
    };
    entries = previous ? entries.map((entry) => entry.id === taskEntryId ? next : entry) : [...entries, next];
  } else if (["message.user", "user_message", "item.user"].includes(type)) {
    entries = appendUnique(entries, {
      id, kind: "user", text: textFrom(event.payload), at: event.createdAt,
      ...(typeof event.payload.senderAgentId === "string" ? { senderAgentId: event.payload.senderAgentId } : {}),
      ...(typeof event.payload.taskId === "string" ? { taskId: event.payload.taskId } : {}),
    });
  // Canonical message.user events cover run inputs and accepted steers; raw userMessage items would duplicate them.
  } else if (["message.assistant.delta", "assistant_message.delta", "item.assistant.delta", "item/agentmessage/delta"].includes(type)) {
    entries = resolveConnectionIssue(entries, event.runId, false);
    entries = appendAssistantDelta(entries, id, textFrom(event.payload), event.createdAt);
    runActive = true;
  } else if (
    ["message.assistant.completed", "assistant_message", "item.assistant.completed", "item/agentmessage/completed"].includes(type)
      || (type === "item/completed" && itemType === "agentmessage")
  ) {
    entries = resolveConnectionIssue(entries, event.runId, false);
    entries = completeAssistant(entries, id, textFrom(event.payload), event.createdAt);
  } else if (type === "item/commandexecution/outputdelta") {
    const existing = entries.find((entry) => entry.id === id && entry.kind === "activity");
    entries = upsertActivity(entries, id, {
      title: existing?.kind === "activity" ? existing.title : "Command",
      detail: `${existing?.kind === "activity" ? existing.detail ?? "" : ""}${textFrom(event.payload)}`,
      status: "running", at: existing?.at ?? event.createdAt,
    });
  } else if (["tool.started", "command.started", "computer.started"].includes(type) || (type === "item/started" && isActivityItem(itemType))) {
    entries = resolveConnectionIssue(entries, event.runId, false);
    entries = upsertActivity(entries, id, {
      title: String(event.payload.title ?? event.payload.tool ?? event.payload.command ?? item?.tool ?? item?.command ?? "Working"),
      detail: textFrom(event.payload),
      status: "running",
      at: event.createdAt,
    });
    runActive = true;
  } else if (["tool.completed", "command.completed", "computer.completed"].includes(type) || (type === "item/completed" && isActivityItem(itemType))) {
    const failed = event.payload.success === false || ["failed", "declined", "cancelled", "canceled"].includes(itemStatus);
    entries = upsertActivity(entries, id, {
      title: String(event.payload.title ?? event.payload.tool ?? event.payload.command ?? item?.tool ?? item?.command ?? "Task"),
      detail: (item ? (failed ? diagnosticText(item) : "") || toolResultText(item) : "") || textFrom(event.payload),
      status: failed ? "failed" : "done",
      at: event.createdAt,
    });
  } else if (["item/autoapprovalreview/started", "item/autoapprovalreview/completed"].includes(type)) {
    const reviewing = type.endsWith("/started");
    const review = objectFrom(event.payload.review);
    const status = String(review?.status ?? "");
    const title = reviewing ? "Checking tool safety"
      : status === "approved" ? "Permission automatically allowed"
      : status === "denied" ? "Permission automatically denied"
      : status === "timedOut" ? "Automatic permission review timed out"
      : status === "aborted" ? "Automatic permission review aborted"
      : "Automatic permission review failed";
    // A command can have several reviews; reviewing it must not replace its tool activity.
    const reviewId = `auto-review-${event.runId}-${event.payload.reviewId ?? event.sequence}`;
    entries = upsertActivity(entries, reviewId, {
      title,
      detail: autoReviewDetail(event.payload),
      status: reviewing ? "running" : status === "approved" ? "done" : "failed",
      at: event.createdAt,
    });
    if (reviewing) runActive = true;
  } else if (type === "approval.reviewing") {
    entries = upsertActivity(entries, id, {
      title: "Checking tool safety",
      detail: approvalDetail(event.payload),
      status: "running",
      at: event.createdAt,
    });
    runActive = true;
  } else if (["approval.autoaccepted", "approval.autodeclined"].includes(type)) {
    const accepted = type === "approval.autoaccepted";
    entries = upsertActivity(entries, id, {
      title: accepted ? "Permission automatically allowed" : "Permission automatically denied",
      detail: typeof event.payload.reason === "string" && event.payload.reason.trim() !== ""
        ? event.payload.reason
        : approvalDetail(event.payload),
      status: accepted ? "done" : "failed",
      at: event.createdAt,
    });
  } else if (type === "approval.requested") {
    entries = upsertActivity(entries, id, {
      title: "Permission requested",
      detail: textFrom(event.payload) || approvalDetail(event.payload),
      status: "waiting",
      at: event.createdAt,
    });
  } else if (type === "approval.resolved") {
    entries = upsertActivity(entries, id, {
      title: "Approval resolved",
      detail: typeof event.payload.decision === "string" ? event.payload.decision : "resolved",
      status: ["decline", "cancel"].includes(String(event.payload.decision)) ? "failed" : "done",
      at: event.createdAt,
    });
  } else if (type === "error") {
    const willRetry = event.payload.willRetry === true;
    entries = upsertActivity(entries, `connection-${event.runId}`, {
      title: willRetry ? "Agent connection interrupted" : "Agent connection failed",
      detail: diagnosticText(event.payload) || (willRetry ? "Codex is reconnecting." : "Codex reported a connection error."),
      status: willRetry ? "running" : "failed",
      at: event.createdAt,
    });
    if (willRetry) runActive = true;
  } else if (["warning", "configwarning", "guardianwarning", "deprecationnotice"].includes(type)) {
    entries = appendUnique(entries, {
      id: `warning-${event.runId}-${event.sequence}`,
      kind: "notice",
      text: diagnosticText(event.payload) || "The agent reported a warning.",
      severity: "warning",
      at: event.createdAt,
    });
  } else if (["run.started", "turn.started", "turn/started"].includes(type)) {
    runActive = true;
  } else if (["run.completed", "turn.completed", "turn/completed", "run.failed", "turn.interrupted", "run.status"].includes(type)) {
    runActive = false;
    const turn = typeof event.payload.turn === "object" && event.payload.turn ? event.payload.turn as Record<string, unknown> : undefined;
    const terminalStatus = String(event.payload.status ?? turn?.status ?? "").toLowerCase();
    const failed = type.endsWith("failed") || terminalStatus === "failed";
    const unknown = terminalStatus === "unknown";
    entries = resolveConnectionIssue(entries, event.runId, failed || unknown);
    if (failed || unknown) {
      entries = upsertNotice(entries, {
        id: `run-terminal-${event.runId}`,
        kind: "notice",
        text: diagnosticText(event.payload) || (unknown ? "The run ended in an unknown state. Check whether external side effects completed." : "The run failed. Check the runtime logs for details."),
        severity: "error",
        at: event.createdAt,
      });
    }
  }

  return { entries, runActive, lastSequence: event.sequence };
}

function isActivityItem(itemType: string) {
  return itemType !== "" && !["agentmessage", "usermessage", "reasoning", "plan", "contextcompaction"].includes(itemType);
}

function approvalDetail(payload: Record<string, unknown>) {
  const method = typeof payload.method === "string" ? payload.method : "Codex action";
  const params = payload.params && typeof payload.params === "object" ? payload.params as Record<string, unknown> : undefined;
  if (typeof params?.message === "string" && params.message.trim() !== "") return params.message;
  const command = params && typeof params.command === "string" ? `: ${params.command}` : "";
  return `${method}${command}`;
}

function objectFrom(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" ? value as Record<string, unknown> : undefined;
}

function autoReviewDetail(payload: Record<string, unknown>): string {
  const action = objectFrom(payload.action);
  const review = objectFrom(payload.review);
  let detail: unknown;
  switch (action?.type) {
    case "command": detail = action.command; break;
    case "execve": detail = Array.isArray(action.argv) && action.argv.length > 0 ? action.argv.join(" ") : action.program; break;
    case "writeStdin": detail = "Send input to a running command"; break;
    case "applyPatch": detail = Array.isArray(action.files) ? `Edit files: ${action.files.join(", ")}` : "Edit files"; break;
    case "networkAccess": detail = action.target; break;
    case "mcpToolCall": detail = action.toolTitle || action.toolName; break;
    case "requestPermissions": detail = action.reason || "Request additional permissions"; break;
  }
  return [
    typeof detail === "string" && detail.trim() ? detail : "Codex action",
    typeof review?.rationale === "string" && review.rationale.trim() ? review.rationale : undefined,
    typeof review?.riskLevel === "string" ? `Risk: ${review.riskLevel}` : undefined,
    typeof review?.userAuthorization === "string" ? `User authorization: ${review.userAuthorization}` : undefined,
  ].filter(Boolean).join("\n");
}

function toolResultText(item: Record<string, unknown>): string {
  const result = typeof item.result === "object" && item.result ? item.result as Record<string, unknown> : undefined;
  if (!Array.isArray(result?.content)) return "";
  return result.content.flatMap((part) =>
    part && typeof part === "object" && part.type === "text" && typeof part.text === "string" && part.text.trim() !== ""
      ? [part.text]
      : [],
  ).join("\n");
}

function diagnosticText(payload: Record<string, unknown>): string {
  const error = typeof payload.error === "object" && payload.error ? payload.error as Record<string, unknown> : undefined;
  const turn = typeof payload.turn === "object" && payload.turn ? payload.turn as Record<string, unknown> : undefined;
  const turnError = typeof turn?.error === "object" && turn.error ? turn.error as Record<string, unknown> : undefined;
  const primary = [payload.message, typeof payload.error === "string" ? payload.error : undefined, error?.message, turnError?.message, payload.summary]
    .find((value) => typeof value === "string" && value.trim() !== "");
  const detail = [payload.details, error?.additionalDetails, turnError?.additionalDetails]
    .find((value) => typeof value === "string" && value.trim() !== "");
  if (typeof primary === "string" && typeof detail === "string" && primary !== detail) return `${primary}\n${detail}`;
  if (typeof primary === "string") return primary;
  return typeof detail === "string" ? detail : "";
}

function resolveConnectionIssue(entries: ChatEntry[], runId: string, failed: boolean): ChatEntry[] {
  const id = `connection-${runId}`;
  return entries.map((entry) => entry.id === id && entry.kind === "activity" && entry.status === "running"
    ? { ...entry, title: failed ? "Agent connection failed" : "Agent connection restored", status: failed ? "failed" : "done" }
    : entry);
}

function appendUnique(entries: ChatEntry[], entry: ChatEntry): ChatEntry[] {
  return entries.some((existing) => existing.id === entry.id) ? entries : [...entries, entry];
}

function upsertNotice(entries: ChatEntry[], notice: Extract<ChatEntry, { kind: "notice" }>): ChatEntry[] {
  return entries.some((entry) => entry.id === notice.id)
    ? entries.map((entry) => entry.id === notice.id ? notice : entry)
    : [...entries, notice];
}

function appendAssistantDelta(entries: ChatEntry[], id: string, delta: string, at?: string): ChatEntry[] {
  const index = entries.findIndex((entry) => entry.id === id && entry.kind === "assistant");
  if (index === -1) return [...entries, { id, kind: "assistant", text: delta, streaming: true, at }];
  return entries.map((entry, entryIndex) =>
    entryIndex === index && entry.kind === "assistant"
      ? { ...entry, text: `${entry.text}${delta}`, streaming: true }
      : entry,
  );
}

function completeAssistant(entries: ChatEntry[], id: string, text: string, at?: string): ChatEntry[] {
  const index = entries.findIndex((entry) => entry.id === id && entry.kind === "assistant");
  if (index === -1) return [...entries, { id, kind: "assistant", text, streaming: false, at }];
  return entries.map((entry, entryIndex) =>
    entryIndex === index && entry.kind === "assistant"
      ? { ...entry, text: text || entry.text, streaming: false }
      : entry,
  );
}

function upsertActivity(
  entries: ChatEntry[],
  id: string,
  activity: Omit<Extract<ChatEntry, { kind: "activity" }>, "id" | "kind">,
): ChatEntry[] {
  const next: Extract<ChatEntry, { kind: "activity" }> = { id, kind: "activity", ...activity };
  return entries.some((entry) => entry.id === id)
    ? entries.map((entry) => (entry.id === id ? next : entry))
    : [...entries, next];
}
