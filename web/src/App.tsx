import { Plus, Monitor, Search, CalendarDays, Link, Send, Square, X, Check, Sparkles, Play, Trash2, Copy, KeyRound, Menu, Settings2, ArrowUpRight } from "lucide-react";
import { Card } from "./components/ui/card";
import { Badge } from "./components/ui/badge";
import { Button } from "./components/ui/button";
import { Input } from "./components/ui/input";
import { Textarea } from "./components/ui/textarea";
import { Label } from "./components/ui/label";
import { NativeSelect, NativeSelectOption } from "./components/ui/native-select";
import { Dialog, DialogContent, DialogTitle, DialogDescription } from "./components/ui/dialog";
import { DesktopSheet } from "./components/DesktopSheet";
import { AgentSidebarList } from "./components/AgentSidebarList";
import { AgentAvatar as Avatar } from "./components/AgentAvatar";
import { AgentIconPicker } from "./components/AgentIconPicker";
import { ScheduleDetail } from "./components/ScheduleDetail";
import { MarkdownMessage } from "./components/MarkdownMessage";
import { Switch } from "./components/ui/switch";
import { FormEvent, useEffect, useRef, useState } from "react";
import { api, ApiError } from "./api";
import { useAgentTimeline } from "./hooks/useAgentTimeline";
import { type ChatEntry } from "./streamModel";
import { MAX_ATTACHMENTS, MAX_ATTACHMENT_BYTES, readAttachment, shouldSubmitComposerKey } from "./composer";
import type {
  Agent,
  IconUpload,
  Session,
  ModelOption,
  Permission,
  DeviceCode,
  Schedule,
} from "./types";

type View = "chat" | "schedules" | "connections";
type IconName =
  | "plus"
  | "monitor"
  | "search"
  | "calendar"
  | "link"
  | "send"
  | "stop"
  | "x"
  | "check"
  | "spark"
  | "play"
  | "trash"
  | "copy"
  | "key"
  | "menu"
  | "settings"
  | "arrow";

type AuthGateState = "checking" | "setup" | "login" | "authenticated";

export function App() {
  const [username, setUsername] = useState("");
  const [authState, setAuthState] = useState<AuthGateState>("checking");

  useEffect(() => {
    let active = true;
    api.getSetupStatus()
      .then(async ({ required }) => {
        if (required) return active && setAuthState("setup");
        try {
          const session = await api.getSession();
          if (active) {
            setUsername(session.username);
            setAuthState("authenticated");
          }
        } catch {
          if (active) setAuthState("login");
        }
      })
      .catch(() => active && setAuthState("login"));
    const unauthorized = () => setAuthState("login");
    window.addEventListener("codexbot:unauthorized", unauthorized);
    return () => {
      active = false;
      window.removeEventListener("codexbot:unauthorized", unauthorized);
    };
  }, []);

  if (authState === "checking") return <div className="auth-page"><span className="spinner large" /></div>;
  if (authState !== "authenticated") {
    return <AuthGate mode={authState} onAuthenticated={(session) => { setUsername(session.username); setAuthState("authenticated"); }} />;
  }
  return <WorkspaceApp username={username} onLoggedOut={() => setAuthState("login")} />;
}

function WorkspaceApp({ username, onLoggedOut }: { username: string; onLoggedOut: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [models, setModels] = useState<ModelOption[]>(fallbackModels);

  useEffect(() => {
    let active = true;
    api.getModels().then((items) => {
      if (active && items.length > 0) setModels(items);
    }).catch(() => { /* Keep the built-in catalog if the API is unavailable. */ });
    return () => { active = false; };
  }, []);
  const [selectedAgentId, setSelectedAgentId] = useState<string>();
  const [activeRunId, setActiveRunId] = useState<string>();
  const [view, setView] = useState<View>("chat");
  const [createOpen, setCreateOpen] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [desktopOpen, setDesktopOpen] = useState(false);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [banner, setBanner] = useState<string>();
  const [conversationId, setConversationId] = useState<string>();
  const [chatBusy, setChatBusy] = useState(true);
  const [resettingChat, setResettingChat] = useState(false);
  const [timelineRevision, setTimelineRevision] = useState(0);
  const selectionGeneration = useRef(0);

  const selectedAgent = agents.find((agent) => agent.id === selectedAgentId);
  const selectedAgentIdRef = useRef(selectedAgentId);
  const agentsRef = useRef(agents);
  selectedAgentIdRef.current = selectedAgentId;
  agentsRef.current = agents;

  useEffect(() => {
    let active = true;
    const refresh = () => api
      .listAgents()
      .then((items) => {
        if (!active) return;
        setAgents(items);
        setSelectedAgentId((current) => current && items.some((agent) => agent.id === current) ? current : items[0]?.id);
      })
      .catch((error) => setBanner(toMessage(error, "Could not reach the Codexbot API.")))
      .finally(() => setLoading(false));
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5_000);
    return () => { active = false; window.clearInterval(timer); };
  }, []);

  const selectAgent = (id: string) => {
    if (id === selectedAgentIdRef.current && view === "chat") {
      setDesktopOpen(false);
      setSidebarOpen(false);
      return;
    }
    selectionGeneration.current++;
    setChatBusy(true);
    setResettingChat(false);
    setSelectedAgentId(id);
    setActiveRunId(undefined);
    setConversationId(undefined);
    setView("chat");
    setDesktopOpen(false);
    setSidebarOpen(false);
  };

  const onCreated = (agent: Agent) => {
    selectionGeneration.current++;
    setChatBusy(true);
    setResettingChat(false);
    setAgents((current) => [...current, agent]);
    setSelectedAgentId(agent.id);
    setView("chat");
    setCreateOpen(false);
    setConversationId(undefined);
  };

  const onUpdated = (agent: Agent) => {
    setAgents((current) => current.map((item) => item.id === agent.id ? agent : item));
    setEditOpen(false);
  };

  const onDeleted = (agentId: string) => {
    const remaining = agentsRef.current.filter((agent) => agent.id !== agentId);
    setAgents(remaining);
    if (selectedAgentIdRef.current === agentId) {
      selectionGeneration.current++;
      setChatBusy(true);
      setResettingChat(false);
      setSelectedAgentId(remaining[0]?.id);
      setActiveRunId(undefined);
      setConversationId(undefined);
      setDesktopOpen(false);
      setView("chat");
    }
    setEditOpen(false);
  };

  const newChat = async () => {
    if (!selectedAgent || chatBusy || resettingChat) return;
    const agentId = selectedAgent.id;
    const generation = selectionGeneration.current;
    setResettingChat(true);
    try {
      const conversation = await api.createConversation(agentId);
      if (generation !== selectionGeneration.current || selectedAgentIdRef.current !== agentId) return;
      setConversationId(conversation.id);
      setActiveRunId(undefined);
      setTimelineRevision((current) => current + 1);
    } catch (error) {
      if (generation === selectionGeneration.current && selectedAgentIdRef.current === agentId) {
        setBanner(toMessage(error, "Could not start a new chat."));
      }
    } finally {
      if (generation === selectionGeneration.current && selectedAgentIdRef.current === agentId) setResettingChat(false);
    }
  };

  return (
    <div className="app-shell">
      <Sidebar
        username={username}
        agents={agents}
        selectedId={selectedAgentId}
        currentView={view}
        open={sidebarOpen}
        loading={loading}
        onSelect={selectAgent}
        onView={(next) => {
          setView(next);
          setDesktopOpen(false);
          setSidebarOpen(false);
        }}
        onCreate={() => setCreateOpen(true)}
        onClose={() => setSidebarOpen(false)}
        onLogout={() => api.logout().then(onLoggedOut).catch((error) => setBanner(toMessage(error, "Could not sign out.")))}
      />

      <main className="workspace">
        <header className="workspace-header">
          <Button variant="ghost" size="icon" className="icon-button mobile-menu" onClick={() => setSidebarOpen(true)} aria-label="Open agents">
            <Icon name="menu" />
          </Button>
          <div className="header-agent">
            {selectedAgent ? <Avatar agent={selectedAgent} size="small" /> : <div className="brand-mark mini">C</div>}
            <div>
              <strong>{viewTitle(view, selectedAgent)}</strong>
              {selectedAgent && view !== "chat" && <span>{selectedAgent.name}</span>}
            </div>
          </div>
          <div className="header-actions">
            {view === "chat" && selectedAgent && <Button variant="outline" disabled={chatBusy || resettingChat}
              onClick={() => void newChat()} title="Start a fresh context and keep earlier messages in the timeline">
              <Icon name="plus" /><span>{resettingChat ? "Starting…" : "New chat"}</span>
            </Button>}
            {view === "chat" && selectedAgent && (
              <Button variant="outline"
                className={`desktop-button ${desktopOpen ? "active" : ""}`}
                onClick={() => setDesktopOpen((value) => !value)}
                aria-label="Desktop"
                aria-haspopup="dialog"
                aria-expanded={desktopOpen}
                title="Open agent desktop"
              >
                <Icon name="monitor" />
                <span>Desktop</span>
              </Button>
            )}
            {selectedAgent && (
              <Button variant="ghost" size="icon" className="icon-button agent-settings" onClick={() => setEditOpen(true)} aria-label="Agent settings" title="Agent settings">
                <Icon name="settings" />
              </Button>
            )}
            <Badge variant="outline" className={`status-pill ${selectedAgent?.status ?? "stopped"}`}>
              <i /> {runtimeLabel(selectedAgent?.status)}
            </Badge>
          </div>
        </header>

        {banner && (
          <div className="api-banner" role="alert">
            <span>{banner}</span>
            <Button variant="ghost" onClick={() => setBanner(undefined)} aria-label="Dismiss"><Icon name="x" /></Button>
          </div>
        )}

        {view === "connections" ? (
          <ConnectionsPanel agent={selectedAgent} onCreate={() => setCreateOpen(true)} onError={setBanner} />
        ) : !selectedAgent ? (
          <EmptyState onCreate={() => setCreateOpen(true)} loading={loading} />
        ) : view === "chat" ? (
          <ChatPanel
            key={selectedAgent.id}
            agent={selectedAgent}
            activeRunId={activeRunId}
            setActiveRunId={setActiveRunId}
            conversationId={conversationId}
            setConversationId={setConversationId}
            onRunActiveChange={setChatBusy}
            resettingChat={resettingChat}
            timelineRevision={timelineRevision}
            onSelectAgent={selectAgent}
            onError={setBanner}
          />
        ) : (
          <SchedulesPanel key={selectedAgent.id} agent={selectedAgent} onError={setBanner} />
        )}
      </main>

      {selectedAgent && <DesktopSheet key={selectedAgent.id} agent={selectedAgent} open={desktopOpen} onOpenChange={setDesktopOpen} />}
      {createOpen && <CreateAgentModal models={models} onClose={() => setCreateOpen(false)} onCreated={onCreated} />}
      {editOpen && selectedAgent && <EditAgentModal models={models} key={selectedAgent.id} agent={selectedAgent} onClose={() => setEditOpen(false)} onUpdated={onUpdated} onDeleted={onDeleted} />}
    </div>
  );
}

function AuthGate({ mode, onAuthenticated }: { mode: "setup" | "login"; onAuthenticated: (session: Session) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [bootstrapToken, setBootstrapToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const isSetup = mode === "setup";

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (isSetup && password !== confirmation) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      if (isSetup) await api.setup({ username: username.trim(), password, bootstrapToken: bootstrapToken.trim() || undefined });
      const session = await api.login(username.trim(), password);
      onAuthenticated(session);
    } catch (cause) {
      setError(toMessage(cause, isSetup ? "Setup failed." : "Sign in failed."));
      setBusy(false);
    }
  };

  return (
    <main className="auth-page">
      <section className="auth-card">
        <div className="auth-brand"><span className="brand-mark">C</span><strong>Codexbot</strong></div>
        <span className="eyebrow">{isSetup ? "FIRST-RUN SETUP" : "LOCAL ADMIN"}</span>
        <h1>{isSetup ? "Create your administrator" : "Welcome back"}</h1>
        <p>{isSetup ? "Secure this self-hosted workspace with a local administrator account." : "Sign in to manage agents and their private desktops."}</p>
        <form onSubmit={submit}>
          <Label className="flex-col items-start leading-normal">Username<Input value={username} onChange={(event) => setUsername(event.target.value)} autoComplete="username" minLength={3} maxLength={64} autoFocus placeholder="admin" /></Label>
          <Label className="flex-col items-start leading-normal">Password<Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoComplete={isSetup ? "new-password" : "current-password"} minLength={12} placeholder="••••••••••••" /></Label>
          {isSetup && <>
            <Label className="flex-col items-start leading-normal">Confirm password<Input type="password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} autoComplete="new-password" minLength={12} placeholder="••••••••••••" /></Label>
            <Label className="flex-col items-start leading-normal">Bootstrap token <small>Only required when configured on the server</small><Input type="password" value={bootstrapToken} onChange={(event) => setBootstrapToken(event.target.value)} autoComplete="off" placeholder="Optional server bootstrap token" /></Label>
          </>}
          {error && <div className="form-error">{error}</div>}
          <Button variant="default" className="button primary large" disabled={busy || username.trim().length < 3 || password.length < 12 || (isSetup && confirmation.length < 12)}>{busy ? "Please wait…" : isSetup ? "Create administrator" : "Sign in"}</Button>
        </form>
        <div className="auth-foot"><Icon name="check" />Credentials stay on this Codexbot host.</div>
      </section>
    </main>
  );
}

function Sidebar({
  username,
  agents,
  selectedId,
  currentView,
  open,
  loading,
  onSelect,
  onView,
  onCreate,
  onClose,
  onLogout,
}: {
  username: string;
  agents: Agent[];
  selectedId?: string;
  currentView: View;
  open: boolean;
  loading: boolean;
  onSelect: (id: string) => void;
  onView: (view: View) => void;
  onCreate: () => void;
  onClose: () => void;
  onLogout: () => void;
}) {
  const [query, setQuery] = useState("");

  return (
    <>
      <aside className={`sidebar ${open ? "open" : ""}`}>
        <div className="sidebar-top">
          <a className="brand" href="/" aria-label="Codexbot home">
            <span className="brand-mark">C</span><strong>Codexbot</strong>
          </a>
          <Button variant="ghost" size="icon" className="icon-button add-agent" onClick={onCreate} aria-label="Add agent" title="Add agent">
            <Icon name="plus" />
          </Button>
          <Button variant="ghost" size="icon" className="icon-button close-sidebar" onClick={onClose} aria-label="Close agents"><Icon name="x" /></Button>
        </div>
        <Label className="search-box">
          <Icon name="search" />
          <Input aria-label="Search agents" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search agents" />
        </Label>

        <AgentSidebarList agents={agents} query={query} loading={loading} onCreate={onCreate} renderAgent={(agent) => (
            <Button variant="ghost"
              className={`agent-row ${agent.id === selectedId && currentView === "chat" ? "selected" : ""}`}
              key={agent.id}
              onClick={() => onSelect(agent.id)}
            >
              <Avatar agent={agent} />
              <span className="agent-copy">
              <span className="agent-name"><strong>{agent.name}</strong><i className={`state-dot ${agent.status}`} /></span>
                <small>{agent.rolePrompt || "Ready for a new task"}</small>
              </span>
            </Button>
          )} />

        <nav className="sidebar-nav">
          <Button variant="ghost" className={currentView === "schedules" ? "selected" : ""} onClick={() => onView("schedules")}>
            <Icon name="calendar" /><span>Schedules</span>
          </Button>
          <Button variant="ghost" className={currentView === "connections" ? "selected" : ""} onClick={() => onView("connections")}>
            <Icon name="link" /><span>Connections</span>
          </Button>
        </nav>
        <div className="account-row">
          <span className="account-avatar" aria-hidden="true">{Array.from(username).slice(0, 2).join("").toUpperCase()}</span>
          <span><strong title={username}>{username}</strong></span>
          <Button variant="ghost" className="account-lock h-auto p-0 text-[11px] text-muted-foreground" onClick={onLogout}>Sign out</Button>
        </div>
      </aside>
      {open && <Button variant="ghost" className="sidebar-scrim" onClick={onClose} aria-label="Close sidebar" />}
    </>
  );
}

export function ChatPanel({
  agent,
  activeRunId,
  setActiveRunId,
  conversationId,
  setConversationId,
  onRunActiveChange,
  resettingChat = false,
  timelineRevision = 0,
  onSelectAgent,
  onError,
}: {
  agent: Agent;
  activeRunId?: string;
  setActiveRunId: (runId: string | undefined) => void;
  conversationId?: string;
  setConversationId: (conversationId: string | undefined) => void;
  onRunActiveChange?: (active: boolean) => void;
  resettingChat?: boolean;
  timelineRevision?: number;
  onSelectAgent?: (agentId: string) => void;
  onError: (message: string) => void;
}) {
  const stream = useAgentTimeline(agent.id, timelineRevision);
  const [draft, setDraft] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const filePicker = useRef<HTMLInputElement>(null);
  const [sending, setSending] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const compositionActive = useRef(false);
  const mounted = useRef(true);
  const entries = stream.entries;
  const scheduledRunActive = Boolean(stream.runtimeBusy && !stream.runActive);
  const prependHeight = useRef<number | undefined>(undefined);
  const followLatest = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);

  useEffect(() => {
    onRunActiveChange?.(stream.runActive || Boolean(stream.runtimeBusy) || sending || stream.connection === "loading");
  }, [onRunActiveChange, stream.runActive, stream.runtimeBusy, sending, stream.connection]);

  useEffect(() => {
    setActiveRunId(stream.activeRunId);
    if (stream.conversationId) setConversationId(stream.conversationId);
  }, [stream.activeRunId, stream.conversationId, setActiveRunId, setConversationId]);

  useEffect(() => {
    const container = scrollRef.current;
    if (!container) return;
    if (prependHeight.current !== undefined) {
      container.scrollTop += container.scrollHeight - prependHeight.current;
      prependHeight.current = undefined;
    } else if (followLatest.current) {
      container.scrollTo?.({ top: container.scrollHeight, behavior: "smooth" });
    }
  }, [entries.length]);

  const send = async (event: FormEvent) => {
    event.preventDefault();
    const submittedDraft = draft;
    const message = submittedDraft.trim();
    if ((!message && files.length === 0) || sending || resettingChat || scheduledRunActive) return;
    const submittedFiles = files;
    setSending(true);
    try {
      const attachments = await Promise.all(submittedFiles.map(readAttachment));
      if (!mounted.current) return;
      const run = attachments.length
        ? await api.sendMessage(agent.id, message, conversationId, attachments)
        : await api.sendMessage(agent.id, message, conversationId);
      if (!mounted.current) return;
      setDraft((current) => current === submittedDraft ? "" : current);
      setFiles((current) => current.filter((file) => !submittedFiles.includes(file)));
      setConversationId(run.conversationId);
      setActiveRunId(run.runId);
      void stream.refresh();
    } catch (error) {
      if (mounted.current) {
        if (error instanceof ApiError && error.status === 409) void stream.refresh();
        onError(toMessage(error, "Could not send the message."));
      }
    } finally {
      if (mounted.current) setSending(false);
    }
  };

  const interrupt = () => {
    if (!stream.activeRunId && !activeRunId) return;
    api.interrupt(agent.id).catch((error) => onError(toMessage(error, "Could not stop the run.")));
  };

  return (
    <section className="chat-panel">
      <div className="messages" ref={scrollRef} onScroll={(event) => {
        const element = event.currentTarget;
        followLatest.current = element.scrollHeight - element.scrollTop - element.clientHeight < 80;
      }}>
        {stream.hasEarlier && <Button variant="ghost" disabled={stream.loadingEarlier} onClick={() => {
          prependHeight.current = scrollRef.current?.scrollHeight;
          void stream.loadEarlier();
        }}>{stream.loadingEarlier ? "Loading…" : "Load earlier messages"}</Button>}
        {entries.length === 0 ? (
          <ChatWelcome agent={agent} />
        ) : (
          <div className="message-stack">
            <div className="day-divider"><span>Agent timeline</span></div>
            {entries.map((entry) => <MessageEntry entry={entry} agent={agent} key={entry.id} onSelectAgent={onSelectAgent} />)}
          </div>
        )}
      </div>
      <div className="composer-wrap">
        {files.length > 0 && <div className="composer-attachments" aria-label="Attachments">
          {files.map((file, index) => <div className="composer-attachment" key={index}>
            <AttachmentPreview file={file} />
            <span title={file.name}>{file.name}<small>{Math.ceil(file.size / 1024)} KB</small></span>
            <Button variant="ghost" type="button" disabled={sending} aria-label={`Remove ${file.name}`} onClick={() => setFiles((current) => current.filter((_, i) => i !== index))}><Icon name="x" /></Button>
          </div>)}
        </div>}
        <input ref={filePicker} type="file" multiple hidden aria-label="Attach files" disabled={sending} onChange={(event) => {
          const selected = Array.from(event.target.files ?? []);
          event.target.value = "";
          const next = [...files, ...selected];
          if (next.length > MAX_ATTACHMENTS || next.reduce((total, file) => total + file.size, 0) > MAX_ATTACHMENT_BYTES) {
            onError("Attach up to 8 files, with a total size of 10 MiB or less.");
            return;
          }
          setFiles(next);
        }} />
        <form className="composer" onSubmit={send}>
          <Button variant="ghost" type="button" className="composer-leading" aria-label="Add attachments" title="Attach files (up to 8 files, 10 MiB total)" disabled={sending} onClick={() => filePicker.current?.click()}><Icon name="plus" /></Button>
          <Textarea
            className="border-0 shadow-none focus-visible:ring-0"
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onCompositionStart={() => { compositionActive.current = true; }}
            onCompositionEnd={() => { compositionActive.current = false; }}
            onKeyDown={(event) => {
              if (shouldSubmitComposerKey(event.nativeEvent, compositionActive.current)) {
                event.preventDefault();
                event.currentTarget.form?.requestSubmit();
              }
            }}
            rows={1}
            placeholder={`Message ${agent.name}`}
            aria-label={`Message ${agent.name}`}
          />
          {stream.runActive && <Button variant="destructive" type="button" className="send-button stop" onClick={interrupt} aria-label="Stop run"><Icon name="stop" /></Button>}
          <Button variant="default" className="send-button" disabled={(!draft.trim() && files.length === 0) || sending || resettingChat || scheduledRunActive} aria-label="Send message" title={stream.runActive ? "Add information to the current task" : "Send message"}><Icon name="send" /></Button>
        </form>
        {scheduledRunActive && <p role="status">Scheduled work is running; chat is available after it finishes.</p>}
        <div className="composer-meta">
          <span className={`connection-dot ${stream.connection}`} />
          <span title={stream.connectionError}>{stream.connection === "live" ? "Live" : stream.connection === "retrying" ? "Reconnecting" : "Loading timeline"}</span>
          <span>·</span><span>Enter to send · Shift + Enter for a new line</span>
        </div>
      </div>
    </section>
  );
}

function AttachmentPreview({ file }: { file: File }) {
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    setUrl(undefined);
    if (!["image/png", "image/jpeg", "image/gif", "image/webp"].includes(file.type)) return;
    const preview = URL.createObjectURL(file);
    setUrl(preview);
    return () => URL.revokeObjectURL(preview);
  }, [file]);
  return url ? <img src={url} alt={file.name} /> : null;
}

function ChatWelcome({ agent }: { agent: Agent }) {
  return (
    <div className="chat-welcome">
      <Avatar agent={agent} size="large" />
      <h1>What should {agent.name} work on?</h1>
      <p>{agent.rolePrompt || "This agent is ready to work in its private desktop and shared workspace."}</p>
      <div className="suggestion-grid">
        <span><Icon name="spark" /><strong>Work autonomously</strong><small>Research, edit files, and use the desktop</small></span>
        <span><Icon name="monitor" /><strong>Watch the desktop</strong><small>Observe progress or take control safely</small></span>
      </div>
    </div>
  );
}

function MessageEntry({ entry, agent, onSelectAgent }: { entry: ChatEntry; agent: Agent; onSelectAgent?: (agentId: string) => void }) {
  if (entry.kind === "context-reset") return <div className="context-reset" role="separator" aria-label="New chat"><strong>New chat</strong><p>{entry.text}</p></div>;
  if (entry.kind === "collaboration") return <Card className="activity-card shadow-none" id={`task-${entry.taskId}-${entry.id}`}>
    <Icon name="arrow" />
    <div><strong>{entry.title}</strong>{entry.text && <p>{entry.text}</p>}
      {entry.agentId && onSelectAgent && <Button variant="link" className="h-auto p-0" onClick={() => onSelectAgent(entry.agentId!)}>Open agent timeline</Button>}
      <small>Task {entry.taskId}</small>
    </div>
    {entry.status && <Badge variant="outline">{entry.status}</Badge>}
  </Card>;
  if (entry.kind === "activity") {
    return (
      <Card className="activity-card shadow-none">
        <span className={`activity-icon ${entry.status}`}>
          {entry.status === "done" ? <Icon name="check" /> : entry.status === "running" ? <span className="spinner" /> : "!"}
        </span>
        <div><strong>{entry.title}</strong>{entry.detail && <p>{entry.detail}</p>}</div>
        <Badge variant="outline" className={`activity-status ${entry.status}`}>{entry.status}</Badge>
      </Card>
    );
  }
  if (entry.kind === "notice") return <div className={`notice-message ${entry.severity ?? "error"}`}>{entry.text}</div>;
  return (
    <div className={`message-row ${entry.kind}`}>
      {entry.kind === "assistant" && <Avatar agent={agent} size="small" />}
      <div className="message-body">
        {entry.kind === "assistant" && <strong>{agent.name}</strong>}
        {entry.kind === "user" && entry.senderAgentId && <span>Delegated task {entry.taskId}{onSelectAgent && <Button variant="link" className="h-auto px-1 py-0" onClick={() => onSelectAgent(entry.senderAgentId!)}>Open requesting agent</Button>}</span>}
        <div className="message-bubble"><MarkdownMessage text={entry.text} />{entry.streaming && <span className="typing-caret" />}</div>
      </div>
    </div>
  );
}

// Suggested choices; custom IDs support models available to other accounts/providers.
const modelMaxEffort: Record<string, string> = {
  "gpt-6-astra": "ultra",
  "gpt-5.6-sol": "ultra",
  "gpt-5.6-terra": "ultra",
  "gpt-5.6-luna": "max",
  "gpt-5.5": "xhigh",
  "gpt-5.4-mini": "xhigh",
  "gpt-5.3-codex-spark": "xhigh",
};
const reasoningEfforts = ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"];

const fallbackModels: ModelOption[] = Object.entries(modelMaxEffort).map(([id, maxEffort]) => ({
  id, name: id, efforts: reasoningEfforts.slice(2, reasoningEfforts.indexOf(maxEffort) + 1),
}));

function AgentModelFields({ models, model, effort, onModelChange, onEffortChange, disabled }: {
  models: ModelOption[]; model: string; effort: string; onModelChange: (value: string) => void; onEffortChange: (value: string) => void; disabled: boolean;
}) {
  const [customInput, setCustom] = useState(false);
  const selectedModel = models.find((item) => item.id === model);
  const custom = customInput || Boolean(model && !selectedModel);
  const efforts = selectedModel?.efforts ?? reasoningEfforts;
  return <>
    <Label className="flex-col items-start leading-normal">Model<NativeSelect value={custom ? "custom" : model} disabled={disabled} onChange={(event) => {
      const value = event.target.value;
      setCustom(value === "custom");
      onModelChange(value === "custom" ? "" : value);
      onEffortChange("");
    }}>
      <NativeSelectOption value="">Default (runtime configuration)</NativeSelectOption>
      {models.map((item) => <NativeSelectOption key={item.id} value={item.id}>{item.name === item.id ? item.id : `${item.name} (${item.id})`}</NativeSelectOption>)}
      <NativeSelectOption value="custom">Custom model…</NativeSelectOption>
    </NativeSelect></Label>
    {custom && <Label className="flex-col items-start leading-normal">Model ID<Input value={model} onChange={(event) => { setCustom(true); onModelChange(event.target.value); }} disabled={disabled} required pattern={".*\\S.*"} maxLength={200} placeholder="Enter a model ID" /></Label>}
    <Label className="flex-col items-start leading-normal">Effort<NativeSelect value={effort} disabled={disabled} onChange={(event) => onEffortChange(event.target.value)}>
      <NativeSelectOption value="">Default (runtime configuration)</NativeSelectOption>
      {effort && !efforts.includes(effort) && <NativeSelectOption value={effort}>{effort}</NativeSelectOption>}
      {efforts.map((value) => <NativeSelectOption key={value} value={value}>{value}</NativeSelectOption>)}
    </NativeSelect></Label>
    <span className="model-hint">Model availability and supported effort levels depend on your connected account and runtime.</span>
  </>;
}

function AgentPermissionField({ permission, onChange, disabled }: {
  permission: Permission; onChange: (value: Permission) => void; disabled: boolean;
}) {
  return <>
    <Label className="flex-col items-start leading-normal">Permission<NativeSelect value={permission} onChange={(event) => onChange(event.target.value as Permission)} disabled={disabled} aria-describedby="permission-hint">
      <NativeSelectOption value="auto">Auto</NativeSelectOption>
      <NativeSelectOption value="full-access">Full Access</NativeSelectOption>
    </NativeSelect></Label>
    <span className="model-hint" id="permission-hint">{permission === "auto"
      ? "Evaluates tool safety automatically. No manual approval is required."
      : "Automatically allows all permission requests."}</span>
  </>;
}

function CreateAgentModal({ models, onClose, onCreated }: { models: ModelOption[]; onClose: () => void; onCreated: (agent: Agent) => void }) {
  const [name, setName] = useState("");
  const [icon, setIcon] = useState<IconUpload | null>();
  const [readingIcon, setReadingIcon] = useState(false);
  const [rolePrompt, setRolePrompt] = useState("");
  const [model, setModel] = useState("");
  const [effort, setEffort] = useState("");
  const [permission, setPermission] = useState<Permission>("auto");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string>();
  const returnFocus = useRef(document.activeElement as HTMLElement | null);
  const nameInput = useRef<HTMLInputElement>(null);
  useEffect(() => nameInput.current?.focus(), []);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim() || !rolePrompt.trim() || saving || readingIcon) return;
    setSaving(true);
    setError(undefined);
    try {
      onCreated(await api.createAgent({ name: name.trim(), rolePrompt: rolePrompt.trim(), model: model.trim(), effort, permission, ...(icon !== undefined ? { icon } : {}) }));
    } catch (cause) {
      setError(toMessage(cause, "Could not create the agent."));
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="modal block max-h-[calc(100dvh-2rem)] overflow-y-auto" onCloseAutoFocus={(event) => { event.preventDefault(); const target = returnFocus.current?.isConnected ? returnFocus.current : document.querySelector<HTMLElement>('[aria-label="Agent settings"], [aria-label="Add agent"]'); target?.focus(); }}>
        <div className="modal-kicker"><span className="brand-mark">C</span><span>New workspace</span></div>
        <DialogTitle>Create an agent</DialogTitle>
        <DialogDescription>Give this agent a name and a durable role. Its desktop and private profile will be created automatically.</DialogDescription>
        <form onSubmit={submit}>
          <Label className="flex-col items-start leading-normal">Agent name<Input ref={nameInput} value={name} onChange={(event) => setName(event.target.value)} maxLength={80} placeholder="e.g. Sales Outbound" /></Label>
          <AgentIconPicker name={name} value={icon} onChange={setIcon} disabled={saving} onBusyChange={setReadingIcon} />
          <Label className="flex-col items-start leading-normal">Role and instructions<Textarea value={rolePrompt} onChange={(event) => setRolePrompt(event.target.value)} rows={6} maxLength={8000} placeholder="Describe what this agent owns, how it should work, and any important boundaries…" /></Label>
          <AgentModelFields models={models} model={model} effort={effort} onModelChange={setModel} onEffortChange={setEffort} disabled={saving} />
          <AgentPermissionField permission={permission} onChange={setPermission} disabled={saving} />
          <div className="role-hint"><Icon name="spark" /><span>These instructions define the agent’s role in its ongoing conversation.</span></div>
          {error && <div className="form-error">{error}</div>}
          <div className="modal-actions"><Button variant="outline" type="button" className="button secondary" onClick={onClose}>Cancel</Button><Button variant="default" className="button primary" disabled={!name.trim() || !rolePrompt.trim() || saving || readingIcon}>{saving ? "Creating…" : "Create agent"}</Button></div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function EditAgentModal({ models, agent, onClose, onUpdated, onDeleted }: { models: ModelOption[]; agent: Agent; onClose: () => void; onUpdated: (agent: Agent) => void; onDeleted: (agentId: string) => void }) {
  const [name, setName] = useState(agent.name);
  const [icon, setIcon] = useState<IconUpload | null>();
  const [readingIcon, setReadingIcon] = useState(false);
  const [rolePrompt, setRolePrompt] = useState(agent.rolePrompt);
  const [model, setModel] = useState(agent.model ?? "");
  const [effort, setEffort] = useState(agent.effort ?? "");
  const [permission, setPermission] = useState<Permission>(agent.permission ?? "auto");
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string>();
  const returnFocus = useRef(document.activeElement as HTMLElement | null);
  const nameInput = useRef<HTMLInputElement>(null);
  useEffect(() => nameInput.current?.focus(), []);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim() || !rolePrompt.trim() || saving || deleting || readingIcon) return;
    setSaving(true);
    setError(undefined);
    try {
      onUpdated(await api.updateAgent(agent.id, { name: name.trim(), rolePrompt: rolePrompt.trim(), model: model.trim(), effort, permission, ...(icon !== undefined ? { icon } : {}) }));
    } catch (cause) {
      setError(toMessage(cause, "Could not update the agent."));
      setSaving(false);
    }
  };

  const remove = async () => {
    if (deleting || saving) return;
    setDeleting(true);
    setError(undefined);
    try {
      await api.archiveAgent(agent.id);
      onDeleted(agent.id);
    } catch (cause) {
      setError(toMessage(cause, "Could not delete the agent."));
      setDeleting(false);
    }
  };

  return (
    <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
      <DialogContent className="modal block max-h-[calc(100dvh-2rem)] overflow-y-auto" onCloseAutoFocus={(event) => { event.preventDefault(); const target = returnFocus.current?.isConnected ? returnFocus.current : document.querySelector<HTMLElement>('[aria-label="Agent settings"], [aria-label="Add agent"]'); target?.focus(); }}>
        <div className="modal-kicker"><Avatar agent={agent} size="small" /><span>Agent settings</span></div>
        <DialogTitle>Edit {agent.name}</DialogTitle>
        <DialogDescription>Role, model, effort, and permission changes start a fresh context for the next chat. Name and icon changes keep the current context.</DialogDescription>
        <form onSubmit={submit}>
          <Label className="flex-col items-start leading-normal">Agent name<Input ref={nameInput} value={name} onChange={(event) => setName(event.target.value)} maxLength={80} /></Label>
          <AgentIconPicker name={name} iconUrl={agent.iconUrl} value={icon} onChange={setIcon} disabled={saving || deleting} onBusyChange={setReadingIcon} />
          <Label className="flex-col items-start leading-normal">Role and instructions<Textarea value={rolePrompt} onChange={(event) => setRolePrompt(event.target.value)} rows={7} maxLength={16384} /></Label>
          <AgentModelFields models={models} model={model} effort={effort} onModelChange={setModel} onEffortChange={setEffort} disabled={saving} />
          <AgentPermissionField permission={permission} onChange={setPermission} disabled={saving} />
          <div className="role-hint"><Icon name="spark" /><span>Updated instructions apply to subsequent tasks in this agent’s conversation.</span></div>
          {error && <div className="form-error">{error}</div>}
          <div className="danger-zone">
            {!confirmDelete ? (
              <Button variant="outline" type="button" className="button danger-outline text-destructive hover:text-destructive" onClick={() => setConfirmDelete(true)}><Icon name="trash" />Delete agent</Button>
            ) : (
              <div className="delete-confirm"><span>Stop and remove <strong>{agent.name}</strong> from this workspace?</span><div><Button variant="outline" type="button" className="button secondary" onClick={() => setConfirmDelete(false)}>Cancel</Button><Button variant="destructive" type="button" className="button danger" onClick={() => void remove()} disabled={deleting}>{deleting ? "Deleting…" : "Delete agent"}</Button></div></div>
            )}
          </div>
          <div className="modal-actions"><Button variant="outline" type="button" className="button secondary" onClick={onClose}>Cancel</Button><Button variant="default" className="button primary" disabled={!name.trim() || !rolePrompt.trim() || saving || deleting || readingIcon}>{saving ? "Saving…" : "Save changes"}</Button></div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function SchedulesPanel({ agent, onError }: { agent: Agent; onError: (message: string) => void }) {
  const [schedules, setSchedules] = useState<Schedule[]>([]);
  const [selectedScheduleId, setSelectedScheduleId] = useState<string>();
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [prompt, setPrompt] = useState("");
  const [type, setType] = useState<"once" | "cron">("cron");
  const [expression, setExpression] = useState("0 9 * * 1-5");
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "Asia/Tokyo";

  const refresh = () => api.listSchedules(agent.id).then(setSchedules).catch((error) => onError(toMessage(error, "Could not load schedules.")));
  useEffect(() => {
    void refresh();
  }, [agent.id]);

  const create = async (event: FormEvent) => {
    event.preventDefault();
    try {
      const schedule = await api.createSchedule({
        agentId: agent.id,
        name: name.trim(),
        prompt: prompt.trim(),
        kind: type,
        expression: type === "once" ? new Date(expression).toISOString() : expression,
        timezone,
        enabled: true,
      });
      setSchedules((current) => [...current, schedule]);
      setName(""); setPrompt(""); setCreating(false);
    } catch (error) { onError(toMessage(error, "Could not create the schedule.")); }
  };
  const toggle = async (schedule: Schedule) => {
    try {
      const updated = await api.setScheduleEnabled(schedule.id, !schedule.enabled);
      setSchedules((items) => items.map((item) => item.id === updated.id ? updated : item));
    } catch (error) {
      onError(toMessage(error, "Could not update the schedule."));
    }
  };

  if (selectedScheduleId) return <ScheduleDetail key={selectedScheduleId} agent={agent} scheduleId={selectedScheduleId}
    onBack={() => { setSelectedScheduleId(undefined); void refresh(); }}
    renderEntry={(entry) => <MessageEntry entry={entry} agent={agent} key={entry.id} />} />;

  return (
    <section className="settings-page">
      <div className="page-heading"><div><span className="eyebrow">AUTOMATION</span><h1>Schedules</h1><p>Give {agent.name} recurring or one-time work. Each run starts with a fresh context, separate from chat. Open a schedule to view its execution history and output.</p></div><Button variant="default" className="button primary" onClick={() => setCreating(true)}><Icon name="plus" />New schedule</Button></div>
      {creating && <form className="schedule-form" onSubmit={create}><Label className="flex-col items-start leading-normal">Schedule name<Input value={name} onChange={(event) => setName(event.target.value)} placeholder="e.g. Weekday pipeline review" /></Label><Label className="flex-col items-start leading-normal">Task<Textarea rows={3} value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder="What should the agent do?" /></Label><div className="schedule-fields"><Label className="flex-col items-start leading-normal">Run<NativeSelect value={type} onChange={(event) => setType(event.target.value as "once" | "cron")}><NativeSelectOption value="cron">On a schedule</NativeSelectOption><NativeSelectOption value="once">Once</NativeSelectOption></NativeSelect></Label><Label className="flex-col items-start leading-normal">{type === "cron" ? "Cron expression" : "Date and time"}<Input type={type === "cron" ? "text" : "datetime-local"} value={expression} onChange={(event) => setExpression(event.target.value)} /></Label><Label className="flex-col items-start leading-normal">Timezone<Input value={timezone} disabled /></Label></div><div className="schedule-warning"><Icon name="spark" />Scheduled tasks can use the web and perform external actions without asking for approval. Every action is audited.</div><div className="form-actions"><Button variant="outline" type="button" className="button secondary" onClick={() => setCreating(false)}>Cancel</Button><Button variant="default" className="button primary" disabled={!name.trim() || !prompt.trim() || !expression}>Create schedule</Button></div></form>}
      <div className="schedule-list">
        {schedules.length === 0 && !creating && <div className="section-empty"><span><Icon name="calendar" /></span><h3>No schedules yet</h3><p>Automate a recurring workflow or queue work for later.</p></div>}
        {schedules.map((schedule) => <Card className="schedule-card shadow-none" key={schedule.id}><div className="schedule-icon"><Icon name="calendar" /></div><div className="schedule-copy"><Button variant="link" className="schedule-name h-auto justify-start p-0" onClick={() => setSelectedScheduleId(schedule.id)}>{schedule.name}</Button><span>{schedule.kind === "cron" ? schedule.expression : formatDate(schedule.expression)} · {schedule.timezone}</span><small>{schedule.prompt}{schedule.nextRunAt ? ` · Next run ${formatDate(schedule.nextRunAt)}` : ""}</small></div><Switch checked={schedule.enabled} onCheckedChange={() => void toggle(schedule)} aria-label={`${schedule.enabled ? "Disable" : "Enable"} schedule ${schedule.name}`} /><Button variant="ghost" size="icon" className="icon-button card-action danger" onClick={async () => { try { await api.deleteSchedule(schedule.id); setSchedules((items) => items.filter((item) => item.id !== schedule.id)); } catch (error) { onError(toMessage(error, "Could not delete the schedule.")); } }} title="Delete"><Icon name="trash" /></Button></Card>)}
      </div>
    </section>
  );
}

function ConnectionsPanel({ agent, onCreate, onError }: { agent?: Agent; onCreate: () => void; onError: (message: string) => void }) {
  const [status, setStatus] = useState<Agent["providerAuthState"]>(agent?.providerAuthState ?? "disconnected");
  const [device, setDevice] = useState<DeviceCode>();
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!agent) return;
    setStatus(agent.providerAuthState);
    setDevice(undefined);
    void api.getProviderAuth(agent.id).then((result) => setStatus(result.state)).catch(() => undefined);
  }, [agent?.id, agent?.providerAuthState]);
  useEffect(() => {
    if (!agent) return;
    const interval = status === "pending" ? 3000 : 30_000;
    const timer = window.setInterval(() => {
      void api.getProviderAuth(agent.id).then((result) => setStatus(result.state)).catch(() => undefined);
    }, interval);
    return () => window.clearInterval(timer);
  }, [agent?.id, status]);

  const perform = async (operation: () => Promise<void>) => { setBusy(true); try { await operation(); } catch (error) { onError(toMessage(error, "Connection request failed.")); } finally { setBusy(false); } };

  if (!agent) {
    return (
      <section className="settings-page">
        <div className="page-heading"><div><span className="eyebrow">SHARED IDENTITY</span><h1>OpenAI connection</h1><p>Codex credentials are shared by every agent through ~/.codex.</p></div></div>
        <div className="section-empty"><span><Icon name="link" /></span><h3>No agents yet</h3><p>Create an agent before connecting it to OpenAI.</p><Button variant="default" className="button primary" onClick={onCreate}><Icon name="plus" />Create an agent</Button></div>
      </section>
    );
  }

  return (
    <section className="settings-page">
      <div className="page-heading"><div><span className="eyebrow">SHARED IDENTITY</span><h1>OpenAI connection</h1><p>Connecting from {agent.name} updates the shared ~/.codex identity used by every agent.</p></div></div>
      <Card className="connection-card shadow-none">
        <div className="provider-logo">O</div><div className="provider-heading"><h2>OpenAI</h2><p>Codex App Server</p></div><Badge variant="outline" className={`provider-state ${status}`}><i />{status}</Badge>
        {status === "connected" ? <div className="connection-details"><div><span>Authentication</span><strong>Connected to Codex</strong></div><Button variant="outline" className="button danger-outline text-destructive hover:text-destructive" disabled={busy} onClick={() => perform(async () => { await api.logoutProvider(agent.id); setStatus("disconnected"); })}>Disconnect</Button></div> : <div className="auth-options"><div className="auth-option"><Icon name="link" /><div><strong>Sign in with ChatGPT</strong><p>Use a device code. Your browser never receives the agent’s session credentials.</p>{device && <div className="device-code"><a href={device.verificationUrl} target="_blank" rel="noreferrer">Open verification page <Icon name="arrow" /></a><Button variant="ghost" type="button" onClick={() => { navigator.clipboard.writeText(device.userCode); setCopied(true); }}>{device.userCode}<Icon name={copied ? "check" : "copy"} /></Button></div>}</div><Button variant="outline" className="button secondary" disabled={busy} onClick={() => perform(async () => { setDevice(await api.startDeviceCode(agent.id)); setStatus("pending"); })}>{device ? "New code" : "Get code"}</Button></div><div className="auth-divider"><span>or</span></div><form className="auth-option api-key-option" onSubmit={(event) => { event.preventDefault(); perform(async () => { await api.registerApiKey(agent.id, apiKey); setStatus("connected"); setApiKey(""); }); }}><Icon name="key" /><div><strong>Use an API key</strong><p>The key is handed directly to Codex and is never saved in the app database or logs.</p><Input aria-label="API key" type="password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} autoComplete="off" placeholder="sk-…" /></div><Button variant="outline" className="button secondary" disabled={!apiKey.trim() || busy}>Connect</Button></form></div>}
      </Card>
      <div className="security-note"><Icon name="check" /><div><strong>Shared Codex identity</strong><p>Authentication, configuration, and sessions are shared through ~/.codex. Browser and desktop files use separate per-agent volumes; all agents remain mutually trusted.</p></div></div>
    </section>
  );
}

function EmptyState({ onCreate, loading }: { onCreate: () => void; loading: boolean }) {
  if (loading) return <div className="center-loader"><span className="spinner large" /></div>;
  return <div className="empty-workspace"><div className="empty-orbit"><span className="brand-mark big">C</span></div><span className="eyebrow">YOUR PRIVATE AGENT TEAM</span><h1>Build your first workspace</h1><p>Each agent gets an isolated desktop and Codex runtime, while sharing the files you choose.</p><Button variant="default" className="button primary large" onClick={onCreate}><Icon name="plus" />Create an agent</Button></div>;
}

const icons = { plus: Plus, monitor: Monitor, search: Search, calendar: CalendarDays, link: Link, send: Send, stop: Square, x: X, check: Check, spark: Sparkles, play: Play, trash: Trash2, copy: Copy, key: KeyRound, menu: Menu, settings: Settings2, arrow: ArrowUpRight };
function Icon({ name }: { name: IconName }) {
  const Component = icons[name];
  return <Component aria-hidden="true" strokeWidth={1.75} />;
}

function viewTitle(view: View, agent?: Agent) { return view === "chat" ? agent?.name ?? "Codexbot" : view === "schedules" ? "Schedules" : "Connections"; }
function runtimeLabel(state?: Agent["status"]) { return state === "running" ? "Online" : state === "starting" ? "Starting" : state === "error" ? "Needs attention" : "Offline"; }
function formatDate(value: string) { const date = new Date(value); return Number.isNaN(date.valueOf()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date); }
function toMessage(error: unknown, fallback: string) { return error instanceof ApiError || error instanceof Error ? error.message : fallback; }
