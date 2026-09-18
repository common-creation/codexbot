import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api";
import { useEventStream } from "./useEventStream";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("useEventStream", () => {
  it("does not retry a permanent HTTP error", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 404 }));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useEventStream("run-1"));

    await waitFor(() => expect(result.current.connection).toBe("failed"));
    expect(result.current.connectionError).toContain("404");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("projects terminal Run status and its exact error after SSE closes", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 200 })));
    vi.spyOn(api, "getRun").mockResolvedValue({
      id: "run-1",
      agentId: "agent-1",
      conversationId: "conversation-1",
      source: "manual",
      prompt: "hello",
      status: "failed",
      error: "upstream unavailable",
      finishedAt: "2026-09-03T10:31:00Z",
    });
    const { result } = renderHook(() => useEventStream("run-1"));

    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    expect(result.current.connection).toBe("idle");
    expect(result.current.runActive).toBe(false);
    expect(result.current.entries).toContainEqual(expect.objectContaining({
      id: "run-terminal-run-1",
      kind: "notice",
      text: "upstream unavailable",
    }));
  });

  it("does not reconnect forever when Run lookup is permanently unavailable", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response("", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    vi.spyOn(api, "getRun").mockRejectedValue(new ApiError("missing", 404));
    const { result } = renderHook(() => useEventStream("run-1"));

    await waitFor(() => expect(result.current.connection).toBe("failed"));
    expect(result.current.connectionError).toContain("404");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
