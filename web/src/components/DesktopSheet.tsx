import { Maximize, Minimize, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api } from "../api";
import type { Agent, DesktopLease } from "../types";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Sheet, SheetClose, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "./ui/sheet";

export function DesktopSheet({ agent, open, onOpenChange }: {
  agent: Agent;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [lease, setLease] = useState<DesktopLease>();
  const [loading, setLoading] = useState(true);
  const [changing, setChanging] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);
  const [maximized, setMaximized] = useState(false);
  const [resizing, setResizing] = useState(false);
  const [error, setError] = useState<string>();
  const contentRef = useRef<HTMLDivElement>(null);
  const openRef = useRef(open);
  openRef.current = open;

  // Keep the control state and heartbeat when the sheet is temporarily closed.
  useEffect(() => {
    if (lease?.holder !== "human") return;
    const timer = window.setInterval(() => {
      void api.heartbeatDesktop(agent.id).then(setLease).catch((error: unknown) => {
        setError(error instanceof Error ? error.message : "Desktop control lease expired.");
      });
    }, 60_000);
    return () => window.clearInterval(timer);
  }, [agent.id, lease?.holder]);

  useEffect(() => {
    const syncFullscreen = () => {
      const content = contentRef.current;
      setMaximized(!!content && !!document.fullscreenElement && content.contains(document.fullscreenElement));
    };
    document.addEventListener("fullscreenchange", syncFullscreen);
    return () => document.removeEventListener("fullscreenchange", syncFullscreen);
  }, []);

  useEffect(() => {
    if (open) setLoading(true);
    else {
      setMaximized(false);
      const content = contentRef.current;
      if (content && document.fullscreenElement && content.contains(document.fullscreenElement)) {
        void document.exitFullscreen().catch(() => { /* Removing the sheet also exits fullscreen. */ });
      }
    }
  }, [open]);

  const toggleFullscreen = async () => {
    const content = contentRef.current;
    if (!content || resizing) return;
    setResizing(true);
    setError(undefined);
    try {
      if (maximized) {
        if (document.fullscreenElement) await document.exitFullscreen();
        setMaximized(false);
      } else if (content.requestFullscreen && document.fullscreenEnabled !== false) {
        await content.requestFullscreen();
        // A sheet may be dismissed while the browser processes the request.
        if (!openRef.current) {
          if (document.fullscreenElement === content) await document.exitFullscreen();
          return;
        }
        setMaximized(true);
      } else {
        // Browsers without the Fullscreen API still get the entire viewport.
        setMaximized(true);
      }
    } catch (error) {
      setError(error instanceof Error ? error.message : "Could not change desktop fullscreen mode.");
    } finally { setResizing(false); }
  };

  const changeControl = async () => {
    setChanging(true);
    setError(undefined);
    try {
      const nextLease = lease?.holder === "human"
        ? await api.releaseDesktop(agent.id)
        : await api.takeOverDesktop(agent.id);
      setLease(nextLease);
      if (nextLease.continueError) setError(`Desktop control was released, but the agent could not continue: ${nextLease.continueError}`);
      setLoading(true);
      setReloadKey((value) => value + 1);
    } catch (error) {
      setError(error instanceof Error ? error.message : "Could not change desktop control.");
    } finally { setChanging(false); }
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        ref={contentRef}
        className={`desktop-sheet h-dvh w-full gap-0 sm:max-w-[min(90vw,1440px)] ${maximized ? "desktop-sheet-maximized" : ""}`}
        showCloseButton={false}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          document.querySelector<HTMLButtonElement>('[aria-label="Desktop"]')?.focus();
        }}
        onEscapeKeyDown={(event) => {
          if (maximized) {
            event.preventDefault();
            void toggleFullscreen();
          }
        }}
      >
        <SheetHeader className="flex-row items-center justify-between gap-3 border-b">
          <div className="min-w-0">
            <SheetTitle className="truncate">{agent.name} desktop</SheetTitle>
            <SheetDescription>Watch the agent or take control of its desktop.</SheetDescription>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Button variant="ghost" size="icon" onClick={() => void toggleFullscreen()} disabled={resizing}
              aria-label={maximized ? "Restore desktop" : "Maximize desktop"}
              title={maximized ? "Restore desktop" : "Maximize desktop"}>
              {maximized ? <Minimize /> : <Maximize />}
            </Button>
            <SheetClose asChild>
              <Button variant="ghost" size="icon" aria-label="Close desktop" title="Close desktop"><X /></Button>
            </SheetClose>
          </div>
        </SheetHeader>
        {error && <div className="api-banner shrink-0" role="alert"><span>{error}</span><Button variant="ghost" size="icon" onClick={() => setError(undefined)} aria-label="Dismiss desktop error"><X /></Button></div>}
        <section className="desktop-panel">
          <div className="desktop-toolbar">
            <div><Badge variant="outline" className={`view-badge ${lease?.holder === "human" ? "control" : "view"}`}><i />{lease?.holder === "human" ? "You have control" : "View only"}</Badge><small>{lease?.holder === "human" ? "The agent is paused while you work." : "Agent input remains active."}</small></div>
            <Button variant={lease?.holder === "human" ? "outline" : "default"} onClick={() => void changeControl()} disabled={changing}>
              {lease?.holder === "human" ? "Release control" : "Take control"}
            </Button>
          </div>
          <div className="desktop-frame-wrap">
            {loading && <div className="desktop-loading"><span className="spinner large" /><strong>Starting {agent.name}’s desktop…</strong><small>XFCE and KasmVNC may take a few seconds.</small></div>}
            <iframe key={reloadKey} className="desktop-frame" src={api.desktopUrl(agent.id, reloadKey)} title={`${agent.name} desktop`}
              onLoad={() => setLoading(false)} allow="clipboard-read; clipboard-write; fullscreen" allowFullScreen />
          </div>
        </section>
      </SheetContent>
    </Sheet>
  );
}
