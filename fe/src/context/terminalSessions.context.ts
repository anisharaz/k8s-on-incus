import { createContext } from "react";
import type { ClusterNode } from "@/lib/types";

export interface OpenTerminalSession {
  clusterId: string;
  clusterName: string;
  node: ClusterNode;
}

export interface TerminalSessionsContextType {
  sessions: OpenTerminalSession[];
  registerSession: (
    clusterId: string,
    clusterName: string,
    node: ClusterNode,
  ) => void;
  closeSession: (nodeId: string) => void;
}

export const TerminalSessionsContext = createContext<
  TerminalSessionsContextType | undefined
>(undefined);
