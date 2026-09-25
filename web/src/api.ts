import type {
  Agent,
  IconUpload,
  Attachment,
  ModelOption,
  Conversation,
  DesktopLease,
  DeviceCode,
  Run,
  Schedule,
  Session,
  SetupStatus,
  SidebarLayout,
} from "./types";

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly details?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

const API_BASE = import.meta.env.VITE_API_BASE_URL?.replace(/\/$/, "") ?? "";
export const resolveApiUrl = (path: string): string => `${API_BASE}${path}`;

type AgentSettingsInput = Pick<Agent, "name" | "rolePrompt" | "model" | "effort" | "permission"> & {
  icon?: IconUpload | null;
};

let csrfToken = "";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    credentials: "same-origin",
    ...init,
    headers: {
      Accept: "application/json",
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...(init?.method && !["GET", "HEAD", "OPTIONS"].includes(init.method) && csrfToken
        ? { "X-CSRF-Token": csrfToken }
        : {}),
      ...init?.headers,
    },
  });

  if (!response.ok) {
    const details = await response.json().catch(() => undefined);
    const message =
      typeof details === "object" && details && ("error" in details || "message" in details)
        ? String("error" in details ? details.error : details.message)
        : `Request failed (${response.status})`;
    if (response.status === 401 && path !== "/api/session") {
      window.dispatchEvent(new Event("codexbot:unauthorized"));
    }
    throw new ApiError(message, response.status, details);
  }

  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

const json = (value: unknown): string => JSON.stringify(value);

export const api = {
  getSetupStatus: () => request<SetupStatus>("/api/setup/status"),
  setup: (input: { username: string; password: string; bootstrapToken?: string }) =>
    request<{ status: "created" }>("/api/setup", {
      method: "POST",
      headers: input.bootstrapToken ? { "X-Bootstrap-Token": input.bootstrapToken } : undefined,
      body: json({ username: input.username, password: input.password }),
    }),
  getSession: async () => {
    const session = await request<Session>("/api/session");
    csrfToken = session.csrfToken;
    return session;
  },
  login: async (username: string, password: string) => {
    const session = await request<Session>("/api/session", {
      method: "POST",
      body: json({ username, password }),
    });
    csrfToken = session.csrfToken;
    return session;
  },
  logout: async () => {
    await request<void>("/api/session", { method: "DELETE" });
    csrfToken = "";
  },

  getModels: () => request<ModelOption[]>("/api/models"),
  listAgents: () => request<Agent[]>("/api/agents"),
  getSidebar: () => request<SidebarLayout>("/api/sidebar"),
  saveSidebar: (layout: SidebarLayout) =>
    request<SidebarLayout>("/api/sidebar", { method: "PUT", body: json(layout) }),
  createAgent: (input: AgentSettingsInput) =>
    request<Agent>("/api/agents", { method: "POST", body: json(input) }),
  updateAgent: (id: string, input: AgentSettingsInput) =>
    request<Agent>(`/api/agents/${id}`, { method: "PATCH", body: json(input) }),
  startAgent: (id: string) => request<Agent>(`/api/agents/${id}/start`, { method: "POST" }),
  stopAgent: (id: string) => request<void>(`/api/agents/${id}/stop`, { method: "POST" }),
  archiveAgent: (id: string) => request<void>(`/api/agents/${id}`, { method: "DELETE" }),

  createConversation: (agentId: string) =>
    request<Conversation>(`/api/agents/${agentId}/conversations`, { method: "POST" }),
  sendMessage: (agentId: string, prompt: string, conversationId?: string, attachments?: Attachment[]) =>
    request<{ runId: string; conversationId: string; threadId: string; turnId: string }>(`/api/agents/${agentId}/messages`, {
      method: "POST",
      body: json({ prompt, conversationId, attachments }),
    }),
  getAgentTimeline: (agentId: string, options: { after?: number; before?: number; limit?: number } = {}) => {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(options)) if (value !== undefined) params.set(key, String(value));
    return request<import("./types").AgentTimeline>(`/api/agents/${agentId}/timeline?${params}`);
  },
  getAgentHistory: (agentId: string, conversationId?: string) =>
    request<{ events: import("./types").AgentEvent[]; activeRunId?: string; conversationId?: string }>(`/api/agents/${agentId}/history${conversationId ? `?conversationId=${encodeURIComponent(conversationId)}` : ""}`),
  interrupt: (agentId: string) => request<void>(`/api/agents/${agentId}/interrupt`, { method: "POST" }),
  getRun: (runId: string) => request<Run>(`/api/runs/${runId}`),
  runEventsUrl: (runId: string, after: number) => `${API_BASE}/api/runs/${runId}/events?after=${after}`,

  desktopUrl: (agentId: string, sessionGeneration: number) => {
    const encodedAgentId = encodeURIComponent(agentId);
    const params = new URLSearchParams({
      autoconnect: "1",
      reconnect: "1",
      resize: "scale",
      path: `api/agents/${encodedAgentId}/desktop/websockify`,
      codexbotSession: String(sessionGeneration),
    });
    return `${API_BASE}/api/agents/${encodedAgentId}/desktop/?${params.toString()}`;
  },
  takeOverDesktop: (agentId: string) =>
    request<DesktopLease>(`/api/agents/${agentId}/desktop/takeover`, { method: "POST" }),
  releaseDesktop: (agentId: string) =>
    request<DesktopLease>(`/api/agents/${agentId}/desktop/release`, { method: "POST" }),
  heartbeatDesktop: (agentId: string) =>
    request<DesktopLease>(`/api/agents/${agentId}/desktop/heartbeat`, { method: "POST" }),

  listSchedules: (agentId?: string) =>
    request<Schedule[]>(`/api/schedules${agentId ? `?agentId=${agentId}` : ""}`),
  getSchedule: (id: string, signal?: AbortSignal) =>
    request<Schedule>(`/api/schedules/${encodeURIComponent(id)}`, { signal }),
  getScheduleRuns: (id: string, before?: string, signal?: AbortSignal) => {
    const params = new URLSearchParams({ limit: "20" });
    if (before) params.set("before", before);
    return request<{ runs: Run[]; nextCursor?: string }>(`/api/schedules/${encodeURIComponent(id)}/runs?${params}`, { signal });
  },
  getScheduleRunEvents: (id: string, runId: string, after = 0, signal?: AbortSignal) =>
    request<{ run: Run; events: import("./types").AgentEvent[]; nextSequence: number; hasMore: boolean }>(
      `/api/schedules/${encodeURIComponent(id)}/runs/${encodeURIComponent(runId)}/events?after=${after}&limit=100`, { signal }),
  createSchedule: (input: Pick<Schedule, "agentId" | "name" | "prompt" | "kind" | "expression" | "timezone" | "enabled">) =>
    request<Schedule>("/api/schedules", { method: "POST", body: json(input) }),
  setScheduleEnabled: (id: string, enabled: boolean) =>
    request<Schedule>(`/api/schedules/${id}`, { method: "PATCH", body: json({ enabled }) }),
  deleteSchedule: (id: string) =>
    request<void>(`/api/schedules/${id}`, { method: "DELETE" }),

  startDeviceCode: (agentId: string) =>
    request<DeviceCode>(`/api/agents/${agentId}/auth/device`, { method: "POST" }),
  getProviderAuth: (agentId: string) =>
    request<{ state: Agent["providerAuthState"]; method?: string; accountLabel?: string }>(`/api/agents/${agentId}/auth`),
  registerApiKey: (agentId: string, apiKey: string) =>
    request<void>(`/api/agents/${agentId}/auth/api-key`, {
      method: "POST",
      body: json({ apiKey }),
    }),
  logoutProvider: (agentId: string) =>
    request<void>(`/api/agents/${agentId}/auth/logout`, { method: "POST" }),
};
