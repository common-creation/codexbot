import { act, cleanup, createEvent, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "../api";
import type { Agent, SidebarLayout } from "../types";
import { AgentSidebarList } from "./AgentSidebarList";

vi.mock("../api", async (importOriginal) => {
  const original = await importOriginal<typeof import("../api")>();
  return { ...original, api: { ...original.api, getSidebar: vi.fn(), saveSidebar: vi.fn() } };
});

const agents: Agent[] = ["Alpha", "Bravo", "Charlie", "Delta"].map((name) => ({
  id: name.toLowerCase(), name, rolePrompt: "Help", roleVersion: 1, status: "running",
  providerAuthState: "connected", createdAt: "2026-09-13T00:00:00Z", updatedAt: "2026-09-13T00:00:00Z",
}));

function initialLayout(): SidebarLayout {
  return { revision: 3, sections: [
    { id: "work", name: "Work", agentIds: ["bravo", "alpha"] },
    { id: "personal", name: "Personal", agentIds: [] },
  ], unsectionedAgentIds: ["delta", "charlie"] };
}

function sidebar(query = "") {
  return <AgentSidebarList agents={agents} query={query} loading={false} onCreate={vi.fn()}
    renderAgent={(agent) => <a href={`#${agent.id}`}>{agent.name}</a>} />;
}

async function ready(query = "") {
  const view = render(sidebar(query));
  await waitFor(() => expect(screen.getByRole("button", { name: "Add section" })).toBeEnabled());
  return view;
}

function names(section: string) {
  return within(screen.getByRole("region", { name: section })).queryAllByRole("link").map((link) => link.textContent);
}

function drag(name: string, target: Element, lowerHalf = false) {
  const handle = screen.getByRole("button", { name: `Move ${name}` });
  const dataTransfer = { setData: vi.fn(), effectAllowed: "", dropEffect: "" };
  fireEvent.dragStart(handle, { dataTransfer });
  for (const type of ["dragOver", "drop"] as const) {
    const event = createEvent[type](target, { dataTransfer });
    Object.defineProperty(event, "clientY", { value: lowerHalf ? 1 : 0 });
    fireEvent(target, event);
  }
  fireEvent.dragEnd(handle, { dataTransfer });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((accept) => { resolve = accept; });
  return { promise, resolve };
}

beforeEach(() => {
  vi.mocked(api.getSidebar).mockReset().mockResolvedValue(initialLayout());
  vi.mocked(api.saveSidebar).mockReset().mockImplementation(async (layout) => ({ ...layout, revision: layout.revision + 1 }));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("Agent sidebar organization", () => {
  it("renders persisted group and agent order, then reloads that order on remount", async () => {
    const view = await ready();
    expect(screen.getAllByRole("region").map((region) => region.getAttribute("aria-label"))).toEqual(["Work", "Personal", "Unsectioned"]);
    expect(names("Work")).toEqual(["Bravo", "Alpha"]);
    expect(names("Unsectioned")).toEqual(["Delta", "Charlie"]);
    view.unmount();
    await ready();
    expect(names("Work")).toEqual(["Bravo", "Alpha"]);
    expect(api.getSidebar).toHaveBeenCalledTimes(2);
  });

  it("drops before and after rows within a section and persists each order", async () => {
    await ready();
    drag("Alpha", screen.getByRole("link", { name: "Bravo" }).parentElement!);
    await waitFor(() => expect(api.saveSidebar).toHaveBeenCalledWith({ ...initialLayout(), sections: [
      { id: "work", name: "Work", agentIds: ["alpha", "bravo"] }, initialLayout().sections[1],
    ] }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Move Alpha" })).toBeEnabled());
    expect(names("Work")).toEqual(["Alpha", "Bravo"]);
    drag("Alpha", screen.getByRole("link", { name: "Bravo" }).parentElement!, true);
    await waitFor(() => expect(api.saveSidebar).toHaveBeenLastCalledWith({ ...initialLayout(), revision: 4 }));
    expect(names("Work")).toEqual(["Bravo", "Alpha"]);
  });

  it("moves into an empty section and then a collapsed section, expanding the destination", async () => {
    await ready();
    drag("Delta", screen.getByRole("region", { name: "Personal" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Move Delta" })).toBeEnabled());
    expect(names("Personal")).toEqual(["Delta"]);
    expect(names("Unsectioned")).toEqual(["Charlie"]);
    fireEvent.click(screen.getByRole("button", { name: /^Work\s*2$/ }));
    expect(screen.queryByRole("link", { name: "Alpha" })).not.toBeInTheDocument();
    drag("Delta", screen.getByRole("region", { name: "Work" }));
    await waitFor(() => expect(api.saveSidebar).toHaveBeenCalledTimes(2));
    expect(names("Work")).toEqual(["Bravo", "Alpha", "Delta"]);
    expect(names("Personal")).toEqual([]);
    expect(screen.getByRole("button", { name: /^Work\s*3$/ })).toHaveAttribute("aria-expanded", "true");
  });

  it("preserves filtered-out agents when moving a search result", async () => {
    const view = await ready("alpha");
    expect(screen.queryByRole("link", { name: "Bravo" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Move Alpha" }));
    fireEvent.change(screen.getByLabelText("Section"), { target: { value: "personal" } });
    fireEvent.click(screen.getByRole("button", { name: "Move agent" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(api.saveSidebar).toHaveBeenCalledWith({ revision: 3, sections: [
      { id: "work", name: "Work", agentIds: ["bravo"] },
      { id: "personal", name: "Personal", agentIds: ["alpha"] },
    ], unsectionedAgentIds: ["delta", "charlie"] });
    view.rerender(sidebar());
    expect(names("Work")).toEqual(["Bravo"]);
    expect(names("Personal")).toEqual(["Alpha"]);
    expect(names("Unsectioned")).toEqual(["Delta", "Charlie"]);
  });

  it("creates and renames sections and preserves their agents when deleting", async () => {
    await ready();
    fireEvent.click(screen.getByRole("button", { name: "Add section" }));
    fireEvent.change(screen.getByLabelText("Section name"), { target: { value: "  Research  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save section" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("region", { name: "Research" })).toBeInTheDocument();
    expect(vi.mocked(api.saveSidebar).mock.calls[0][0].sections.at(-1)).toEqual({ id: expect.any(String), name: "Research", agentIds: [] });
    fireEvent.click(screen.getByRole("button", { name: "Edit section Work" }));
    fireEvent.change(screen.getByLabelText("Section name"), { target: { value: "Projects" } });
    fireEvent.click(screen.getByRole("button", { name: "Save section" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(names("Projects")).toEqual(["Bravo", "Alpha"]);
    fireEvent.click(screen.getByRole("button", { name: "Edit section Projects" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete section" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByRole("region", { name: "Projects" })).not.toBeInTheDocument();
    expect(names("Unsectioned")).toEqual(["Delta", "Charlie", "Bravo", "Alpha"]);
  });

  it("provides a focusable move dialog with a section and insertion position", async () => {
    await ready();
    const handle = screen.getByRole("button", { name: "Move Alpha" });
    handle.focus();
    expect(handle).toHaveFocus();
    // Keyboard activation of a native button dispatches a click with detail zero.
    fireEvent.click(handle, { detail: 0 });
    expect(screen.getByRole("dialog", { name: "Move Alpha" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Section"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Position"), { target: { value: "charlie" } });
    fireEvent.submit(screen.getByRole("button", { name: "Move agent" }).closest("form")!);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(names("Unsectioned")).toEqual(["Delta", "Alpha", "Charlie"]);
    expect(names("Work")).toEqual(["Bravo"]);
  });

  it("restores the prior order and reports a rejected save", async () => {
    vi.mocked(api.saveSidebar).mockRejectedValueOnce(new ApiError("Unavailable", 503));
    await ready();
    drag("Alpha", screen.getByRole("link", { name: "Bravo" }).parentElement!);
    expect(await screen.findByRole("alert")).toHaveTextContent("Your previous order has been restored");
    await waitFor(() => expect(screen.getByRole("button", { name: "Move Alpha" })).toBeEnabled());
    expect(names("Work")).toEqual(["Bravo", "Alpha"]);
    expect(api.getSidebar).toHaveBeenCalledTimes(2);
  });

  it("reloads a conflicting edit and uses the latest revision on the next save", async () => {
    const latest: SidebarLayout = { revision: 9, sections: initialLayout().sections, unsectionedAgentIds: ["charlie", "delta"] };
    vi.mocked(api.getSidebar).mockResolvedValueOnce(initialLayout()).mockResolvedValue(latest);
    vi.mocked(api.saveSidebar).mockRejectedValueOnce(new ApiError("Conflict", 409));
    await ready();
    drag("Alpha", screen.getByRole("link", { name: "Bravo" }).parentElement!);
    expect(await screen.findByRole("alert")).toHaveTextContent("Sidebar changed elsewhere");
    await waitFor(() => expect(screen.getByRole("button", { name: "Move Alpha" })).toBeEnabled());
    expect(names("Unsectioned")).toEqual(["Charlie", "Delta"]);
    drag("Alpha", screen.getByRole("region", { name: "Personal" }));
    await waitFor(() => expect(api.saveSidebar).toHaveBeenLastCalledWith(expect.objectContaining({ revision: 9 })));
  });

  it("ignores an older polling response that arrives after a successful save", async () => {
    let poll!: () => void;
    const originalInterval = window.setInterval.bind(window);
    vi.spyOn(window, "setInterval").mockImplementation((callback, delay, ...args) => {
      if (delay === 5_000) { poll = callback as () => void; return 123; }
      return originalInterval(callback, delay, ...args);
    });
    const stale = deferred<SidebarLayout>();
    vi.mocked(api.getSidebar).mockResolvedValueOnce(initialLayout()).mockReturnValueOnce(stale.promise);
    await ready();
    act(() => poll());
    expect(api.getSidebar).toHaveBeenCalledTimes(2);
    drag("Alpha", screen.getByRole("link", { name: "Bravo" }).parentElement!);
    await waitFor(() => expect(screen.getByRole("button", { name: "Move Alpha" })).toBeEnabled());
    await act(async () => { stale.resolve(initialLayout()); await stale.promise; });
    expect(names("Work")).toEqual(["Alpha", "Bravo"]);
  });
});
