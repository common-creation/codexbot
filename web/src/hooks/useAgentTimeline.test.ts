import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "../api";
import type { AgentEvent, AgentTimeline } from "../types";
import { useAgentTimeline } from "./useAgentTimeline";

const event = (sequence: number, text: string, runId = "run-1"): AgentEvent => ({
  runId, sequence, type: "message.user", payload: { id: `input-${sequence}`, text }, createdAt: "2026-09-14T00:00:00Z",
});
const page = (events: AgentEvent[], rest: Partial<AgentTimeline> = {}): AgentTimeline => ({
  events, conversationId: "conversation-1", lastSequence: events.at(-1)?.sequence ?? 0, hasMore: false, ...rest,
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers(); });

describe("useAgentTimeline", () => {
  it("discards in-flight metadata from before a context reset without losing history", async () => {
    let resolve!: (value: AgentTimeline) => void;
    const get = vi.spyOn(api, "getAgentTimeline")
      .mockResolvedValueOnce(page([event(1, "Earlier input")]))
      .mockImplementationOnce(() => new Promise((done) => { resolve = done; }))
      .mockResolvedValue(page([event(2, "New context input")], { conversationId: "fresh-context" }));
    const { result, rerender } = renderHook(({ revision }) => useAgentTimeline("agent-1", revision), { initialProps: { revision: 0 } });
    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    let pending!: Promise<void>;
    act(() => { pending = result.current.refresh(); });
    rerender({ revision: 1 });
    await act(async () => { resolve(page([], { conversationId: "stale-context", lastSequence: 100 })); await pending; });
    expect(result.current.conversationId).toBe("fresh-context");
    expect(result.current.entries.map((entry) => "text" in entry && entry.text)).toEqual(["Earlier input", "New context input"]);
    expect(get).toHaveBeenLastCalledWith("agent-1", { after: 1, limit: 100 });
  });

  it("discovers delegated work while idle and drains incremental pages without duplicates", async () => {
    vi.useFakeTimers();
    const get = vi.spyOn(api, "getAgentTimeline")
      .mockResolvedValueOnce(page([event(10, "Earlier manual input")]))
      .mockResolvedValueOnce(page([event(11, "Delegated input", "run-2")], { activeRunId: "run-2", hasMore: true }))
      .mockResolvedValueOnce(page([event(12, "Additional directory", "run-2")], { activeRunId: "run-2" }))
      .mockResolvedValue(page([], { lastSequence: 12 }));
    const { result } = renderHook(() => useAgentTimeline("agent-1"));
    await act(async () => {});
    expect(result.current.runActive).toBe(false);
    await act(async () => { await vi.advanceTimersByTimeAsync(750); });
    expect(get).toHaveBeenNthCalledWith(2, "agent-1", { after: 10, limit: 100 });
    expect(get).toHaveBeenNthCalledWith(3, "agent-1", { after: 11, limit: 100 });
    expect(result.current.activeRunId).toBe("run-2");
    expect(result.current.entries).toHaveLength(3);
    await act(async () => { await vi.advanceTimersByTimeAsync(750); });
    expect(result.current.runActive).toBe(false);
    expect(result.current.entries).toHaveLength(3);
  });

  it("prepends older pages and preserves current run metadata and the live cursor", async () => {
    const get = vi.spyOn(api, "getAgentTimeline").mockImplementation(async (_, options) =>
      options?.before ? page([event(2, "Old input")]) : options?.after
        ? page([event(21, "Newest")])
        : page([event(20, "Current")], { hasMore: true, activeRunId: "run-1" }));
    const { result } = renderHook(() => useAgentTimeline("agent-1"));
    await waitFor(() => expect(result.current.hasEarlier).toBe(true));
    await act(async () => { await result.current.loadEarlier(); });
    expect(result.current.entries.map((entry) => "text" in entry && entry.text)).toEqual(["Old input", "Current"]);
    expect(result.current.activeRunId).toBe("run-1");
    expect(result.current.hasEarlier).toBe(false);
    await act(async () => { await result.current.refresh(); });
    expect(get).toHaveBeenLastCalledWith("agent-1", { after: 20, limit: 100 });
  });

  it("ignores a late response after selecting another agent", async () => {
    let resolve!: (value: AgentTimeline) => void;
    vi.spyOn(api, "getAgentTimeline").mockImplementation((id) => id === "agent-1"
      ? new Promise((done) => { resolve = done; }) : Promise.resolve(page([event(2, "Second agent")])));
    const { result, rerender } = renderHook(({ id }) => useAgentTimeline(id), { initialProps: { id: "agent-1" } });
    rerender({ id: "agent-2" });
    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    await act(async () => { resolve(page([event(1, "First agent")])); });
    expect(result.current.entries).toEqual([expect.objectContaining({ text: "Second agent" })]);
  });
});
