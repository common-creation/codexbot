import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { api } from "./api";
import type { Agent } from "./types";

vi.mock("./api", () => ({
  ApiError: class ApiError extends Error { constructor(message: string, readonly status: number) { super(message); } },
  resolveApiUrl: (path: string) => `/test-api${path}`,
  api: {
    getSetupStatus: vi.fn(),
    getSession: vi.fn(),
    listAgents: vi.fn(),
    getSidebar: vi.fn(),
    saveSidebar: vi.fn(),
    getModels: vi.fn(),
    createAgent: vi.fn(),
    updateAgent: vi.fn(),
    getProviderAuth: vi.fn(),
    getAgentTimeline: vi.fn(),
    desktopUrl: vi.fn(),
  },
}));

const iconData = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=";
const previewUrl = `data:image/png;base64,${iconData}`;
const agent: Agent = {
  id: "agent-1", name: "General", rolePrompt: "Help the user", roleVersion: 1,
  iconUrl: "/api/agents/agent-1/icon?v=old", status: "running", providerAuthState: "connected",
  createdAt: "2026-09-03T00:00:00Z", updatedAt: "2026-09-03T00:00:00Z",
};
const settings = { name: agent.name, rolePrompt: agent.rolePrompt, model: "", effort: "", permission: "auto" };
const avatarLocations = [".sidebar", ".workspace-header", ".message-row.assistant"];

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.getSetupStatus).mockResolvedValue({ required: false });
  vi.mocked(api.getSession).mockResolvedValue({ authenticated: true, username: "mohemohe", csrfToken: "csrf" });
  vi.mocked(api.listAgents).mockResolvedValue([agent]);
  vi.mocked(api.getSidebar).mockResolvedValue({ revision: 0, sections: [], unsectionedAgentIds: [] });
  vi.mocked(api.getModels).mockResolvedValue([]);
  vi.mocked(api.getProviderAuth).mockResolvedValue({ state: "connected" });
  vi.mocked(api.getAgentTimeline).mockResolvedValue({
    events: [{
      runId: "run-1", sequence: 1, type: "message.assistant.completed",
      payload: { itemId: "answer", text: "Earlier assistant reply" }, createdAt: agent.createdAt,
    }],
    conversationId: "conversation-1", lastSequence: 1, hasMore: false,
  });
  vi.mocked(api.createAgent).mockResolvedValue(agent);
  vi.mocked(api.updateAgent).mockResolvedValue(agent);
});
afterEach(cleanup);

async function openSettings() {
  const view = render(<App />);
  await screen.findByText("Earlier assistant reply");
  fireEvent.click(screen.getByRole("button", { name: "Agent settings" }));
  return view;
}

async function selectIcon() {
  const bytes = Uint8Array.from(atob(iconData), (character) => character.charCodeAt(0));
  fireEvent.change(screen.getByLabelText("Agent icon"), {
    target: { files: [new File([bytes], "agent.png", { type: "image/png" })] },
  });
  await waitFor(() => expect(screen.getByRole("dialog").querySelector(`img[src="${previewUrl}"]`)).toBeInTheDocument());
}

function expectWorkspaceIcon(container: HTMLElement, iconUrl: string | undefined) {
  for (const location of avatarLocations) {
    const avatar = container.querySelector(`${location} .agent-avatar`);
    expect(avatar).toBeInTheDocument();
    if (iconUrl) expect(avatar!.querySelector("img")).toHaveAttribute("src", `/test-api${iconUrl}`);
    else {
      expect(avatar!.querySelector("img")).not.toBeInTheDocument();
      expect(avatar).toHaveTextContent("G");
    }
  }
}

describe("Agent icon settings", () => {
  it("creates an agent with the selected icon and its other settings in one request", async () => {
    vi.mocked(api.listAgents).mockResolvedValue([]);
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Create an agent" }));
    fireEvent.change(screen.getByLabelText("Agent name"), { target: { value: "Research" } });
    fireEvent.change(screen.getByLabelText("Role and instructions"), { target: { value: "Investigate" } });
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "gpt-6-astra" } });
    fireEvent.change(screen.getByLabelText("Effort"), { target: { value: "high" } });
    await selectIcon();

    expect(api.createAgent).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));

    await waitFor(() => expect(api.createAgent).toHaveBeenCalledWith({
      name: "Research", rolePrompt: "Investigate", model: "gpt-6-astra", effort: "high", permission: "auto", icon: { data: iconData },
    }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("updates sidebar, header, and existing timeline avatars after an icon is saved", async () => {
    const savedAgent = { ...agent, iconUrl: "/api/agents/agent-1/icon?v=new" };
    vi.mocked(api.updateAgent).mockResolvedValue(savedAgent);
    const { container } = await openSettings();
    expectWorkspaceIcon(container, agent.iconUrl);
    await selectIcon();
    expectWorkspaceIcon(container, agent.iconUrl);

    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(api.updateAgent).toHaveBeenCalledWith(agent.id, { ...settings, icon: { data: iconData } }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expectWorkspaceIcon(container, savedAgent.iconUrl);
    expect(screen.getByText("Earlier assistant reply")).toBeInTheDocument();
  });

  it("removes the saved icon explicitly and restores fallback avatars", async () => {
    vi.mocked(api.updateAgent).mockResolvedValue({ ...agent, iconUrl: undefined });
    const { container } = await openSettings();
    fireEvent.click(screen.getByRole("button", { name: "Remove icon" }));
    expect(api.updateAgent).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(api.updateAgent).toHaveBeenCalledWith(agent.id, { ...settings, icon: null }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expectWorkspaceIcon(container, undefined);
  });

  it("omits the icon when saving unrelated settings", async () => {
    await openSettings();
    fireEvent.change(screen.getByLabelText("Agent name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(api.updateAgent).toHaveBeenCalledWith(agent.id, { ...settings, name: "Renamed" }));
    expect(vi.mocked(api.updateAgent).mock.calls[0][1]).not.toHaveProperty("icon");
  });

  it("discards an unsaved icon when settings are canceled", async () => {
    const { container } = await openSettings();
    await selectIcon();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api.updateAgent).not.toHaveBeenCalled();
    expectWorkspaceIcon(container, agent.iconUrl);
    fireEvent.click(screen.getByRole("button", { name: "Agent settings" }));
    expect(screen.getByRole("dialog").querySelector(`img[src="${previewUrl}"]`)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(api.updateAgent).toHaveBeenCalledWith(agent.id, settings));
  });

  it("keeps the draft icon available after a failed save and retries the same upload", async () => {
    const savedAgent = { ...agent, iconUrl: "/api/agents/agent-1/icon?v=retry" };
    vi.mocked(api.updateAgent).mockRejectedValueOnce(new Error("Could not store icon")).mockResolvedValueOnce(savedAgent);
    const { container } = await openSettings();
    await selectIcon();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(await screen.findByText("Could not store icon")).toBeInTheDocument();
    expect(screen.getByRole("dialog").querySelector(`img[src="${previewUrl}"]`)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
    expectWorkspaceIcon(container, agent.iconUrl);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api.updateAgent).toHaveBeenCalledTimes(2);
    expect(api.updateAgent).toHaveBeenNthCalledWith(1, agent.id, { ...settings, icon: { data: iconData } });
    expect(api.updateAgent).toHaveBeenNthCalledWith(2, agent.id, { ...settings, icon: { data: iconData } });
    expectWorkspaceIcon(container, savedAgent.iconUrl);
  });
});
