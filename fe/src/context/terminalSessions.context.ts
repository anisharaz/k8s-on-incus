import { createContext } from "react";
import type { ClusterNode } from "@/lib/types";

// "connecting"/"connected" while the websocket is being (re)established or
// open; "error"/"closed" once it's dead — distinguished so a pane that
// failed outright can be styled differently from one that simply ended.
export type TerminalSessionStatus =
  | "connecting"
  | "connected"
  | "error"
  | "closed";

export interface OpenTerminalSession {
  clusterId: string;
  clusterName: string;
  node: ClusterNode;
  status: TerminalSessionStatus;
  // Bumped by reconnectSession to force TerminalPane to fully remount (a
  // fresh WebSocket) — see TerminalOverlay's key on TerminalPane.
  generation: number;
}

export interface TerminalSessionsContextType {
  sessions: OpenTerminalSession[];
  registerSession: (
    clusterId: string,
    clusterName: string,
    node: ClusterNode,
  ) => void;
  closeSession: (nodeId: string) => void;
  setSessionStatus: (nodeId: string, status: TerminalSessionStatus) => void;
  reconnectSession: (nodeId: string) => void;
}

export const TerminalSessionsContext = createContext<
  TerminalSessionsContextType | undefined
>(undefined);
