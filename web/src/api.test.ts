import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";

afterEach(() => vi.unstubAllGlobals());

it("scopes schedule history and event cursors to the selected schedule and supports aborting navigation", async () => {
  const fetch = vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) });
  vi.stubGlobal("fetch", fetch);
  const controller = new AbortController();
  await api.getSchedule("schedule-1", controller.signal);
  await api.getScheduleRuns("schedule-1", "previous-run", controller.signal);
  await api.getScheduleRunEvents("schedule-1", "selected-run", 42, controller.signal);
  expect(fetch).toHaveBeenNthCalledWith(1, "/api/schedules/schedule-1", expect.objectContaining({ signal: controller.signal }));
  expect(fetch).toHaveBeenNthCalledWith(2, "/api/schedules/schedule-1/runs?limit=20&before=previous-run", expect.objectContaining({ signal: controller.signal }));
  expect(fetch).toHaveBeenNthCalledWith(3, "/api/schedules/schedule-1/runs/selected-run/events?after=42&limit=100", expect.objectContaining({ signal: controller.signal }));
});

describe("desktopUrl", () => {
  it("routes the KasmVNC websocket through the agent-scoped proxy", () => {
    const url = new URL(api.desktopUrl("agent-123", 7), "http://codexbot.test");
    expect(url.pathname).toBe("/api/agents/agent-123/desktop/");
    expect(url.searchParams.get("autoconnect")).toBe("1");
    expect(url.searchParams.get("reconnect")).toBe("1");
    expect(url.searchParams.get("resize")).toBe("scale");
    expect(url.searchParams.get("path")).toBe("api/agents/agent-123/desktop/websockify");
    expect(url.searchParams.get("codexbotSession")).toBe("7");
  });
});
