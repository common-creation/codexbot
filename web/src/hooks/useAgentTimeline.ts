import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { entriesFromHistory } from "../streamModel";
import type { AgentEvent, AgentTimeline } from "../types";

function mergeEvents(current: AgentEvent[], incoming: AgentEvent[]): AgentEvent[] {
  const events = new Map(current.map((event) => [event.sequence, event]));
  for (const event of incoming) events.set(event.sequence, event);
  return [...events.values()].sort((a, b) => a.sequence - b.sequence);
}

export function useAgentTimeline(agentId: string, revision = 0) {
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [metadata, setMetadata] = useState<Pick<AgentTimeline, "conversationId" | "activeRunId" | "runtimeBusy">>();
  const [connection, setConnection] = useState<"loading" | "live" | "retrying">("loading");
  const [connectionError, setConnectionError] = useState<string>();
  const [hasEarlier, setHasEarlier] = useState(false);
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const refreshRef = useRef<() => Promise<void>>(async () => {});
  const generation = useRef(0);
  const revisionRef = useRef(revision);
  revisionRef.current = revision;

  useEffect(() => {
    const currentGeneration = ++generation.current;
    let active = true;
    let cursor: number | undefined;
    let timer: ReturnType<typeof setTimeout>;
    let pending: Promise<void> | undefined;
    let refreshAgain = false;
    setEvents([]);
    setMetadata(undefined);
    setConnection("loading");
    setHasEarlier(false);
    setLoadingEarlier(false);
    const refresh = (): Promise<void> => {
      if (pending) { refreshAgain = true; return pending; }
      clearTimeout(timer);
      pending = (async () => {
        try {
          let more: boolean;
          do {
            refreshAgain = false;
            const initial = cursor === undefined;
            const requestRevision = revisionRef.current;
            const result = await api.getAgentTimeline(agentId, { ...(initial ? {} : { after: cursor }), limit: 100 });
            if (!active) return;
            // A response begun before New chat must not restore its old context ID.
            if (requestRevision !== revisionRef.current) { more = true; continue; }
            setEvents((current) => mergeEvents(current, result.events));
            setMetadata({ conversationId: result.conversationId, activeRunId: result.activeRunId, runtimeBusy: result.runtimeBusy });
            if (initial) setHasEarlier(result.hasMore);
            // The API cursor is the last returned event, never a future high-water mark.
            cursor = result.lastSequence;
            more = !initial && result.hasMore;
            setConnection("live");
            setConnectionError(undefined);
          } while (active && (more || refreshAgain));
        } catch (error) {
          if (active) {
            setConnection("retrying");
            setConnectionError(error instanceof Error ? error.message : "Could not load the agent timeline.");
          }
        } finally {
          pending = undefined;
          if (active) timer = setTimeout(() => void refresh(), 750);
        }
      })();
      return pending;
    };
    refreshRef.current = refresh;
    void refresh();
    return () => {
      active = false;
      clearTimeout(timer);
      if (generation.current === currentGeneration) generation.current++;
    };
  }, [agentId]);

  useEffect(() => {
    if (revision !== 0) void refreshRef.current();
  }, [revision]);

  const loadEarlier = useCallback(async () => {
    if (loadingEarlier || !hasEarlier || events.length === 0) return;
    const currentGeneration = generation.current;
    setLoadingEarlier(true);
    try {
      const result = await api.getAgentTimeline(agentId, { before: events[0].sequence, limit: 100 });
      if (generation.current !== currentGeneration) return;
      setEvents((current) => mergeEvents(current, result.events));
      setHasEarlier(result.hasMore);
    } catch (error) {
      if (generation.current === currentGeneration) setConnectionError(error instanceof Error ? error.message : "Could not load earlier messages.");
    } finally {
      if (generation.current === currentGeneration) setLoadingEarlier(false);
    }
  }, [agentId, events, hasEarlier, loadingEarlier]);

  return {
    entries: useMemo(() => entriesFromHistory(events, agentId), [events, agentId]),
    ...metadata,
    runActive: Boolean(metadata?.activeRunId),
    connection, connectionError, hasEarlier, loadingEarlier, loadEarlier,
    refresh: useCallback(() => refreshRef.current(), []),
  };
}
