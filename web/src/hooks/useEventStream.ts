import { useEffect, useRef, useState, type Dispatch, type MutableRefObject, type SetStateAction } from "react";
import { api, ApiError } from "../api";
import { applyAgentEvent, initialStreamState, type StreamState } from "../streamModel";
import type { AgentEvent } from "../types";

export function useEventStream(runId?: string): StreamState & {
  connection: "idle" | "connecting" | "live" | "retrying" | "failed";
  connectionError?: string;
  addOptimisticUserMessage: (id: string, text: string) => void;
  clear: () => void;
} {
  const [state, setState] = useState(initialStreamState);
  const [connection, setConnection] = useState<"idle" | "connecting" | "live" | "retrying" | "failed">("idle");
  const [connectionError, setConnectionError] = useState<string>();
  const lastSequence = useRef(0);

  useEffect(() => {
    setState(runId ? { ...initialStreamState, runActive: true } : initialStreamState);
    lastSequence.current = 0;
    if (!runId) {
      setConnection("idle");
      setConnectionError(undefined);
      return;
    }

    const controller = new AbortController();
    let retryTimer: number | undefined;
    let retryAttempt = 0;
    const retry = () => {
      if (controller.signal.aborted) return;
      setConnection("retrying");
      setConnectionError("The event stream was interrupted. Reconnecting…");
      const delay = Math.min(1_000 * 2 ** retryAttempt, 15_000);
      retryAttempt = Math.min(retryAttempt + 1, 4);
      retryTimer = window.setTimeout(() => void connect(), delay);
    };

    const connect = async () => {
      setConnection("connecting");
      try {
        const response = await fetch(api.runEventsUrl(runId, lastSequence.current), {
          credentials: "same-origin",
          headers: { Accept: "text/event-stream" },
          signal: controller.signal,
        });
        if (response.status === 401) {
          window.dispatchEvent(new Event("codexbot:unauthorized"));
          setConnection("idle");
          return;
        }
        if (!response.ok) {
          if (response.status === 408 || response.status === 429 || response.status >= 500) {
            retry();
          } else {
            setConnection("failed");
            setConnectionError(`Event stream unavailable (${response.status}).`);
          }
          return;
        }
        if (!response.body) throw new Error("Event stream response had no body");
        setConnection("live");
        setConnectionError(undefined);
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        while (!controller.signal.aborted) {
          const { done, value } = await reader.read();
          buffer += decoder.decode(value, { stream: !done });
          const blocks = buffer.split(/\r?\n\r?\n/);
          buffer = blocks.pop() ?? "";
          for (const block of blocks) {
            if (consumeSSEBlock(block, runId, lastSequence, setState)) retryAttempt = 0;
          }
          if (done) break;
        }
        if (!controller.signal.aborted) {
          const run = await api.getRun(runId);
          if (run.status === "queued" || run.status === "running") retry();
          else {
            const event: AgentEvent = {
              runId,
              sequence: lastSequence.current + 1,
              type: "run.status",
              payload: { status: run.status, error: run.error ?? "" },
              createdAt: run.finishedAt ?? new Date().toISOString(),
            };
            setState((current) => {
              const next = applyAgentEvent(current, event);
              lastSequence.current = next.lastSequence;
              return next;
            });
            setConnection("idle");
          }
        }
      } catch (error) {
        if (controller.signal.aborted) return;
        if (error instanceof ApiError && error.status === 401) {
          window.dispatchEvent(new Event("codexbot:unauthorized"));
          setConnection("idle");
          return;
        }
        if (error instanceof ApiError && error.status !== 408 && error.status !== 429 && error.status < 500) {
          setConnection("failed");
          setConnectionError(`Run status unavailable (${error.status}).`);
          return;
        }
        retry();
      }
    };

    void connect();
    return () => {
      controller.abort();
      if (retryTimer) window.clearTimeout(retryTimer);
    };
  }, [runId]);

  return {
    ...state,
    connection,
    connectionError,
    addOptimisticUserMessage: (id, text) =>
      setState((current) => ({
        ...current,
        entries: [...current.entries, { id, kind: "user", text }],
        runActive: true,
      })),
    clear: () => setState(initialStreamState),
  };
}

function consumeSSEBlock(
  block: string,
  runId: string,
  lastSequence: MutableRefObject<number>,
  setState: Dispatch<SetStateAction<StreamState>>,
): boolean {
  let eventType = "message";
  const data: string[] = [];
  for (const line of block.split(/\r?\n/)) {
    if (line.startsWith("event:")) eventType = line.slice(6).trim();
    if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
  }
  if (!data.length) return false;
  try {
    const raw = JSON.parse(data.join("\n")) as Partial<AgentEvent> & Record<string, unknown>;
    const payload = raw.payload && typeof raw.payload === "object"
      ? raw.payload as Record<string, unknown>
      : Object.fromEntries(Object.entries(raw).filter(([key]) => !["runId", "sequence", "type", "createdAt"].includes(key)));
    const event: AgentEvent = {
      runId: typeof raw.runId === "string" ? raw.runId : runId,
      sequence: typeof raw.sequence === "number" ? raw.sequence : lastSequence.current + 1,
      type: typeof raw.type === "string" ? raw.type : eventType,
      payload,
      createdAt: typeof raw.createdAt === "string" ? raw.createdAt : new Date().toISOString(),
    };
    setState((current) => {
      const next = applyAgentEvent(current, event);
      lastSequence.current = next.lastSequence;
      return next;
    });
    return true;
  } catch {
    // A malformed event is skipped; a later sequence can still complete the stream.
    return false;
  }
}
