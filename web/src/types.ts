export type RuntimeState = "stopped" | "starting" | "running" | "error";

export type Permission = "auto" | "full-access";

export interface SidebarSection {
  id: string;
  name: string;
  agentIds: string[];
}

export interface SidebarLayout {
  revision: number;
  sections: SidebarSection[];
  unsectionedAgentIds: string[];
}

export interface Agent {
  id: string;
  name: string;
  rolePrompt: string;
  model?: string;
  effort?: string;
  permission?: Permission;
  roleVersion: number;
  status: RuntimeState;
  providerAuthState: "connected" | "disconnected" | "pending";
  createdAt: string;
  updatedAt: string;
}

export interface Conversation {
  id: string;
  agentId: string;
  codexThreadId?: string;
  kind: "agent" | "manual" | "scheduled" | "collaboration";
  roleVersion: number;
  title: string;
  createdAt: string;
}

export type RunStatus =
  | "queued"
  | "running"
  | "completed"
  | "failed"
  | "interrupted"
  | "unknown"
  | "skipped_overlap";

export interface Run {
  id: string;
  agentId: string;
  source: string;
  conversationId: string;
  codexTurnId?: string;
  prompt: string;
  status: RunStatus;
  scheduledFor?: string;
  startedAt?: string;
  finishedAt?: string;
  error?: string;
}

export interface AgentEvent {
  runId: string;
  sequence: number;
  type: string;
  payload: Record<string, unknown>;
  createdAt: string;
}

export interface Schedule {
  id: string;
  agentId: string;
  prompt: string;
  name: string;
  kind: "once" | "cron";
  expression: string;
  timezone: string;
  enabled: boolean;
  nextRunAt?: string;
  lastRunAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface DesktopLease {
  agentId: string;
  holder: "agent" | "human";
  generation: number;
  expiresAt: string;
  continueRunId?: string;
  continueError?: string;
}

export interface DeviceCode {
  loginId: string;
  verificationUrl: string;
  userCode: string;
}

export interface Session { username: string; authenticated: true; csrfToken: string; expiresAt?: string }
export interface SetupStatus { required: boolean }

export interface ModelOption {
  id: string;
  name: string;
  efforts: string[];
}

export interface Attachment {
  name: string;
  data: string;
}

export interface AgentTimeline {
  events: AgentEvent[];
  conversationId: string;
  activeRunId?: string;
  runtimeBusy?: boolean;
  lastSequence: number;
  hasMore: boolean;
  beforeSequence?: number;
}
