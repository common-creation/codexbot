import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App, ChatPanel } from "./App";
import { api, ApiError } from "./api";
import type { Agent, AgentEvent, ModelOption, Permission, Schedule } from "./types";

vi.mock("./api", () => ({
  ApiError: class ApiError extends Error { constructor(message: string, readonly status: number) { super(message); } },
  api: {
    getSetupStatus: vi.fn(),
    getSession: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    listAgents: vi.fn(),
    getSidebar: vi.fn(),
    saveSidebar: vi.fn(),
    getModels: vi.fn(),
    createAgent: vi.fn(),
    updateAgent: vi.fn(),
    listSchedules: vi.fn(),
    getSchedule: vi.fn(),
    getScheduleRuns: vi.fn(),
    getScheduleRunEvents: vi.fn(),
    setScheduleEnabled: vi.fn(),
    getProviderAuth: vi.fn(),
    getAgentTimeline: vi.fn(),
    createConversation: vi.fn(),
    sendMessage: vi.fn(),
    interrupt: vi.fn(),
    runEventsUrl: vi.fn(),
    desktopUrl: vi.fn(),
    takeOverDesktop: vi.fn(),
    releaseDesktop: vi.fn(),
    heartbeatDesktop: vi.fn(),
  },
}));


const agent: Agent = {
  id: "agent-1",
  name: "General",
  rolePrompt: "Help the user",
  roleVersion: 1,
  status: "running",
  providerAuthState: "connected",
  createdAt: "2026-09-03T00:00:00Z",
  updatedAt: "2026-09-03T00:00:00Z",
};

beforeEach(() => {
  vi.mocked(api.getSidebar).mockResolvedValue({ revision: 0, sections: [], unsectionedAgentIds: [] });
  vi.mocked(api.getModels).mockReset().mockResolvedValue([]);
  vi.mocked(api.getAgentTimeline).mockReset().mockResolvedValue({ events: [], conversationId: "conversation-1", lastSequence: 0, hasMore: false });
});
afterEach(cleanup);

function panel() {
  return render(<StrictMode><ChatPanel agent={agent} activeRunId={undefined} setActiveRunId={vi.fn()} conversationId="conversation-1" setConversationId={vi.fn()} onError={vi.fn()} /></StrictMode>);
}

describe("Chat Markdown", () => {
  const markdown = [
    "現在、次のエージェントが利用できます。",
    "",
    "| エージェント | 役割 |",
    "|---|---|",
    "| **manager（私）** | ユーザーとの対話、作業の調整、結果の取りまとめ |",
    "| **planner** | 依頼の分解、制約の整理、設計 |",
    "",
    "全員 **gpt-6-astra** を使用しています。",
  ].join("\n");

  it("renders persisted user and assistant messages as GitHub-flavored Markdown", async () => {
    vi.mocked(api.getAgentTimeline).mockResolvedValue({
      events: [
        { runId: "run-1", sequence: 1, type: "message.user", payload: { text: "**エージェント一覧**を表示してください。" }, createdAt: agent.createdAt },
        { runId: "run-1", sequence: 2, type: "message.assistant.completed", payload: { itemId: "answer", text: markdown }, createdAt: agent.createdAt },
      ],
      conversationId: "conversation-1", lastSequence: 2, hasMore: false,
    });
    panel();

    expect(await screen.findByRole("table")).toBeInTheDocument();
    expect(screen.getAllByRole("columnheader").map((header) => header.textContent)).toEqual(["エージェント", "役割"]);
    expect(screen.getAllByRole("row")).toHaveLength(3);
    expect(screen.getByRole("cell", { name: "manager（私）" }).querySelector("strong")).toHaveTextContent("manager（私）");
    expect(screen.getByText("gpt-6-astra").tagName).toBe("STRONG");
    expect(screen.getByText("エージェント一覧").tagName).toBe("STRONG");
    expect(screen.queryByText("|---|---|")).not.toBeInTheDocument();
  });

  it("reparses an assistant message when incremental Markdown arrives", async () => {
    const prefix = "| エージェント | 役割 |\n|---|---|\n| **plan";
    vi.mocked(api.getAgentTimeline).mockResolvedValue({
      events: [{ runId: "run-1", sequence: 1, type: "message.assistant.delta", payload: { itemId: "answer", delta: prefix }, createdAt: agent.createdAt }],
      conversationId: "conversation-1", activeRunId: "run-1", lastSequence: 1, hasMore: false,
    });
    const props = { agent, activeRunId: undefined, setActiveRunId: vi.fn(), conversationId: "conversation-1", setConversationId: vi.fn(), onError: vi.fn() };
    const view = render(<ChatPanel {...props} />);
    expect(await screen.findByRole("cell", { name: "**plan" })).toBeInTheDocument();

    vi.mocked(api.getAgentTimeline).mockResolvedValue({
      events: [{ runId: "run-1", sequence: 2, type: "message.assistant.delta", payload: { itemId: "answer", delta: "ner** | 依頼の分解、制約の整理、設計 |" }, createdAt: agent.createdAt }],
      conversationId: "conversation-1", activeRunId: "run-1", lastSequence: 2, hasMore: false,
    });
    view.rerender(<ChatPanel {...props} timelineRevision={1} />);

    expect((await screen.findByRole("cell", { name: "planner" })).querySelector("strong")).toHaveTextContent("planner");
    expect(screen.getByRole("cell", { name: "依頼の分解、制約の整理、設計" })).toBeInTheDocument();
    expect(screen.getAllByRole("table")).toHaveLength(1);
    expect(screen.queryByText("**plan")).not.toBeInTheDocument();
  });
});

describe("Workspace navigation", () => {
  beforeEach(() => {
    vi.mocked(api.getSetupStatus).mockResolvedValue({ required: false });
    vi.mocked(api.getSession).mockResolvedValue({ authenticated: true, username: "mohemohe", csrfToken: "csrf" });
    vi.mocked(api.listAgents).mockResolvedValue([]);
    vi.mocked(api.getProviderAuth).mockReset();
  });

  it("shows the session username and signs out only from the Sign out button", async () => {
    vi.mocked(api.logout).mockReset().mockResolvedValue(undefined);
    render(<App />);

    const username = await screen.findByText("mohemohe");
    fireEvent.click(username);
    fireEvent.click(screen.getByText("MO"));
    fireEvent.click(username.closest(".account-row")!);
    expect(api.logout).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(api.logout).toHaveBeenCalledTimes(1));
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
  });

  it("shows the username returned by login immediately", async () => {
    vi.mocked(api.getSession).mockRejectedValueOnce(new Error("Unauthorized"));
    vi.mocked(api.login).mockResolvedValue({ authenticated: true, username: "signed-in-user", csrfToken: "csrf" });
    render(<App />);

    fireEvent.change(await screen.findByLabelText("Username"), { target: { value: "signed-in-user" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct horse battery staple" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("signed-in-user")).toBeInTheDocument();
  });

  it("shows the Connections page before an agent has been created", async () => {
    render(<StrictMode><App /></StrictMode>);

    fireEvent.click(await screen.findByRole("button", { name: "Connections" }));

    expect(await screen.findByRole("heading", { name: "OpenAI connection" })).toBeInTheDocument();
    expect(screen.getByText("Create an agent before connecting it to OpenAI.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create an agent" })).toBeInTheDocument();
    expect(api.getProviderAuth).not.toHaveBeenCalled();
  });
});

describe("New chat", () => {
  const newConversation = { id: "fresh-context", agentId: agent.id, kind: "agent" as const, roleVersion: 1, title: "General", createdAt: agent.createdAt };
  const earlier: AgentEvent = { runId: "old-run", sequence: 1, type: "message.user", payload: { text: "Previous task" }, createdAt: agent.createdAt };
  const boundary: AgentEvent = { runId: "", sequence: 2, type: "conversation.reset", payload: {
    id: "conversation-fresh-context", conversationId: newConversation.id,
    text: "New chat started. Earlier messages remain visible, but are not included in the new context.",
  }, createdAt: agent.createdAt };

  beforeEach(() => {
    vi.mocked(api.getSetupStatus).mockResolvedValue({ required: false });
    vi.mocked(api.getSession).mockResolvedValue({ authenticated: true, username: "mohemohe", csrfToken: "csrf" });
    vi.mocked(api.listAgents).mockResolvedValue([agent]);
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [earlier], conversationId: "old-context", lastSequence: 1, hasMore: false });
    vi.mocked(api.createConversation).mockReset().mockResolvedValue(newConversation);
    vi.mocked(api.sendMessage).mockReset().mockResolvedValue({ runId: "new-run", conversationId: newConversation.id, threadId: "new-thread", turnId: "new-turn" });
  });

  it("starts a fresh context, retains the draft and earlier timeline, and sends to the new conversation", async () => {
    render(<App />);
    expect(await screen.findByText("Previous task")).toBeInTheDocument();
    const composer = screen.getByRole("textbox", { name: "Message General" });
    fireEvent.change(composer, { target: { value: "Use /workspace next" } });
    fireEvent.click(screen.getAllByText("General").find((element) => element.closest("button"))!.closest("button")!);
    expect(screen.getByRole("button", { name: "New chat" })).toBeEnabled();
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [boundary], conversationId: newConversation.id, lastSequence: 2, hasMore: false });
    fireEvent.click(screen.getByRole("button", { name: "New chat" }));
    expect(await screen.findByRole("separator", { name: "New chat" })).toHaveTextContent("Earlier messages remain visible");
    expect(api.createConversation).toHaveBeenCalledWith(agent.id);
    expect(screen.getByText("Previous task")).toBeInTheDocument();
    expect(composer).toHaveValue("Use /workspace next");
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalledWith(agent.id, "Use /workspace next", newConversation.id));
  });

  it("disables New chat during an active run", async () => {
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [earlier], conversationId: "old-context", activeRunId: "active-run", lastSequence: 1, hasMore: false });
    render(<App />);
    expect(await screen.findByRole("button", { name: "Stop run" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New chat" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "New chat" }));
    expect(api.createConversation).not.toHaveBeenCalled();
  });

  it("disables New chat while a message is being sent", async () => {
    let resolve!: (value: Awaited<ReturnType<typeof api.sendMessage>>) => void;
    vi.mocked(api.sendMessage).mockImplementation(() => new Promise((done) => { resolve = done; }));
    render(<App />);
    await screen.findByText("Previous task");
    fireEvent.change(screen.getByRole("textbox", { name: "Message General" }), { target: { value: "Task" } });
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    expect(screen.getByRole("button", { name: "New chat" })).toBeDisabled();
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalled());
    await act(async () => resolve({ runId: "new-run", conversationId: "old-context", threadId: "thread", turnId: "turn" }));
  });

  it("disables reset and sends until the reset request completes", async () => {
    let resolve!: (value: typeof newConversation) => void;
    vi.mocked(api.createConversation).mockImplementation(() => new Promise((done) => { resolve = done; }));
    render(<App />);
    await screen.findByText("Previous task");
    fireEvent.change(screen.getByRole("textbox", { name: "Message General" }), { target: { value: "Next task" } });
    fireEvent.click(screen.getByRole("button", { name: "New chat" }));
    expect(screen.getByRole("button", { name: "Starting…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Send message" })).toBeDisabled();
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [boundary], conversationId: newConversation.id, lastSequence: 2, hasMore: false });
    await act(async () => resolve(newConversation));
    expect(screen.getByRole("button", { name: "New chat" })).toBeEnabled();
  });

  it("does not apply a pending reset to another agent after navigation", async () => {
    const second = { ...agent, id: "agent-2", name: "Research" };
    vi.mocked(api.listAgents).mockResolvedValue([agent, second]);
    vi.mocked(api.getAgentTimeline).mockImplementation(async (id) => ({ events: id === agent.id ? [earlier] : [], conversationId: id === agent.id ? "old-context" : "research-context", lastSequence: id === agent.id ? 1 : 0, hasMore: false }));
    let resolve!: (value: typeof newConversation) => void;
    vi.mocked(api.createConversation).mockImplementation(() => new Promise((done) => { resolve = done; }));
    render(<App />);
    await screen.findByText("Previous task");
    await waitFor(() => expect(screen.getByRole("button", { name: "New chat" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "New chat" }));
    fireEvent.click(screen.getByText("Research").closest("button")!);
    const composer = await screen.findByRole("textbox", { name: "Message Research" });
    await act(async () => resolve(newConversation));
    fireEvent.change(composer, { target: { value: "Check this" } });
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalledWith(second.id, "Check this", "research-context"));
  });

  it("preserves a rejected draft and refreshes its context before an explicit retry", async () => {
    render(<App />);
    await screen.findByText("Previous task");
    const composer = screen.getByRole("textbox", { name: "Message General" });
    fireEvent.change(composer, { target: { value: "Keep this draft" } });
    vi.mocked(api.sendMessage).mockRejectedValueOnce(new ApiError("The chat context changed. Send the message again.", 409));
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [boundary], conversationId: newConversation.id, lastSequence: 2, hasMore: false });
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The chat context changed");
    await screen.findByRole("separator", { name: "New chat" });
    expect(composer).toHaveValue("Keep this draft");
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(api.sendMessage).toHaveBeenLastCalledWith(agent.id, "Keep this draft", newConversation.id));
  });
});

describe("Desktop sheet", () => {
  let fullscreenElement: Element | null;
  let originalFullscreenElement: PropertyDescriptor | undefined;
  let originalExitFullscreen: PropertyDescriptor | undefined;
  const exitFullscreen = vi.fn();

  beforeEach(() => {
    vi.mocked(api.getSetupStatus).mockResolvedValue({ required: false });
    vi.mocked(api.getSession).mockResolvedValue({ authenticated: true, username: "mohemohe", csrfToken: "csrf" });
    vi.mocked(api.listAgents).mockResolvedValue([agent]);
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [], conversationId: "conversation-1", lastSequence: 0, hasMore: false });
    vi.mocked(api.desktopUrl).mockReset().mockImplementation((id, generation) => `/api/agents/${id}/desktop/?session=${generation}`);
    vi.mocked(api.takeOverDesktop).mockReset().mockResolvedValue({ agentId: agent.id, holder: "human", generation: 1, expiresAt: "2026-09-11T01:00:00Z" });
    vi.mocked(api.releaseDesktop).mockReset().mockResolvedValue({ agentId: agent.id, holder: "agent", generation: 2, expiresAt: "2026-09-11T01:00:00Z" });
    fullscreenElement = null;
    originalFullscreenElement = Object.getOwnPropertyDescriptor(document, "fullscreenElement");
    originalExitFullscreen = Object.getOwnPropertyDescriptor(document, "exitFullscreen");
    Object.defineProperty(document, "fullscreenElement", { configurable: true, get: () => fullscreenElement });
    exitFullscreen.mockReset().mockImplementation(async () => {
      fullscreenElement = null;
      fireEvent(document, new Event("fullscreenchange"));
    });
    Object.defineProperty(document, "exitFullscreen", { configurable: true, value: exitFullscreen });
  });

  afterEach(() => {
    if (originalFullscreenElement) Object.defineProperty(document, "fullscreenElement", originalFullscreenElement);
    else Reflect.deleteProperty(document, "fullscreenElement");
    if (originalExitFullscreen) Object.defineProperty(document, "exitFullscreen", originalExitFullscreen);
    else Reflect.deleteProperty(document, "exitFullscreen");
  });

  async function openDesktop() {
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Desktop" }));
    return screen.findByRole("dialog", { name: "General desktop" });
  }

  it("keeps the chat draft and attachments mounted while opening and closing the sheet", async () => {
    render(<App />);
    const composer = await screen.findByRole("textbox", { name: "Message General" });
    fireEvent.change(composer, { target: { value: "Continue after checking the desktop" } });
    fireEvent.change(screen.getByLabelText("Attach files"), { target: { files: [new File(["notes"], "notes.txt")] } });
    const trigger = screen.getByRole("button", { name: "Desktop" });
    trigger.focus();
    fireEvent.click(trigger);

    expect(await screen.findByRole("dialog", { name: "General desktop" })).toBeInTheDocument();
    expect(composer).toBeInTheDocument();
    expect(composer).toHaveValue("Continue after checking the desktop");
    fireEvent.click(screen.getByRole("button", { name: "Close desktop" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("textbox", { name: "Message General" })).toBe(composer);
    expect(composer).toHaveValue("Continue after checking the desktop");
    expect(screen.getByRole("button", { name: "Remove notes.txt" })).toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());
  });

  it("retains takeover and release controls and reconnects KasmVNC after each control change", async () => {
    await openDesktop();
    const initialFrame = screen.getByTitle("General desktop");
    expect(initialFrame).toHaveAttribute("allowfullscreen");
    expect(initialFrame.getAttribute("allow")).toContain("clipboard-read");
    expect(initialFrame.getAttribute("allow")).toContain("clipboard-write");
    fireEvent.click(screen.getByRole("button", { name: "Take control" }));
    expect(await screen.findByRole("button", { name: "Release control" })).toBeInTheDocument();
    expect(api.takeOverDesktop).toHaveBeenCalledWith(agent.id);
    const controlFrame = screen.getByTitle("General desktop");
    expect(controlFrame).not.toBe(initialFrame);
    expect(controlFrame).toHaveAttribute("src", `/api/agents/${agent.id}/desktop/?session=1`);

    fireEvent.click(screen.getByRole("button", { name: "Release control" }));
    expect(await screen.findByRole("button", { name: "Take control" })).toBeInTheDocument();
    expect(api.releaseDesktop).toHaveBeenCalledWith(agent.id);
    expect(screen.getByTitle("General desktop")).not.toBe(controlFrame);
    expect(screen.getByTitle("General desktop")).toHaveAttribute("src", `/api/agents/${agent.id}/desktop/?session=2`);
  });

  it("maximizes the sheet with native fullscreen and handles browser exit without reconnecting KasmVNC", async () => {
    const dialog = await openDesktop();
    const frame = screen.getByTitle("General desktop");
    const requestFullscreen = vi.fn(async () => {
      fullscreenElement = dialog;
      fireEvent(document, new Event("fullscreenchange"));
    });
    Object.defineProperty(dialog, "requestFullscreen", { configurable: true, value: requestFullscreen });

    fireEvent.click(screen.getByRole("button", { name: "Maximize desktop" }));
    expect(await screen.findByRole("button", { name: "Restore desktop" })).toBeInTheDocument();
    expect(requestFullscreen).toHaveBeenCalledOnce();
    expect(screen.getByTitle("General desktop")).toBe(frame);

    fullscreenElement = null;
    fireEvent(document, new Event("fullscreenchange"));
    expect(await screen.findByRole("button", { name: "Maximize desktop" })).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "General desktop" })).toBe(dialog);

    fireEvent.click(screen.getByRole("button", { name: "Maximize desktop" }));
    fireEvent.click(await screen.findByRole("button", { name: "Restore desktop" }));
    await waitFor(() => expect(exitFullscreen).toHaveBeenCalledOnce());
    expect(screen.getByTitle("General desktop")).toBe(frame);
    expect(await screen.findByRole("button", { name: "Maximize desktop" })).toBeInTheDocument();
  });

  it("can maximize and restore when the Fullscreen API is unavailable", async () => {
    const dialog = await openDesktop();
    Object.defineProperty(dialog, "requestFullscreen", { configurable: true, value: undefined });
    const frame = screen.getByTitle("General desktop");

    fireEvent.click(screen.getByRole("button", { name: "Maximize desktop" }));
    fireEvent.click(await screen.findByRole("button", { name: "Restore desktop" }));

    expect(await screen.findByRole("button", { name: "Maximize desktop" })).toBeInTheDocument();
    expect(screen.getByTitle("General desktop")).toBe(frame);
    expect(exitFullscreen).not.toHaveBeenCalled();
  });
});

describe("Agent model settings", () => {
  beforeEach(() => {
    vi.mocked(api.getSetupStatus).mockResolvedValue({ required: false });
    vi.mocked(api.getSession).mockResolvedValue({ authenticated: true, username: "mohemohe", csrfToken: "csrf" });
    vi.mocked(api.listAgents).mockResolvedValue([]);
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [], conversationId: "conversation-1", lastSequence: 0, hasMore: false });
    vi.mocked(api.getProviderAuth).mockResolvedValue({ state: "connected" });
    vi.mocked(api.createAgent).mockReset().mockResolvedValue(agent);
    vi.mocked(api.updateAgent).mockReset().mockResolvedValue(agent);
  });

  it.each(["Add agent", "Create an agent"])("dismisses the creation dialog with Escape and restores focus to %s", async (triggerName) => {
    render(<App />);
    const trigger = await screen.findByRole("button", { name: triggerName });
    trigger.focus();
    fireEvent.click(trigger);

    const dialog = screen.getByRole("dialog", { name: "Create an agent" });
    expect(dialog).toHaveAccessibleDescription(/Give this agent a name and a durable role/);
    expect(screen.getByLabelText("Agent name")).toHaveFocus();
    trigger.focus();
    expect(dialog).toContainElement(document.activeElement as HTMLElement);

    fireEvent.keyDown(dialog, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(api.createAgent).not.toHaveBeenCalled();
  });

  it("dismisses agent settings with Escape and restores focus without saving", async () => {
    vi.mocked(api.listAgents).mockResolvedValue([agent]);
    render(<App />);
    const trigger = await screen.findByRole("button", { name: "Agent settings" });
    trigger.focus();
    fireEvent.click(trigger);
    const dialog = screen.getByRole("dialog", { name: "Edit General" });
    fireEvent.change(screen.getByLabelText("Agent name"), { target: { value: "Unsaved name" } });
    fireEvent.keyDown(dialog, { key: "Escape" });

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(trigger).toHaveFocus());
    expect(api.updateAgent).not.toHaveBeenCalled();
  });

  it.each<Permission>(["auto", "full-access"])("saves %s permission when creating an agent", async (permission) => {
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Create an agent" }));
    expect(screen.getByLabelText("Permission")).toHaveValue("auto");
    expect(screen.getByRole("option", { name: "Auto" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Full Access" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Agent name"), { target: { value: "Research" } });
    fireEvent.change(screen.getByLabelText("Role and instructions"), { target: { value: "Investigate" } });
    fireEvent.change(screen.getByLabelText("Permission"), { target: { value: permission } });
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() => expect(api.createAgent).toHaveBeenCalledWith({
      name: "Research", rolePrompt: "Investigate", model: "", effort: "", permission,
    }));
  });

  it.each<Permission>(["auto", "full-access"])("loads and updates %s permission when editing an agent", async (permission) => {
    const nextPermission = permission === "auto" ? "full-access" : "auto";
    vi.mocked(api.listAgents).mockResolvedValue([{ ...agent, permission }]);
    vi.mocked(api.updateAgent).mockResolvedValue({ ...agent, permission: nextPermission, roleVersion: 2 });
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Agent settings" }));
    expect(screen.getByLabelText("Permission")).toHaveValue(permission);
    fireEvent.change(screen.getByLabelText("Permission"), { target: { value: nextPermission } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(api.updateAgent).toHaveBeenCalledWith(agent.id, {
      name: agent.name, rolePrompt: agent.rolePrompt, model: "", effort: "", permission: nextPermission,
    }));
  });

  it("defaults legacy agents to Auto and preserves the timeline when permission changes", async () => {
    vi.mocked(api.listAgents).mockResolvedValue([agent]);
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [{
      runId: "run-old", sequence: 1, type: "message.user", payload: { text: "Previous conversation" },
      createdAt: "2026-09-03T00:00:00Z",
    }], conversationId: "old-conversation", lastSequence: 1, hasMore: false });
    vi.mocked(api.updateAgent).mockResolvedValue({ ...agent, permission: "full-access", roleVersion: 2 });
    render(<App />);
    expect(await screen.findByText("Previous conversation")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Agent settings" }));
    expect(screen.getByLabelText("Permission")).toHaveValue("auto");
    fireEvent.change(screen.getByLabelText("Permission"), { target: { value: "full-access" } });
    expect(screen.getByText("Automatically allows all permission requests.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(api.updateAgent).toHaveBeenCalled());
    expect(screen.getByText("Previous conversation")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New chat" })).toBeInTheDocument();
  });

  it("uses the startup catalog and its model-specific efforts", async () => {
    vi.mocked(api.getModels).mockResolvedValue([{ id: "remote-model", name: "Remote Model", efforts: ["low", "future-effort"] }]);
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Create an agent" }));
    expect(await screen.findByRole("option", { name: "Remote Model (remote-model)" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "gpt-6-astra" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Agent name"), { target: { value: "Research" } });
    fireEvent.change(screen.getByLabelText("Role and instructions"), { target: { value: "Investigate" } });
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "remote-model" } });
    expect(screen.queryByRole("option", { name: "ultra" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Effort"), { target: { value: "future-effort" } });
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() => expect(api.createAgent).toHaveBeenCalledWith({ name: "Research", rolePrompt: "Investigate", model: "remote-model", effort: "future-effort", permission: "auto" }));
    fireEvent.click(await screen.findByRole("button", { name: "Agent settings" }));
    expect(api.getModels).toHaveBeenCalledTimes(1);
  });

  it("keeps fallback choices available when the catalog request fails", async () => {
    vi.mocked(api.getModels).mockRejectedValue(new Error("unavailable"));
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Create an agent" }));
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "gpt-6-astra" } });
    expect(screen.getByRole("option", { name: "ultra" })).toBeInTheDocument();
  });

  it("recognizes a saved model when the catalog arrives after opening edit", async () => {
    let resolve!: (items: ModelOption[]) => void;
    vi.mocked(api.getModels).mockImplementation(() => new Promise((done) => { resolve = done; }));
    vi.mocked(api.listAgents).mockResolvedValue([{ ...agent, model: "remote-model", effort: "high" }]);
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Agent settings" }));
    expect(screen.getByLabelText("Model ID")).toHaveValue("remote-model");
    resolve([{ id: "remote-model", name: "Remote", efforts: ["high"] }]);
    await waitFor(() => expect(screen.getByLabelText("Model")).toHaveValue("remote-model"));
    expect(screen.queryByLabelText("Model ID")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Effort")).toHaveValue("high");
  });

  it("saves model and effort when creating an agent", async () => {
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Create an agent" }));
    expect(screen.getByLabelText("Model")).toHaveValue("");
    expect(screen.getByLabelText("Effort")).toHaveValue("");
    fireEvent.change(screen.getByLabelText("Agent name"), { target: { value: "Research" } });
    fireEvent.change(screen.getByLabelText("Role and instructions"), { target: { value: "Investigate" } });
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "gpt-6-astra" } });
    fireEvent.change(screen.getByLabelText("Effort"), { target: { value: "high" } });
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() => expect(api.createAgent).toHaveBeenCalledWith({ name: "Research", rolePrompt: "Investigate", model: "gpt-6-astra", effort: "high", permission: "auto" }));
  });

  it("loads custom settings and can reset them to defaults", async () => {
    vi.mocked(api.listAgents).mockResolvedValue([{ ...agent, model: "my-custom-model", effort: "high" }]);
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Agent settings" }));
    expect(screen.getByLabelText("Model")).toHaveValue("custom");
    expect(screen.getByLabelText("Model ID")).toHaveValue("my-custom-model");
    expect(screen.getByLabelText("Effort")).toHaveValue("high");
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "" } });
    expect(screen.getByLabelText("Effort")).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(api.updateAgent).toHaveBeenCalledWith(agent.id, { name: agent.name, rolePrompt: agent.rolePrompt, model: "", effort: "", permission: "auto" }));
  });

  it("resets effort on model changes and saves a custom model", async () => {
    vi.mocked(api.listAgents).mockResolvedValue([{ ...agent, model: "gpt-6-astra", effort: "ultra" }]);
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Agent settings" }));
    expect(screen.getByLabelText("Effort")).toHaveValue("ultra");
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "gpt-5.5" } });
    expect(screen.getByLabelText("Effort")).toHaveValue("");
    expect(screen.queryByRole("option", { name: "ultra" })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "custom" } });
    fireEvent.change(screen.getByLabelText("Model ID"), { target: { value: "custom-model" } });
    fireEvent.change(screen.getByLabelText("Effort"), { target: { value: "medium" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(api.updateAgent).toHaveBeenCalledWith(agent.id, { name: agent.name, rolePrompt: agent.rolePrompt, model: "custom-model", effort: "medium", permission: "auto" }));
  });
});

describe("Schedule controls", () => {
  const schedule: Schedule = {
    id: "schedule-1", agentId: agent.id, name: "Morning review", prompt: "Review tasks",
    kind: "cron", expression: "0 9 * * 1-5", timezone: "Asia/Tokyo", enabled: true,
    createdAt: agent.createdAt, updatedAt: agent.updatedAt,
  };

  beforeEach(() => {
    vi.mocked(api.getSetupStatus).mockResolvedValue({ required: false });
    vi.mocked(api.getSession).mockResolvedValue({ authenticated: true, username: "mohemohe", csrfToken: "csrf" });
    vi.mocked(api.listAgents).mockResolvedValue([agent]);
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [], conversationId: "conversation-1", lastSequence: 0, hasMore: false });
    vi.mocked(api.listSchedules).mockReset().mockResolvedValue([schedule]);
    vi.mocked(api.setScheduleEnabled).mockReset();
    vi.mocked(api.getSchedule).mockReset().mockResolvedValue(schedule);
    vi.mocked(api.getScheduleRuns).mockReset().mockResolvedValue({ runs: [] });
  });

  it("opens schedule details from its name and returns to the schedule list", async () => {
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Schedules" }));
    fireEvent.click(await screen.findByRole("button", { name: "Morning review" }));
    expect(await screen.findByRole("heading", { name: "Morning review" })).toBeInTheDocument();
    expect(await screen.findByText("No runs yet.")).toBeInTheDocument();
    expect(screen.queryByRole("textbox", { name: "Message General" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back to schedules" }));
    expect(await screen.findByRole("button", { name: "Morning review" })).toBeInTheDocument();
  });

  it("updates the accessible switch state only after saving the schedule", async () => {
    let finish!: (value: Schedule) => void;
    vi.mocked(api.setScheduleEnabled).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Schedules" }));
    const toggle = await screen.findByRole("switch", { name: "Disable schedule Morning review", checked: true });
    fireEvent.click(toggle);

    expect(api.setScheduleEnabled).toHaveBeenCalledWith(schedule.id, false);
    expect(toggle).toBeChecked();
    finish({ ...schedule, enabled: false });
    const disabledToggle = await screen.findByRole("switch", { name: "Enable schedule Morning review", checked: false });

    vi.mocked(api.setScheduleEnabled).mockResolvedValueOnce(schedule);
    fireEvent.click(disabledToggle);
    expect(api.setScheduleEnabled).toHaveBeenLastCalledWith(schedule.id, true);
    expect(await screen.findByRole("switch", { name: "Disable schedule Morning review", checked: true })).toBeChecked();
  });

  it("keeps the schedule enabled and reports a failed toggle", async () => {
    vi.mocked(api.setScheduleEnabled).mockRejectedValueOnce(new Error("Schedule update failed"));
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Schedules" }));
    fireEvent.click(await screen.findByRole("switch", { name: "Disable schedule Morning review" }));

    expect(await screen.findByText("Schedule update failed")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Disable schedule Morning review" })).toBeChecked();
  });
});

describe("ChatPanel composer", () => {
  beforeEach(() => {
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [], conversationId: "conversation-1", lastSequence: 0, hasMore: false });
    vi.mocked(api.sendMessage).mockReset();
  });

  it("preserves editable drafts and attachments while scheduled work blocks chat submission and New chat", async () => {
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [], conversationId: "conversation-1", runtimeBusy: true, lastSequence: 0, hasMore: false });
    const onRunActiveChange = vi.fn();
    render(<ChatPanel agent={agent} setActiveRunId={vi.fn()} setConversationId={vi.fn()} onError={vi.fn()} onRunActiveChange={onRunActiveChange} />);
    expect(await screen.findByText("Scheduled work is running; chat is available after it finishes.")).toBeInTheDocument();
    const textarea = screen.getByRole("textbox", { name: "Message General" });
    fireEvent.change(textarea, { target: { value: "Check /workspace when ready" } });
    const file = new File(["Extra information"], "notes.txt", { type: "text/plain" });
    fireEvent.change(screen.getByLabelText("Attach files"), { target: { files: [file] } });
    expect(screen.getByText("notes.txt")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send message" })).toBeDisabled();
    fireEvent.keyDown(textarea, { key: "Enter", code: "Enter" });
    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(textarea).toHaveValue("Check /workspace when ready");
    expect(onRunActiveChange).toHaveBeenLastCalledWith(true);
    expect(screen.queryByRole("button", { name: "Stop run" })).not.toBeInTheDocument();
    vi.mocked(api.createConversation).mockClear();
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [], conversationId: "conversation-1", runtimeBusy: false, lastSequence: 0, hasMore: false });
    await waitFor(() => expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled(), { timeout: 2000 });
    expect(textarea).toHaveValue("Check /workspace when ready");
    expect(screen.getByText("notes.txt")).toBeInTheDocument();
    expect(api.createConversation).not.toHaveBeenCalled();
    expect(api.sendMessage).not.toHaveBeenCalled();
  });

  it("opens the picker and sends selected files without requiring text", async () => {
    vi.mocked(api.sendMessage).mockImplementation(async () => {
      vi.mocked(api.getAgentTimeline).mockResolvedValue({
        events: [{ runId: "run-file", sequence: 1, type: "message.user", payload: { id: "user-run-file", text: "Attached file: notes.txt" }, createdAt: "2026-09-14T00:00:00Z" }],
        conversationId: "conversation-1", lastSequence: 1, hasMore: false,
      });
      return { runId: "run-file", conversationId: "conversation-1", threadId: "thread-1", turnId: "turn-1" };
    });
    panel();
    const picker = screen.getByLabelText("Attach files");
    const click = vi.spyOn(picker, "click");
    fireEvent.click(screen.getByRole("button", { name: "Add attachments" }));
    expect(click).toHaveBeenCalledOnce();
    fireEvent.change(picker, { target: { files: [new File(["hello"], "notes.txt", { type: "text/plain" })] } });
    expect(screen.getByRole("button", { name: "Remove notes.txt" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalledWith("agent-1", "", "conversation-1", [{ name: "notes.txt", data: "aGVsbG8=" }]));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Remove notes.txt" })).not.toBeInTheDocument());
    expect(await screen.findByText("Attached file: notes.txt")).toBeInTheDocument();
  });

  it("accepts additional information while a delegated task is running", async () => {
    vi.mocked(api.getAgentTimeline).mockResolvedValue({
      events: [{ runId: "delegated-run", sequence: 1, type: "message.user", payload: { id: "user-delegated", text: "Inspect repository" }, createdAt: "2026-09-14T00:00:00Z" }],
      conversationId: "conversation-1", activeRunId: "delegated-run", lastSequence: 1, hasMore: false,
    });
    vi.mocked(api.sendMessage).mockResolvedValue({ runId: "delegated-run", conversationId: "conversation-1", threadId: "thread-1", turnId: "turn-1" });
    panel();
    expect(await screen.findByRole("button", { name: "Stop run" })).toBeInTheDocument();
    fireEvent.change(screen.getByRole("textbox", { name: "Message General" }), { target: { value: "Also inspect /shared/project" } });
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalledWith("agent-1", "Also inspect /shared/project", "conversation-1"));
    expect(screen.getAllByText("Inspect repository")).toHaveLength(1);
  });

  it("previews images and releases the preview when removed", () => {
    const create = vi.fn(() => "blob:preview");
    const revoke = vi.fn();
    vi.stubGlobal("URL", class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    });
    try {
      panel();
      fireEvent.change(screen.getByLabelText("Attach files"), { target: { files: [new File(["image"], "photo.png", { type: "image/png" }), new File(["notes"], "notes.txt")] } });
      expect(screen.getByRole("img", { name: "photo.png" })).toHaveAttribute("src", "blob:preview");
      fireEvent.click(screen.getByRole("button", { name: "Remove photo.png" }));
      expect(revoke).toHaveBeenCalledWith("blob:preview");
      expect(screen.queryByRole("img")).not.toBeInTheDocument();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("preserves files on failure and allows removing and selecting the same file again", async () => {
    vi.mocked(api.sendMessage).mockRejectedValue(new Error("Offline"));
    panel();
    const picker = screen.getByLabelText("Attach files");
    const file = new File(["data"], "notes.txt");
    fireEvent.change(picker, { target: { files: [file] } });
    fireEvent.click(screen.getByRole("button", { name: "Send message" }));
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByRole("button", { name: "Remove notes.txt" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Remove notes.txt" }));
    expect(screen.getByRole("button", { name: "Send message" })).toBeDisabled();
    fireEvent.change(picker, { target: { files: [file] } });
    expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
  });

  it("rejects excessive file counts and sizes before sending", () => {
    const onError = vi.fn();
    render(<ChatPanel agent={agent} setActiveRunId={vi.fn()} setConversationId={vi.fn()} onError={onError} />);
    const picker = screen.getByLabelText("Attach files");
    fireEvent.change(picker, { target: { files: Array.from({ length: 9 }, () => new File(["x"], "notes.txt")) } });
    expect(onError).toHaveBeenCalledOnce();
    const large = new File(["x"], "large.txt");
    Object.defineProperty(large, "size", { value: 10 * 1024 * 1024 + 1 });
    fireEvent.change(picker, { target: { files: [large] } });
    expect(onError).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Send message" })).toBeDisabled();
    expect(api.sendMessage).not.toHaveBeenCalled();
  });

  it("does not send when Enter confirms an IME composition", async () => {
    panel();
    const textarea = await screen.findByRole("textbox", { name: "Message General" });
    fireEvent.change(textarea, { target: { value: "日本語" } });
    fireEvent.compositionStart(textarea);
    fireEvent.keyDown(textarea, { key: "Enter", code: "Enter", keyCode: 229, isComposing: true });
    fireEvent.compositionEnd(textarea);

    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(textarea).toHaveValue("日本語");
  });

  it("clears the submitted draft only after the turn is accepted", async () => {
    let accept!: (value: { runId: string; conversationId: string; threadId: string; turnId: string }) => void;
    vi.mocked(api.sendMessage).mockImplementation(() => new Promise((resolve) => { accept = resolve; }));
    panel();
    const textarea = await screen.findByRole("textbox", { name: "Message General" });
    fireEvent.change(textarea, { target: { value: "調査してください" } });
    fireEvent.keyDown(textarea, { key: "Enter", code: "Enter", keyCode: 13 });

    await waitFor(() => expect(api.sendMessage).toHaveBeenCalledWith("agent-1", "調査してください", "conversation-1"));
    expect(textarea).toHaveValue("調査してください");
    accept({ runId: "run-1", conversationId: "conversation-1", threadId: "thread-1", turnId: "turn-1" });
    await waitFor(() => expect(textarea).toHaveValue(""));
  });

  it("preserves text typed while a send request is pending", async () => {
    let accept!: (value: { runId: string; conversationId: string; threadId: string; turnId: string }) => void;
    vi.mocked(api.sendMessage).mockImplementation(() => new Promise((resolve) => { accept = resolve; }));
    panel();
    const textarea = await screen.findByRole("textbox", { name: "Message General" });
    fireEvent.change(textarea, { target: { value: "first" } });
    fireEvent.keyDown(textarea, { key: "Enter", code: "Enter", keyCode: 13 });
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalled());
    fireEvent.change(textarea, { target: { value: "next draft" } });
    accept({ runId: "run-1", conversationId: "conversation-1", threadId: "thread-1", turnId: "turn-1" });

    await waitFor(() => expect(textarea).toHaveValue("next draft"));
  });

  it("does not apply a completed send to another chat after unmount", async () => {
    let accept!: (value: { runId: string; conversationId: string; threadId: string; turnId: string }) => void;
    vi.mocked(api.sendMessage).mockImplementation(() => new Promise((resolve) => { accept = resolve; }));
    const setActiveRunId = vi.fn();
    const setConversationId = vi.fn();
    const rendered = render(<StrictMode><ChatPanel agent={agent} activeRunId={undefined} setActiveRunId={setActiveRunId} conversationId="conversation-1" setConversationId={setConversationId} onError={vi.fn()} /></StrictMode>);
    const textarea = await screen.findByRole("textbox", { name: "Message General" });
    fireEvent.change(textarea, { target: { value: "first" } });
    fireEvent.keyDown(textarea, { key: "Enter", code: "Enter", keyCode: 13 });
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalled());
    rendered.unmount();
    setActiveRunId.mockClear();
    setConversationId.mockClear();
    accept({ runId: "run-1", conversationId: "conversation-1", threadId: "thread-1", turnId: "turn-1" });
    await Promise.resolve();

    expect(setActiveRunId).not.toHaveBeenCalled();
    expect(setConversationId).not.toHaveBeenCalled();
  });
});


describe("Automatic permissions in chat", () => {
  it("replaces a native review from history with its live decision without manual approval controls", async () => {
    const nativeReview: AgentEvent = {
      runId: "run-1", sequence: 1, type: "item/autoApprovalReview/started",
      payload: { reviewId: "review-1", targetItemId: "tool-1", action: { type: "command", command: "npm test" }, review: { status: "inProgress" } },
      createdAt: "2026-09-11T00:00:00Z",
    };
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [nativeReview], activeRunId: "run-1", conversationId: "conversation-1", lastSequence: 1, hasMore: false });
    const props = { agent, activeRunId: "run-1", setActiveRunId: vi.fn(), setConversationId: vi.fn(), onError: vi.fn() };
    const rendered = render(<ChatPanel {...props} />);
    expect(await screen.findByText("Checking tool safety")).toBeInTheDocument();
    expect(screen.getByText("npm test")).toBeInTheDocument();
    vi.mocked(api.getAgentTimeline).mockResolvedValue({
      events: [{ ...nativeReview, sequence: 2, type: "item/autoApprovalReview/completed", payload: {
        ...nativeReview.payload, review: { status: "approved", rationale: "Runs the requested local tests." },
      } }],
      activeRunId: "run-1", conversationId: "conversation-1", lastSequence: 2, hasMore: false,
    });
    rendered.rerender(<ChatPanel {...props} />);
    expect(await screen.findByText("Permission automatically allowed")).toBeInTheDocument();
    expect(screen.getByText(/Runs the requested local tests/)).toBeInTheDocument();
    expect(screen.queryByText("Checking tool safety")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /accept|decline/i })).not.toBeInTheDocument();
  });

  const approval: AgentEvent = {
    runId: "run-1", sequence: 1, type: "approval.reviewing",
    payload: {
      approvalId: "approval-1", method: "mcpServer/elicitation/request",
      params: { message: "Open Wikipedia in the browser" }, model: "gpt-5.3-codex-spark",
    },
    createdAt: "2026-09-08T00:00:00Z",
  };

  it("does not offer manual approval buttons for legacy history", async () => {
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [{ ...approval, type: "approval.requested" }], conversationId: "conversation-1", lastSequence: 1, hasMore: false });
    panel();
    expect(await screen.findByText("Permission requested")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /accept|decline/i })).not.toBeInTheDocument();
    expect(screen.queryByText("Approval needed")).not.toBeInTheDocument();
  });

  it.each([
    { type: "approval.autoAccepted", title: "Permission automatically allowed", reason: "Browser navigation is safe." },
    { type: "approval.autoDeclined", title: "Permission automatically denied", reason: "The request would expose credentials." },
  ])("updates a reviewing history entry with the live $type result", async ({ type, title, reason }) => {
    vi.mocked(api.getAgentTimeline).mockResolvedValue({ events: [approval], activeRunId: "run-1", conversationId: "conversation-1", lastSequence: 1, hasMore: false });
    const props = { agent, activeRunId: "run-1", setActiveRunId: vi.fn(), setConversationId: vi.fn(), onError: vi.fn() };
    const rendered = render(<ChatPanel {...props} />);
    expect(await screen.findByText("Checking tool safety")).toBeInTheDocument();
    expect(screen.getByText("Open Wikipedia in the browser")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /accept|decline/i })).not.toBeInTheDocument();
    vi.mocked(api.getAgentTimeline).mockResolvedValue({
      events: [{ ...approval, sequence: 2, type, payload: { ...approval.payload, reason } }],
      activeRunId: "run-1", conversationId: "conversation-1", lastSequence: 2, hasMore: false,
    });
    rendered.rerender(<ChatPanel {...props} />);
    expect(await screen.findByText(title)).toBeInTheDocument();
    expect(screen.getByText(reason)).toBeInTheDocument();
    expect(screen.queryByText("Checking tool safety")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /accept|decline/i })).not.toBeInTheDocument();
  });
});
