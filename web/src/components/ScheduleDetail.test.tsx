import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ScheduleDetail } from "./ScheduleDetail";
import { api } from "../api";
import type { Agent, AgentEvent, Run, Schedule } from "../types";
import type { ChatEntry } from "../streamModel";

vi.mock("../api", () => ({ api: { getSchedule: vi.fn(), getScheduleRuns: vi.fn(), getScheduleRunEvents: vi.fn() } }));

const agent: Agent = { id: "agent-1", name: "Researcher", rolePrompt: "Research", roleVersion: 1, status: "running", providerAuthState: "connected", createdAt: "2026-09-14T00:00:00Z", updatedAt: "2026-09-14T00:00:00Z" };
const schedule: Schedule = { id: "schedule-1", agentId: agent.id, name: "Morning review", prompt: "Review the latest changes", kind: "cron", expression: "0 9 * * *", timezone: "Asia/Tokyo", enabled: true, createdAt: agent.createdAt, updatedAt: agent.updatedAt };
const run: Run = { id: "a-run", agentId: agent.id, source: "schedule", conversationId: "schedule-context", prompt: "Original scheduled instruction", status: "running", scheduledFor: "2026-09-14T00:00:00Z", startedAt: "2026-09-14T00:00:01Z" };
const event = (sequence: number, type: string, payload: Record<string, unknown>, runId = run.id): AgentEvent => ({ runId, sequence, type, payload, createdAt: agent.createdAt });
const renderEntry = (entry: ChatEntry) => <div key={entry.id} data-kind={entry.kind}>{entry.kind === "activity" ? `${entry.title}: ${entry.detail}` : "text" in entry ? entry.text : ""}</div>;
const panel = (scheduleId = schedule.id) => <ScheduleDetail agent={agent} scheduleId={scheduleId} onBack={vi.fn()} renderEntry={renderEntry} />;
const flush = async () => { await act(async () => {}); };
const expand = async () => {
  fireEvent.click(within(screen.getByRole("list", { name: "Schedule execution history" })).getAllByRole("button")[0]);
  await flush();
};

beforeEach(() => {
  vi.useFakeTimers();
  vi.mocked(api.getSchedule).mockReset().mockResolvedValue(schedule);
  vi.mocked(api.getScheduleRuns).mockReset().mockResolvedValue({ runs: [run] });
  vi.mocked(api.getScheduleRunEvents).mockReset().mockResolvedValue({ run, events: [], nextSequence: 0, hasMore: false });
});
afterEach(() => { cleanup(); vi.useRealTimers(); });

describe("Schedule details", () => {
  it("shows metadata and live paginated output without a composer, prompt duplication, or steering", async () => {
    vi.mocked(api.getScheduleRunEvents)
      .mockResolvedValueOnce({ run, events: [event(1, "message.user", { text: run.prompt }), event(2, "item/started", { item: { id: "cmd", type: "commandExecution", command: "ls /workspace" } })], nextSequence: 2, hasMore: true })
      .mockResolvedValueOnce({ run, events: [event(3, "item/completed", { item: { id: "cmd", type: "commandExecution", command: "ls /workspace", aggregatedOutput: "report.txt" } }), event(4, "item/agentMessage/delta", { itemId: "answer", delta: "Found a report" })], nextSequence: 4, hasMore: false })
      .mockResolvedValueOnce({ run: { ...run, status: "completed" }, events: [event(5, "item/completed", { item: { id: "answer", type: "agentMessage", text: "Found a report." } })], nextSequence: 5, hasMore: false });
    render(panel()); await flush();
    expect(screen.getByRole("heading", { name: schedule.name })).toBeInTheDocument();
    expect(screen.getByText(schedule.timezone)).toBeInTheDocument();
    expect(screen.getByText(schedule.prompt)).toBeInTheDocument();
    await expand();
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(screen.getAllByText(run.prompt)).toHaveLength(1);
    expect(screen.getByText("ls /workspace: report.txt")).toBeInTheDocument();
    expect(screen.getByText("Found a report")).toBeInTheDocument();
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(screen.getByText("Found a report.")).toBeInTheDocument();
    expect(api.getScheduleRunEvents).toHaveBeenNthCalledWith(3, schedule.id, run.id, 4, expect.any(AbortSignal));
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /send|stop|steer/i })).not.toBeInTheDocument();
  });

  it("keeps older pages while polling fresh status and sorts by scheduled time instead of random run IDs", async () => {
    const older: Run = { ...run, id: "z-older", scheduledFor: "2026-09-13T00:00:00Z", status: "failed", error: "Provider unavailable" };
    const skipped: Run = { ...run, id: "b-skipped", scheduledFor: "2026-09-12T00:00:00Z", status: "skipped_overlap", startedAt: undefined };
    vi.mocked(api.getScheduleRuns)
      .mockResolvedValueOnce({ runs: [run], nextCursor: run.id })
      .mockResolvedValueOnce({ runs: [older, skipped] })
      .mockResolvedValue({ runs: [{ ...run, status: "completed" }], nextCursor: run.id });
    render(panel()); await flush();
    fireEvent.click(screen.getByRole("button", { name: "Load older runs" })); await flush();
    expect(api.getScheduleRuns).toHaveBeenNthCalledWith(2, schedule.id, run.id, expect.any(AbortSignal));
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    const items = within(screen.getByRole("list")).getAllByRole("listitem");
    expect(items).toHaveLength(3);
    expect(items[0]).toHaveTextContent("Completed");
    expect(items[1]).toHaveTextContent("Failed");
    expect(items[1]).toHaveTextContent("Provider unavailable");
    expect(items[2]).toHaveTextContent("Skipped (agent busy)");
    expect(screen.queryByRole("button", { name: "Load older runs" })).not.toBeInTheDocument();
  });

  it("ignores and aborts a previous schedule's pending metadata after switching", async () => {
    let resolve!: (value: Schedule) => void;
    vi.mocked(api.getSchedule).mockImplementationOnce(() => new Promise((done) => { resolve = done; }));
    const view = render(panel()); await flush();
    const signal = vi.mocked(api.getSchedule).mock.calls[0][1]!;
    vi.mocked(api.getSchedule).mockResolvedValue({ ...schedule, id: "schedule-2", name: "Evening review" });
    view.rerender(panel("schedule-2")); await flush();
    expect(signal.aborted).toBe(true);
    await act(async () => { resolve(schedule); });
    expect(screen.getByRole("heading", { name: "Evening review" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Morning review" })).not.toBeInTheDocument();
  });

  it("ignores a previous run's late output after selecting a different run", async () => {
    const older = { ...run, id: "older", status: "completed" as const, scheduledFor: "2026-09-13T00:00:00Z" };
    vi.mocked(api.getScheduleRuns).mockResolvedValue({ runs: [run, older] });
    let resolve!: (value: Awaited<ReturnType<typeof api.getScheduleRunEvents>>) => void;
    vi.mocked(api.getScheduleRunEvents)
      .mockImplementationOnce(() => new Promise((done) => { resolve = done; }))
      .mockResolvedValue({ run: older, events: [event(1, "message.assistant.completed", { text: "Older result" }, older.id)], nextSequence: 1, hasMore: false });
    render(panel()); await flush(); await expand();
    const signal = vi.mocked(api.getScheduleRunEvents).mock.calls[0][3]!;
    fireEvent.click(within(screen.getByRole("list")).getAllByRole("button")[1]); await flush();
    expect(signal.aborted).toBe(true);
    await act(async () => { resolve({ run, events: [event(1, "message.assistant.completed", { text: "Late result" })], nextSequence: 1, hasMore: false }); });
    expect(screen.getByText("Older result")).toBeInTheDocument();
    expect(screen.queryByText("Late result")).not.toBeInTheDocument();
  });

  it("shows explicit truncated previews and skipped runs without output", async () => {
    const skipped = { ...run, status: "skipped_overlap" as const };
    vi.mocked(api.getScheduleRunEvents).mockResolvedValue({ run: skipped, events: [event(1, "item/completed", { truncated: true, originalBytes: 300000, preview: "Large command log" })], nextSequence: 1, hasMore: false });
    render(panel()); await flush(); await expand();
    expect(screen.getByText(/Output preview \(300000 bytes; truncated\)/)).toHaveTextContent("Large command log");
    expect(screen.getByText("Skipped (agent busy)")).toBeInTheDocument();
    expect(screen.getByText(run.prompt)).toBeInTheDocument();
  });

  it("provides loading, empty history, and retryable output errors", async () => {
    vi.mocked(api.getScheduleRunEvents).mockRejectedValueOnce(new Error("Read failed"))
      .mockResolvedValue({ run: { ...run, status: "completed" }, events: [], nextSequence: 0, hasMore: false });
    render(panel());
    expect(screen.getByRole("status")).toHaveTextContent("Loading schedule");
    await flush(); await expand();
    expect(screen.getByRole("alert")).toHaveTextContent("Read failed");
    fireEvent.click(screen.getByRole("button", { name: "Retry output" })); await flush();
    expect(screen.getByText("No output recorded.")).toBeInTheDocument();
    cleanup();
    vi.mocked(api.getScheduleRuns).mockResolvedValue({ runs: [] });
    render(panel()); await flush();
    expect(screen.getByText("No runs yet.")).toBeInTheDocument();
  });
});
