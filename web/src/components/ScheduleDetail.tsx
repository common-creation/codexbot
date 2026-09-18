import { useEffect, useRef, useState, type ReactNode } from "react";
import { api } from "../api";
import { entriesFromHistory, type ChatEntry } from "../streamModel";
import type { Agent, AgentEvent, Run, Schedule } from "../types";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";

export function ScheduleDetail(props: {
  agent: Agent;
  scheduleId: string;
  onBack: () => void;
  renderEntry: (entry: ChatEntry) => ReactNode;
}) {
  // Changing the owner or schedule also discards pending requests and cached pages.
  return <ScheduleDetailContent key={`${props.agent.id}:${props.scheduleId}`} {...props} />;
}

function ScheduleDetailContent({ agent, scheduleId, onBack, renderEntry }: Parameters<typeof ScheduleDetail>[0]) {
  const [schedule, setSchedule] = useState<Schedule>();
  const [runs, setRuns] = useState<Run[]>([]);
  const [nextCursor, setNextCursor] = useState<string>();
  const [selectedRunId, setSelectedRunId] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [pageError, setPageError] = useState("");
  const [retry, setRetry] = useState(0);
  const initialized = useRef(false);
  const pageController = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const [detail, page] = await Promise.all([
          api.getSchedule(scheduleId, controller.signal),
          api.getScheduleRuns(scheduleId, undefined, controller.signal),
        ]);
        if (controller.signal.aborted) return;
        setSchedule(detail);
        setRuns((current) => mergeRuns(current, page.runs));
        if (!initialized.current) {
          setNextCursor(page.nextCursor);
          initialized.current = true;
        }
        setError("");
      } catch (failure) {
        if (!controller.signal.aborted) setError(errorMessage(failure, "Could not load schedule details."));
      } finally {
        if (!controller.signal.aborted) {
          setLoading(false);
          timer = setTimeout(() => void refresh(), 2000);
        }
      }
    };
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [scheduleId, retry]);

  useEffect(() => () => { pageController.current?.abort(); }, []);

  const loadMore = async () => {
    if (!nextCursor || pageController.current) return;
    const controller = new AbortController();
    pageController.current = controller;
    setLoadingMore(true);
    setPageError("");
    try {
      const page = await api.getScheduleRuns(scheduleId, nextCursor, controller.signal);
      if (controller.signal.aborted) return;
      setRuns((current) => mergeRuns(current, page.runs));
      setNextCursor(page.nextCursor);
    } catch (failure) {
      if (!controller.signal.aborted) setPageError(errorMessage(failure, "Could not load older runs."));
    } finally {
      if (!controller.signal.aborted) { pageController.current = null; setLoadingMore(false); }
    }
  };

  return <section className="settings-page">
    <div className="schedule-detail">
      <Button variant="outline" onClick={onBack}>Back to schedules</Button>
      {error && <div role="alert" className="notice-message">{error} <Button variant="link" onClick={() => setRetry((value) => value + 1)}>Retry details</Button></div>}
      {loading && <p role="status">Loading schedule…</p>}
      {schedule && <>
        <div className="page-heading"><div><span className="eyebrow">SCHEDULE · {agent.name}</span><h1>{schedule.name}</h1>
          <p>Each run uses a fresh context, separate from chat.</p></div><Badge variant="outline">{schedule.enabled ? "Enabled" : "Disabled"}</Badge></div>
        <dl className="schedule-metadata">
          <div><dt>Task</dt><dd className="schedule-prompt">{schedule.prompt}</dd></div>
          <div><dt>{schedule.kind === "cron" ? "Cron expression" : "Run once"}</dt><dd>{schedule.kind === "cron" ? schedule.expression : formatDate(schedule.expression)}</dd></div>
          <div><dt>Timezone</dt><dd>{schedule.timezone}</dd></div>
          <div><dt>Next run</dt><dd>{schedule.nextRunAt ? formatDate(schedule.nextRunAt) : "—"}</dd></div>
          <div><dt>Last run</dt><dd>{schedule.lastRunAt ? formatDate(schedule.lastRunAt) : "—"}</dd></div>
        </dl>
      </>}
      <h2>Execution history</h2>
      {!loading && !error && runs.length === 0 && <p>No runs yet.</p>}
      <ol className="schedule-run-list" aria-label="Schedule execution history">
        {runs.map((run) => <li key={run.id} className="schedule-run">
          <div className="schedule-run-summary">
            <Button variant="link" className="h-auto justify-start p-0" aria-expanded={selectedRunId === run.id}
              aria-controls={`schedule-run-${run.id}`} onClick={() => setSelectedRunId((current) => current === run.id ? undefined : run.id)}>
              {formatDate(run.scheduledFor || run.startedAt || "") || run.id}
            </Button>
            <Badge variant="outline">{runStatus(run.status)}</Badge>
          </div>
          <div className="schedule-run-times">
            <span>Started: {run.startedAt ? formatDate(run.startedAt) : "—"}</span>
            <span>Finished: {run.finishedAt ? formatDate(run.finishedAt) : "—"}</span>
          </div>
          {run.error && <p className="schedule-run-error">{run.error}</p>}
          <div id={`schedule-run-${run.id}`}>
            {selectedRunId === run.id && <ScheduleRunOutput key={run.id} scheduleId={scheduleId} initialRun={run} renderEntry={renderEntry} />}
          </div>
        </li>)}
      </ol>
      {pageError && <p role="alert">{pageError}</p>}
      {nextCursor && <Button variant="outline" disabled={loadingMore} onClick={() => void loadMore()}>{loadingMore ? "Loading older runs…" : "Load older runs"}</Button>}
    </div>
  </section>;
}

function ScheduleRunOutput({ scheduleId, initialRun, renderEntry }: {
  scheduleId: string;
  initialRun: Run;
  renderEntry: (entry: ChatEntry) => ReactNode;
}) {
  const [run, setRun] = useState(initialRun);
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const cursor = useRef(0);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const page = await api.getScheduleRunEvents(scheduleId, initialRun.id, cursor.current, controller.signal);
        if (controller.signal.aborted) return;
        setRun(page.run);
        setEvents((current) => [...new Map([...current, ...page.events].map((event) => [event.sequence, event])).values()].sort((a, b) => a.sequence - b.sequence));
        cursor.current = page.nextSequence;
        setError("");
        if (page.hasMore || isActive(page.run)) timer = setTimeout(() => void refresh(), page.hasMore ? 0 : 1000);
      } catch (failure) {
        if (!controller.signal.aborted) setError(errorMessage(failure, "Could not load run output."));
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    };
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [scheduleId, initialRun.id, retry]);

  const entries = entriesFromHistory(events.map((event) => event.payload.truncated === true ? {
    ...event, type: "warning", payload: { message: `Output preview (${event.payload.originalBytes ?? "unknown"} bytes; truncated):\n${String(event.payload.preview ?? "")}` },
  } : event), run.agentId);
  const promptEntry: ChatEntry = { id: `${run.id}:schedule-prompt`, kind: "user", text: run.prompt, at: run.startedAt };
  const hasPrompt = entries.some((entry) => entry.kind === "user" && entry.text === run.prompt);
  return <div className="schedule-run-output" aria-label="Run output">
    <div className="schedule-run-summary"><strong>Run output</strong><Badge variant="outline">{runStatus(run.status)}</Badge></div>
    {loading && <p role="status">Loading run output…</p>}
    {error && <p role="alert">{error} <Button variant="link" onClick={() => setRetry((value) => value + 1)}>Retry output</Button></p>}
    {run.prompt && !hasPrompt && renderEntry(promptEntry)}
    {entries.map((entry) => renderEntry(!isActive(run) && entry.kind === "assistant" ? { ...entry, streaming: false } : entry))}
    {!loading && !error && !entries.some((entry) => entry.kind !== "user") && <p>{isActive(run) ? "Waiting for output…" : "No output recorded."}</p>}
  </div>;
}

function mergeRuns(current: Run[], incoming: Run[]) {
  return [...new Map([...current, ...incoming].map((run) => [run.id, run])).values()]
    .sort((a, b) => runTime(b) - runTime(a) || b.id.localeCompare(a.id));
}
function runTime(run: Run) { return Date.parse(run.scheduledFor || run.startedAt || run.finishedAt || "") || 0; }
function isActive(run: Run) { return run.status === "queued" || run.status === "running"; }
function runStatus(status: Run["status"]) {
  return ({ queued: "Queued", running: "Running", completed: "Completed", failed: "Failed", interrupted: "Interrupted", unknown: "Unknown", skipped_overlap: "Skipped (agent busy)" })[status];
}
function formatDate(value: string) { const date = new Date(value); return Number.isNaN(date.valueOf()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" }).format(date); }
function errorMessage(error: unknown, fallback: string) { return error instanceof Error ? error.message : fallback; }
