import { useState } from "react";
import { resolveApiUrl } from "../api";
import type { Agent } from "../types";

type AvatarAgent = Pick<Agent, "id" | "name"> & { iconUrl?: string };

export function AgentAvatar({ agent, size = "normal" }: { agent: AvatarAgent; size?: "small" | "normal" | "large" }) {
  const initial = Array.from(agent.name.trim())[0]?.toUpperCase() || "?";
  const source = agent.iconUrl?.startsWith("data:") ? agent.iconUrl : agent.iconUrl ? resolveApiUrl(agent.iconUrl) : undefined;

  return (
    <span className={`agent-avatar ${size}`} aria-hidden="true">
      {source ? <AvatarImage key={`${agent.id}:${source}`} source={source} initial={initial} /> : <span>{initial}</span>}
    </span>
  );
}

function AvatarImage({ source, initial }: { source: string; initial: string }) {
  const [failed, setFailed] = useState(false);
  return failed ? <span>{initial}</span> : <img src={source} alt="" onError={() => setFailed(true)} />;
}
