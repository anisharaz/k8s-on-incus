import { type ReactNode, useEffect, useState } from "react";
import { TerminalSessionsContext } from "./terminalSessions.context";
import type { OpenTerminalSession } from "./terminalSessions.context";
import { useAuth } from "./useAuth";
import type { ClusterNode } from "@/lib/types";

// Mounted once at the app root (see App.tsx), above <Routes> — not inside
// the /terminal/:clusterId/:nodeId route itself. Session state used to live
// in TerminalPage's own useState, so navigating away from that route (the
// "Back" link, a sidebar link, browser back/forward) unmounted the whole
// page and silently closed every open terminal session at once, with no
// warning — the exact opposite of this feature's point. Living here
// instead means sessions (and the TerminalPane instances that hold their
// websockets/xterm buffers, rendered by TerminalOverlay) survive ordinary
// in-app navigation; only an explicit close, or actually leaving/reloading
// the page, ends one.
export function TerminalSessionsProvider({
  children,
}: {
  children: ReactNode;
}) {
  const [sessions, setSessions] = useState<OpenTerminalSession[]>([]);
  const { status } = useAuth();

  function registerSession(
    clusterId: string,
    clusterName: string,
    node: ClusterNode,
  ) {
    setSessions((prev) => {
      if (prev.some((s) => s.node.id === node.id)) return prev;
      return [...prev, { clusterId, clusterName, node }];
    });
  }

  function closeSession(nodeId: string) {
    setSessions((prev) => prev.filter((s) => s.node.id !== nodeId));
  }

  // Reloading/closing the tab kills every open terminal session — warn
  // before that happens. Browsers ignore custom text here and show their
  // own generic prompt; setting returnValue is what triggers it at all.
  useEffect(() => {
    if (sessions.length === 0) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [sessions.length]);

  // Logging out (or the session expiring) ends every open terminal too —
  // otherwise the next person to use this browser as a different user
  // would inherit live shell sessions into someone else's nodes.
  useEffect(() => {
    const clearSessions = () => setSessions([]);
    if (status !== "authenticated") clearSessions();
  }, [status]);

  return (
    <TerminalSessionsContext.Provider
      value={{ sessions, registerSession, closeSession }}
    >
      {children}
    </TerminalSessionsContext.Provider>
  );
}
