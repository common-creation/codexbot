import { describe, expect, it } from "vitest";
import { applyAgentEvent, entriesFromHistory, initialStreamState } from "./streamModel";

describe("applyAgentEvent", () => {
  it("assembles streamed assistant messages and completes them", () => {
    const first = applyAgentEvent(initialStreamState, {
      runId: "run-1",
      sequence: 1,
      type: "message.assistant.delta",
      payload: { itemId: "answer-1", delta: "Hello" },
      createdAt: "2026-09-02T00:00:00Z",
    });
    const second = applyAgentEvent(first, {
      runId: "run-1",
      sequence: 2,
      type: "message.assistant.delta",
      payload: { itemId: "answer-1", delta: " world" },
      createdAt: "2026-09-02T00:00:01Z",
    });
    const completed = applyAgentEvent(second, {
      runId: "run-1",
      sequence: 3,
      type: "message.assistant.completed",
      payload: { itemId: "answer-1" },
      createdAt: "2026-09-02T00:00:02Z",
    });

    expect(completed.entries).toEqual([
      { id: "run-1:answer-1", kind: "assistant", text: "Hello world", streaming: false, at: "2026-09-02T00:00:00Z" },
    ]);
    expect(completed.lastSequence).toBe(3);
  });

  it("ignores replayed events and updates tool status in place", () => {
    const running = applyAgentEvent(initialStreamState, {
      runId: "run-1",
      sequence: 4,
      type: "tool.started",
      payload: { itemId: "tool-1", title: "Browser", text: "Opening account" },
      createdAt: "2026-09-02T00:00:00Z",
    });
    const replayed = applyAgentEvent(running, {
      runId: "run-1",
      sequence: 4,
      type: "tool.started",
      payload: { itemId: "tool-1", title: "Duplicate" },
      createdAt: "2026-09-02T00:00:00Z",
    });
    const done = applyAgentEvent(replayed, {
      runId: "run-1",
      sequence: 5,
      type: "tool.completed",
      payload: { itemId: "tool-1", title: "Browser", text: "Account opened", success: true },
      createdAt: "2026-09-02T00:00:01Z",
    });

    expect(replayed).toBe(running);
    expect(done.entries).toHaveLength(1);
    expect(done.entries[0]).toMatchObject({ id: "run-1:tool-1", kind: "activity", status: "done" });
  });

  it("displays legacy approval requests without interactive actions", () => {
    const state = applyAgentEvent(initialStreamState, {
      runId: "run-1",
      sequence: 1,
      type: "approval.requested",
      payload: {
        approvalId: "approval-1",
        method: "item/commandExecution/requestApproval",
        params: { command: "npm test" },
      },
      createdAt: "2026-09-02T00:00:00Z",
    });

    expect(state.entries[0]).toMatchObject({
      id: "run-1:approval-1",
      kind: "activity",
      status: "waiting",
      detail: "item/commandExecution/requestApproval: npm test",
    });
  });

  it("shows the MCP approval message and marks cancellation as failed", () => {
    const requested = applyAgentEvent(initialStreamState, {
      runId: "run-1", sequence: 1, type: "approval.requested",
      payload: {
        approvalId: "approval-1",
        method: "mcpServer/elicitation/request",
        params: { message: "Allow browser navigation to Wikipedia?" },
      },
      createdAt: "2026-09-02T00:00:00Z",
    });
    expect(requested.entries[0]).toMatchObject({
      status: "waiting", detail: "Allow browser navigation to Wikipedia?",
    });

    const resolved = applyAgentEvent(requested, {
      runId: "run-1", sequence: 2, type: "approval.resolved",
      payload: { approvalId: "approval-1", decision: "cancel" },
      createdAt: "2026-09-02T00:00:01Z",
    });
    expect(resolved.entries).toHaveLength(1);
    expect(resolved.entries[0]).toMatchObject({ id: "run-1:approval-1", status: "failed", detail: "cancel" });
  });

  it.each(["accept", "acceptForSession", "decline", "cancel"])("resolves legacy permission requests in history after %s", (decision) => {
    const entries = entriesFromHistory([
      {
        runId: "run-1", sequence: 1, type: "approval.requested",
        payload: { approvalId: "approval-1", method: "mcpServer/elicitation/request", params: { message: "Allow browser navigation?" } },
        createdAt: "2026-09-02T00:00:00Z",
      },
      {
        runId: "run-1", sequence: 2, type: "approval.resolved",
        payload: { approvalId: "approval-1", decision },
        createdAt: "2026-09-02T00:00:01Z",
      },
    ]);
    expect(entries).toHaveLength(1);
    expect(entries[0]).toMatchObject({ id: "run-1:approval-1", kind: "activity" });
    expect(entries[0]).not.toHaveProperty("approvalId");
  });

  it.each([
    { type: "approval.autoAccepted", title: "Permission automatically allowed", status: "done", reason: "Safe browser action." },
    { type: "approval.autoDeclined", title: "Permission automatically denied", status: "failed", reason: "Credentials would be exposed." },
  ])("updates the automatic permission review in place after $type", ({ type, title, status, reason }) => {
    const reviewing = applyAgentEvent(initialStreamState, {
      runId: "run-1", sequence: 1, type: "approval.reviewing",
      payload: { approvalId: "approval-1", method: "mcpServer/elicitation/request", params: { message: "Open Wikipedia" }, model: "gpt-5.3-codex-spark" },
      createdAt: "2026-09-08T00:00:00Z",
    });
    expect(reviewing.entries[0]).toMatchObject({ title: "Checking tool safety", status: "running", detail: "Open Wikipedia" });
    expect(reviewing.runActive).toBe(true);
    const resolved = applyAgentEvent(reviewing, {
      runId: "run-1", sequence: 2, type,
      payload: { approvalId: "approval-1", reason, model: "gpt-5.3-codex-spark" },
      createdAt: "2026-09-08T00:00:01Z",
    });
    expect(resolved.entries).toHaveLength(1);
    expect(resolved.entries[0]).toMatchObject({ id: "run-1:approval-1", kind: "activity", title, status, detail: reason });
    expect(resolved.entries[0]).not.toHaveProperty("approvalId");
    expect(resolved.runActive).toBe(true);
  });

  it("displays Full Access decisions without a preceding review or model", () => {
    const entries = entriesFromHistory([{
      runId: "run-1", sequence: 1, type: "approval.autoAccepted",
      payload: { approvalId: "approval-1", method: "item/commandExecution/requestApproval", params: { command: "npm test" } },
      createdAt: "2026-09-08T00:00:00Z",
    }]);
    expect(entries[0]).toMatchObject({ title: "Permission automatically allowed", status: "done", detail: "item/commandExecution/requestApproval: npm test" });
  });

  it.each([
    { reviewStatus: "approved", title: "Permission automatically allowed", status: "done" },
    { reviewStatus: "denied", title: "Permission automatically denied", status: "failed" },
    { reviewStatus: "timedOut", title: "Automatic permission review timed out", status: "failed" },
    { reviewStatus: "aborted", title: "Automatic permission review aborted", status: "failed" },
    { reviewStatus: "futureStatus", title: "Automatic permission review failed", status: "failed" },
  ])("updates a native auto-review in place when $reviewStatus", ({ reviewStatus, title, status }) => {
    const payload = {
      threadId: "thread-1", turnId: "turn-1", reviewId: "review-1", targetItemId: "tool-1",
      startedAtMs: 1788825600000, action: { type: "command", command: "npm test", cwd: "/workspace", source: "unifiedExec" },
    };
    const running = applyAgentEvent(initialStreamState, {
      runId: "run-1", sequence: 1, type: "item/autoApprovalReview/started",
      payload: { ...payload, review: { status: "inProgress" } }, createdAt: "2026-09-08T00:00:00Z",
    });
    expect(running.entries[0]).toMatchObject({ title: "Checking tool safety", detail: "npm test", status: "running" });
    expect(running.runActive).toBe(true);
    const completed = applyAgentEvent(running, {
      runId: "run-1", sequence: 2, type: "item/autoApprovalReview/completed",
      payload: { ...payload, completedAtMs: 1788825601000, decisionSource: "agent", review: {
        status: reviewStatus, rationale: "Runs the requested local tests.", riskLevel: "low", userAuthorization: "high",
      } }, createdAt: "2026-09-08T00:00:01Z",
    });
    expect(completed.entries).toHaveLength(1);
    expect(completed.entries[0]).toMatchObject({
      id: "auto-review-run-1-review-1", title, status,
      detail: "npm test\nRuns the requested local tests.\nRisk: low\nUser authorization: high",
    });
    expect(completed.runActive).toBe(true);
  });

  it("keeps separate reviews for one tool and a network review without a target in replayed history", () => {
    const entries = entriesFromHistory([
      { runId: "run-1", sequence: 1, type: "item/started", payload: { item: { id: "tool-1", type: "commandExecution", command: "npm test" } }, createdAt: "2026-09-08T00:00:00Z" },
      ...["review-1", "review-2"].map((reviewId, index) => ({
        runId: "run-1", sequence: index + 2, type: "item/autoApprovalReview/completed",
        payload: { reviewId, targetItemId: "tool-1", action: { type: "execve", argv: ["node", "test.js"] }, review: { status: "approved" } }, createdAt: "2026-09-08T00:00:01Z",
      })),
      { runId: "run-1", sequence: 4, type: "item/autoApprovalReview/completed", payload: { reviewId: "review-3", targetItemId: null, action: { type: "networkAccess", target: "https://example.com" }, review: { status: "denied" } }, createdAt: "2026-09-08T00:00:02Z" },
      { runId: "run-1", sequence: 5, type: "item/completed", payload: { item: { id: "tool-1", type: "commandExecution", command: "npm test", status: "completed" } }, createdAt: "2026-09-08T00:00:03Z" },
    ]);
    expect(entries).toHaveLength(4);
    expect(entries[0]).toMatchObject({ id: "run-1:tool-1", title: "npm test", status: "done" });
    expect(entries[1]).toMatchObject({ id: "auto-review-run-1-review-1", status: "done", detail: "node test.js" });
    expect(entries[2]).toMatchObject({ id: "auto-review-run-1-review-2", status: "done" });
    expect(entries[3]).toMatchObject({ id: "auto-review-run-1-review-3", status: "failed", detail: "https://example.com" });
  });

  it.each([
    { type: "mcpToolCall", tool: "browser_navigate", command: undefined, title: "browser_navigate" },
    { type: "commandExecution", tool: undefined, command: "npm test", title: "npm test" },
  ])("shows nested $type names and failure reasons", ({ title, ...item }) => {
    const running = applyAgentEvent(initialStreamState, {
      runId: "run-1", sequence: 1, type: "item/started",
      payload: { item: { ...item, id: "tool-1", status: "inProgress" } },
      createdAt: "2026-09-02T00:00:00Z",
    });
    expect(running.entries[0]).toMatchObject({ id: "run-1:tool-1", title, status: "running" });

    const failed = applyAgentEvent(running, {
      runId: "run-1", sequence: 2, type: "item/completed",
      payload: { item: { ...item, id: "tool-1", status: "failed", error: { message: "Tool execution was denied." } } },
      createdAt: "2026-09-02T00:00:01Z",
    });
    expect(failed.entries).toHaveLength(1);
    expect(failed.entries[0]).toMatchObject({ id: "run-1:tool-1", title, status: "failed", detail: "Tool execution was denied." });
  });

  it.each([
    { status: "failed", error: null, detail: "Authorization required\nCannot open display :99" },
    { status: "failed", error: { message: "Desktop authorization failed" }, detail: "Desktop authorization failed" },
    { status: "completed", error: null, detail: "Authorization required\nCannot open display :99" },
  ])("uses only text result blocks for MCP calls: $status, $detail", ({ status, error, detail }) => {
    const state = applyAgentEvent(initialStreamState, {
      runId: "run-1", sequence: 1, type: "item/completed",
      payload: {
        item: {
          id: "desktop-1", type: "mcpToolCall", tool: "desktop_screenshot", status, error,
          result: {
            content: [
              { type: "text", text: "Authorization required" },
              { type: "image", data: "image-base64", text: "Do not display image data" },
              { type: "resource", resource: { text: "Do not display resource data" } },
              null,
              { type: "text", text: " " },
              { type: "text", text: { invalid: true } },
              { type: "text", text: "Cannot open display :99" },
            ],
          },
        },
      },
      createdAt: "2026-09-02T00:00:00Z",
    });
    expect(state.entries[0]).toMatchObject({ id: "run-1:desktop-1", title: "desktop_screenshot", detail });
  });

  it("does not render agent messages as tools and preserves nested failures", () => {
    const startedMessage = applyAgentEvent(initialStreamState, {
      runId: "run-1", sequence: 1, type: "item/started",
      payload: { item: { id: "answer-1", type: "agentMessage", status: "inProgress" } },
      createdAt: "2026-09-02T00:00:00Z",
    });
    expect(startedMessage.entries).toHaveLength(0);
    const failedTool = applyAgentEvent(startedMessage, {
      runId: "run-1", sequence: 2, type: "item/completed",
      payload: { item: { id: "tool-1", type: "commandExecution", status: "failed" } },
      createdAt: "2026-09-02T00:00:01Z",
    });
    expect(failedTool.entries[0]).toMatchObject({ id: "run-1:tool-1", kind: "activity", status: "failed" });
    const failedTurn = applyAgentEvent(failedTool, {
      runId: "run-1", sequence: 3, type: "turn/completed",
      payload: { turn: { id: "turn-1", status: "failed" } },
      createdAt: "2026-09-02T00:00:02Z",
    });
    expect(failedTurn.entries.some((entry) => entry.kind === "notice")).toBe(true);
  });

  it("shows reconnecting Codex errors without ending the active run", () => {
    const first = applyAgentEvent(initialStreamState, {
      runId: "run-1", sequence: 1, type: "error",
      payload: {
        error: {
          message: "Reconnecting... 2/5",
          additionalDetails: "Proxy connection failed: HTTP CONNECT failed with status 403",
        },
        willRetry: true,
      },
      createdAt: "2026-09-03T10:30:49Z",
    });
    const next = applyAgentEvent(first, {
      runId: "run-1", sequence: 2, type: "error",
      payload: { error: { message: "Reconnecting... 3/5" }, willRetry: true },
      createdAt: "2026-09-03T10:30:50Z",
    });

    expect(next.runActive).toBe(true);
    expect(next.entries).toHaveLength(1);
    expect(next.entries[0]).toMatchObject({ id: "connection-run-1", kind: "activity", status: "running", detail: "Reconnecting... 3/5" });
  });

  it("keeps non-retry errors active until terminal status and preserves the exact cause", () => {
    const error = applyAgentEvent({ ...initialStreamState, runActive: true }, {
      runId: "run-1", sequence: 1, type: "error",
      payload: { error: { message: "Connection failed" }, willRetry: false },
      createdAt: "2026-09-03T10:30:49Z",
    });
    expect(error.runActive).toBe(true);
    expect(error.entries[0]).toMatchObject({ status: "failed", detail: "Connection failed" });

    const terminal = applyAgentEvent(error, {
      runId: "run-1", sequence: 2, type: "turn/completed",
      payload: { turn: { status: "failed", error: { message: "Upstream unavailable" } } },
      createdAt: "2026-09-03T10:31:00Z",
    });
    expect(terminal.runActive).toBe(false);
    expect(terminal.entries).toContainEqual(expect.objectContaining({ id: "run-terminal-run-1", kind: "notice", text: "Upstream unavailable" }));
  });

  it("renders warnings without changing run ownership", () => {
    const state = applyAgentEvent({ ...initialStreamState, runActive: true }, {
      runId: "run-1", sequence: 1, type: "warning",
      payload: { message: "Falling back from WebSockets to HTTPS transport." },
      createdAt: "2026-09-03T10:30:49Z",
    });

    expect(state.runActive).toBe(true);
    expect(state.entries[0]).toMatchObject({ kind: "notice", severity: "warning", text: "Falling back from WebSockets to HTTPS transport." });
  });

  it("upgrades a generic terminal notice with the exact persisted error", () => {
    const generic = applyAgentEvent(initialStreamState, {
      runId: "run-1", sequence: 1, type: "turn/completed",
      payload: { turn: { status: "failed" } },
      createdAt: "2026-09-03T10:31:00Z",
    });
    const exact = applyAgentEvent(generic, {
      runId: "run-1", sequence: 2, type: "run.status",
      payload: { status: "failed", error: "proxy denied the request" },
      createdAt: "2026-09-03T10:31:01Z",
    });

    expect(exact.entries.filter((entry) => entry.id === "run-terminal-run-1")).toEqual([
      expect.objectContaining({ text: "proxy denied the request", severity: "error" }),
    ]);
  });

  it("shows unknown terminal runs as an error", () => {
    const state = applyAgentEvent(initialStreamState, {
      runId: "run-1", sequence: 1, type: "run.status",
      payload: { status: "unknown", error: "agent worker restarted during an active turn" },
      createdAt: "2026-09-03T10:31:00Z",
    });

    expect(state.runActive).toBe(false);
    expect(state.entries[0]).toMatchObject({ kind: "notice", severity: "error", text: "agent worker restarted during an active turn" });
  });
});


describe("unified agent timeline", () => {
  it("keeps repeated item IDs in different runs separate and displays each submitted input once", () => {
    const events = [
      { runId: "manual", type: "message.user", payload: { id: "user-manual", text: "Inspect /workspace" } },
      { runId: "manual", type: "item/started", payload: { item: { id: "input", type: "userMessage", content: [{ type: "text", text: "Inspect /workspace" }] } } },
      { runId: "manual", type: "item/completed", payload: { item: { id: "answer", type: "agentMessage", text: "First output" } } },
      { runId: "delegated", type: "message.user", payload: { id: "user-delegated", text: "Continue inspection" } },
      { runId: "delegated", type: "message.user", payload: { id: "steer-1", text: "Also inspect /shared" } },
      { runId: "delegated", type: "item/completed", payload: { item: { id: "answer", type: "agentMessage", text: "Second output" } } },
    ].map((event, index) => ({ ...event, sequence: index + 1, createdAt: "2026-09-14T00:00:00Z" }));
    const entries = entriesFromHistory(events);
    expect(entries.map((entry) => "text" in entry ? entry.text : "")).toEqual([
      "Inspect /workspace", "First output", "Continue inspection", "Also inspect /shared", "Second output",
    ]);
    expect(new Set(entries.map((entry) => entry.id)).size).toBe(5);
  });

  it("shows command output and links delegation metadata to the other agent", () => {
    const events = [
      { type: "collaboration.received", payload: { taskId: "task-1", senderAgentId: "sender", targetAgentId: "target", prompt: "Inspect files", status: "queued" } },
      { type: "item/started", payload: { item: { id: "tool", type: "commandExecution", command: "ls" } } },
      { type: "item/commandExecution/outputDelta", payload: { itemId: "tool", delta: "README.md\n" } },
      { type: "item/completed", payload: { item: { id: "tool", type: "commandExecution", command: "ls", status: "completed", aggregatedOutput: "README.md\n" } } },
    ].map((event, index) => ({ ...event, runId: "run-1", sequence: index + 1, createdAt: "2026-09-14T00:00:00Z" }));
    expect(entriesFromHistory(events)).toEqual([
      expect.objectContaining({ kind: "collaboration", taskId: "task-1", agentId: "sender", text: "Inspect files" }),
      expect.objectContaining({ kind: "activity", title: "ls", detail: "README.md\n", status: "done" }),
    ]);
  });
});


it("updates delegated task status across queued and running events and keeps the other-agent link", () => {
  const entries = entriesFromHistory([
    { runId: "", sequence: 1, type: "collaboration.received", payload: { taskId: "task-1", senderAgentId: "sender", targetAgentId: "target", prompt: "Inspect", status: "queued" }, createdAt: "2026-09-14T00:00:00Z" },
    { runId: "run-1", sequence: 2, type: "collaboration.status", payload: { taskId: "task-1", senderAgentId: "sender", targetAgentId: "target", status: "completed" }, createdAt: "2026-09-14T00:01:00Z" },
  ], "target");
  expect(entries).toEqual([expect.objectContaining({ id: "collaboration-task-1", title: "Task received", agentId: "sender", status: "completed", text: "Inspect" })]);
});
