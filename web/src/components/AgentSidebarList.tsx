import { ChevronDown, ChevronRight, FolderPlus, GripVertical, Settings2, ArrowUp, ArrowDown } from "lucide-react";
import { useEffect, useRef, useState, type DragEvent, type ReactNode } from "react";
import { api, ApiError } from "../api";
import { moveSidebarAgent, normalizeSidebar } from "../sidebarLayout";
import type { Agent, SidebarLayout } from "../types";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { NativeSelect, NativeSelectOption } from "./ui/native-select";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "./ui/dialog";

export function AgentSidebarList({ agents, query, loading, onCreate, renderAgent }: {
  agents: Agent[];
  query: string;
  loading: boolean;
  onCreate: () => void;
  renderAgent: (agent: Agent) => ReactNode;
}) {
  const [saved, setSaved] = useState<SidebarLayout>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [editor, setEditor] = useState<{ id?: string; name: string }>();
  const [moving, setMoving] = useState<{ agentId: string; sectionId: string; beforeId: string }>();
  const [dragged, setDragged] = useState<string>();
  const dragId = useRef<string | undefined>(undefined);
  const [dropTarget, setDropTarget] = useState("");
  const pending = useRef(false);
  const generation = useRef(0);
  const mounted = useRef(false);

  useEffect(() => {
    mounted.current = true;
    let active = true;
    const refresh = async () => {
      if (pending.current) return;
      const started = generation.current;
      try {
        const next = await api.getSidebar();
        if (active && started === generation.current && !pending.current) {
          setSaved((current) => !current || next.revision >= current.revision ? next : current);
          setError((current) => current.startsWith("Could not load sidebar") || current.startsWith("Could not reload sidebar") ? "" : current);
        }
      } catch {
        if (active && started === generation.current && !pending.current) setError("Could not load sidebar organization. Retrying automatically.");
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5_000);
    return () => { active = false; mounted.current = false; window.clearInterval(timer); };
  }, []);

  const layout = normalizeSidebar(saved ?? { revision: 0, sections: [], unsectionedAgentIds: [] }, agents);
  const disabled = loading || !saved || saving;
  const groups = [...layout.sections, { id: "", name: "Unsectioned", agentIds: layout.unsectionedAgentIds }];
  const byId = new Map(agents.map((agent) => [agent.id, agent]));
  const matches = (id: string) => byId.get(id)?.name.toLowerCase().includes(query.trim().toLowerCase());

  const save = async (next: SidebarLayout): Promise<boolean> => {
    if (pending.current || disabled) return false;
    if (JSON.stringify(next) === JSON.stringify(layout)) return true;
    pending.current = true;
    generation.current += 1;
    setSaving(true);
    setError("");
    const previous = saved;
    setSaved(next);
    try {
      const result = await api.saveSidebar(next);
      if (mounted.current) setSaved(result);
      return true;
    } catch (cause) {
      if (mounted.current) {
        setSaved(previous);
        setError(cause instanceof ApiError && cause.status === 409
          ? "Sidebar changed elsewhere. Reloaded the latest order; please try again."
          : "Could not save sidebar organization. Your previous order has been restored.");
      }
      // Refresh after conflicts and uncertain network results before allowing another edit.
      try {
        const latest = await api.getSidebar();
        if (mounted.current) setSaved(latest);
      } catch {
        if (mounted.current) {
          setSaved(undefined);
          setError("Could not reload sidebar organization. Editing is paused while reconnecting.");
        }
      }
      return false;
    } finally {
      pending.current = false;
      if (mounted.current) setSaving(false);
    }
  };

  const finishDrag = () => { dragId.current = undefined; setDragged(undefined); setDropTarget(""); };
  const allowDrop = (event: DragEvent, target: string) => {
    if (!dragId.current || disabled) return;
    event.preventDefault();
    event.stopPropagation();
    event.dataTransfer.dropEffect = "move";
    setDropTarget(target);
  };
  const drop = (event: DragEvent, sectionId: string, beforeId?: string) => {
    if (!dragId.current || disabled) return;
    event.preventDefault();
    event.stopPropagation();
    void save(moveSidebarAgent(layout, dragId.current, sectionId, beforeId));
    setCollapsed((current) => { const next = new Set(current); next.delete(sectionId); return next; });
    finishDrag();
  };

  return <>
    <div className="agent-list-toolbar">
      <span>Agents</span>
      <Button variant="ghost" size="icon-xs" disabled={disabled} aria-label="Add section" title="Add section" onClick={() => setEditor({ name: "" })}><FolderPlus /></Button>
    </div>
    {error && <p className="sidebar-error" role="alert">{error}</p>}
    <span className="sr-only" role="status">{saving ? "Saving sidebar organization…" : ""}</span>
    <div className="agent-list" aria-label="Agents" aria-busy={loading || saving} onDragLeave={(event) => {
      if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDropTarget("");
    }}>
      {loading && [0, 1, 2].map((item) => <div className="agent-skeleton" key={item} />)}
      {!loading && agents.length === 0 && <Button variant="ghost" className="sidebar-empty" onClick={onCreate}>No agents yet. Create one.</Button>}
      {!loading && agents.length > 0 && !agents.some((agent) => matches(agent.id)) && <p className="sidebar-empty">No matching agents.</p>}
      {!loading && groups.map((group) => {
        const visible = group.agentIds.filter(matches);
        if (query.trim() && visible.length === 0) return null;
        const closed = collapsed.has(group.id) && !query.trim();
        const showHeader = layout.sections.length > 0;
        return <section key={group.id} aria-label={group.name} className={`agent-section ${dropTarget === `group:${group.id}` ? "drop-group" : ""}`}
          onDragOver={(event) => allowDrop(event, `group:${group.id}`)} onDrop={(event) => drop(event, group.id)}>
          {showHeader && <div className="agent-section-heading">
            <Button variant="ghost" className="section-toggle" aria-expanded={!closed} onClick={() => setCollapsed((current) => {
              const next = new Set(current); if (next.has(group.id)) next.delete(group.id); else next.add(group.id); return next;
            })}>
              {closed ? <ChevronRight /> : <ChevronDown />}<span title={group.name}>{group.name}</span><small>{visible.length}</small>
            </Button>
            {group.id && <Button variant="ghost" size="icon-xs" disabled={disabled} aria-label={`Edit section ${group.name}`} title="Edit section" onClick={() => setEditor({ id: group.id, name: group.name })}><Settings2 /></Button>}
          </div>}
          {!closed && visible.map((id) => {
            const agent = byId.get(id)!;
            const position = group.agentIds.indexOf(id);
            const afterId = group.agentIds[position + 1];
            const rowTarget = (event: DragEvent) => event.clientY > event.currentTarget.getBoundingClientRect().top + event.currentTarget.getBoundingClientRect().height / 2 ? afterId : id;
            return <div key={id} className={`sortable-agent ${dragged === id ? "dragging" : ""} ${dropTarget === `before:${id}` ? "drop-before" : ""} ${dropTarget === `after:${id}` ? "drop-after" : ""}`}
              onDragOver={(event) => allowDrop(event, rowTarget(event) === id ? `before:${id}` : `after:${id}`)}
              onDrop={(event) => drop(event, group.id, rowTarget(event))}>
              <Button variant="ghost" size="icon-xs" className="agent-drag-handle" aria-label={`Move ${agent.name}`} title="Drag to reorder, or click to move" disabled={disabled} draggable={!disabled}
                onDragStart={(event) => { dragId.current = id; setDragged(id); event.dataTransfer.effectAllowed = "move"; event.dataTransfer.setData("text/plain", id); }}
                onDragEnd={finishDrag}
                onClick={() => setMoving({ agentId: id, sectionId: group.id, beforeId: afterId ?? "" })}><GripVertical /></Button>
              {renderAgent(agent)}
            </div>;
          })}
          {!closed && visible.length === 0 && showHeader && <div className="section-drop-placeholder">Drag agents here</div>}
        </section>;
      })}
    </div>

    <Dialog open={!!editor} onOpenChange={(open) => { if (!open && !saving) setEditor(undefined); }}>
      {editor && <DialogContent>
        <DialogTitle>{editor.id ? "Edit section" : "Add section"}</DialogTitle>
        <DialogDescription>Group agents by project or purpose. Drag agents into a section to organize them.</DialogDescription>
        <form className="section-form" onSubmit={(event) => {
          event.preventDefault();
          const name = editor.name.trim();
          if (!name) return;
          const sections = editor.id ? layout.sections.map((section) => section.id === editor.id ? { ...section, name } : section)
            : [...layout.sections, { id: crypto.randomUUID(), name, agentIds: [] }];
          void save({ ...layout, sections }).then((ok) => { if (ok) setEditor(undefined); });
        }}>
          <Label htmlFor="section-name">Section name</Label>
          <Input id="section-name" value={editor.name} maxLength={100} required onChange={(event) => setEditor({ ...editor, name: event.target.value })} />
          {editor.id && <div className="section-editor-actions">
            <Button type="button" variant="outline" disabled={disabled || layout.sections[0]?.id === editor.id} onClick={() => {
              const sections = [...layout.sections]; const index = sections.findIndex((section) => section.id === editor.id);
              if (index > 0) { [sections[index - 1], sections[index]] = [sections[index], sections[index - 1]]; void save({ ...layout, sections }); }
            }}><ArrowUp />Move up</Button>
            <Button type="button" variant="outline" disabled={disabled || layout.sections.at(-1)?.id === editor.id} onClick={() => {
              const sections = [...layout.sections]; const index = sections.findIndex((section) => section.id === editor.id);
              if (index >= 0 && index < sections.length - 1) { [sections[index], sections[index + 1]] = [sections[index + 1], sections[index]]; void save({ ...layout, sections }); }
            }}><ArrowDown />Move down</Button>
          </div>}
          {editor.id && <p className="text-sm text-muted-foreground">Deleting a section moves its agents to Unsectioned.</p>}
          {error && <p className="text-sm text-destructive" role="alert">{error}</p>}
          <div className="section-editor-actions">
            {editor.id && <Button type="button" variant="destructive" disabled={disabled} onClick={() => {
              const section = layout.sections.find((item) => item.id === editor.id);
              void save({ ...layout, sections: layout.sections.filter((item) => item.id !== editor.id), unsectionedAgentIds: [...layout.unsectionedAgentIds, ...(section?.agentIds ?? [])] }).then((ok) => { if (ok) setEditor(undefined); });
            }}>Delete section</Button>}
            <Button type="button" variant="outline" disabled={saving} onClick={() => setEditor(undefined)}>Cancel</Button>
            <Button type="submit" disabled={disabled || !editor.name.trim()}>{saving ? "Saving…" : "Save section"}</Button>
          </div>
        </form>
      </DialogContent>}
    </Dialog>

    <Dialog open={!!moving} onOpenChange={(open) => { if (!open && !saving) setMoving(undefined); }}>
      {moving && <DialogContent>
        <DialogTitle>Move {byId.get(moving.agentId)?.name ?? "agent"}</DialogTitle>
        <DialogDescription>Choose a section and position. You can also drag the handle beside an agent.</DialogDescription>
        <form className="section-form" onSubmit={(event) => {
          event.preventDefault();
          if (!byId.has(moving.agentId)) { setMoving(undefined); return; }
          void save(moveSidebarAgent(layout, moving.agentId, moving.sectionId, moving.beforeId || undefined)).then((ok) => { if (ok) setMoving(undefined); });
        }}>
          <Label htmlFor="move-section">Section</Label>
          <NativeSelect id="move-section" value={moving.sectionId} onChange={(event) => setMoving({ ...moving, sectionId: event.target.value, beforeId: "" })}>
            {groups.map((group) => <NativeSelectOption key={group.id} value={group.id}>{group.name}</NativeSelectOption>)}
          </NativeSelect>
          <Label htmlFor="move-position">Position</Label>
          <NativeSelect id="move-position" value={moving.beforeId} onChange={(event) => setMoving({ ...moving, beforeId: event.target.value })}>
            {(groups.find((group) => group.id === moving.sectionId)?.agentIds ?? []).filter((id) => id !== moving.agentId).map((id) => <NativeSelectOption key={id} value={id}>Before {byId.get(id)?.name}</NativeSelectOption>)}
            <NativeSelectOption value="">At the end</NativeSelectOption>
          </NativeSelect>
          {error && <p className="text-sm text-destructive" role="alert">{error}</p>}
          <div className="section-editor-actions"><Button type="button" variant="outline" disabled={saving} onClick={() => setMoving(undefined)}>Cancel</Button><Button type="submit" disabled={disabled}>Move agent</Button></div>
        </form>
      </DialogContent>}
    </Dialog>
  </>;
}
