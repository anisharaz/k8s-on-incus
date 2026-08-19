import { useContext } from "react";
import { TerminalSessionsContext } from "./terminalSessions.context";

export function useTerminalSessions() {
  const context = useContext(TerminalSessionsContext);
  if (context === undefined) {
    throw new Error(
      "useTerminalSessions must be used within a TerminalSessionsProvider",
    );
  }

  return context;
}
