import type { Agent, SidebarLayout } from "./types";

export function normalizeSidebar(layout: SidebarLayout, agents: Agent[]): SidebarLayout {
  const remaining = new Set(agents.map((agent) => agent.id));
  const keep = (ids: string[]) => ids.filter((id) => remaining.delete(id));
  const sections = layout.sections.map((section) => ({ ...section, agentIds: keep(section.agentIds) }));
  const unsectionedAgentIds = keep(layout.unsectionedAgentIds);
  return { ...layout, sections, unsectionedAgentIds: [...unsectionedAgentIds, ...remaining] };
}

// An empty section ID denotes the built-in Unsectioned group.
export function moveSidebarAgent(layout: SidebarLayout, agentId: string, sectionId: string, beforeId?: string): SidebarLayout {
  if (beforeId === agentId || (sectionId && !layout.sections.some((section) => section.id === sectionId))) return layout;
  const remove = (ids: string[]) => ids.filter((id) => id !== agentId);
  const next = {
    ...layout,
    sections: layout.sections.map((section) => ({ ...section, agentIds: remove(section.agentIds) })),
    unsectionedAgentIds: remove(layout.unsectionedAgentIds),
  };
  const target = sectionId ? next.sections.find((section) => section.id === sectionId)!.agentIds : next.unsectionedAgentIds;
  const index = beforeId ? target.indexOf(beforeId) : -1;
  target.splice(index < 0 ? target.length : index, 0, agentId);
  return next;
}
