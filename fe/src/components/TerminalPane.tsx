import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

import { cn } from "@/lib/utils";
import type { TerminalSessionStatus } from "@/context";

interface TerminalPaneProps {
  clusterId: string;
  nodeId: string;
  /** Whether this pane is the one currently shown. Inactive panes stay
   * mounted (session alive) but hidden — see TerminalOverlay, which keeps
   * every opened pane mounted so switching nodes (or navigating away and
   * back) doesn't kill the connection. */
  active: boolean;
  /** Reports the websocket's lifecycle so a session that dies in the
   * background isn't invisible — TerminalOverlay reflects this in the
   * session pill list, since this pane may not even be the visible one. */
  onStatusChange?: (status: TerminalSessionStatus) => void;
}

// Bridges an xterm.js instance to the node's interactive shell over the
// terminal websocket. The session's lifecycle (setup/teardown) is tied to
// this component's mount/unmount only — NOT to `active` — so a caller that
// keeps inactive panes mounted keeps their sessions alive in the
// background.
export function TerminalPane({
  clusterId,
  nodeId,
  active,
  onStatusChange,
}: TerminalPaneProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitAddonRef = useRef<FitAddon | null>(null);
  const activeRef = useRef(active);
  useEffect(() => {
    activeRef.current = active;
  }, [active]);

  // Keeps onStatusChange callable from the socket's event handlers without
  // making the setup effect below re-run (and so tear down/reopen the
  // websocket) every time the caller passes a new function reference.
  const onStatusChangeRef = useRef(onStatusChange);
  useEffect(() => {
    onStatusChangeRef.current = onStatusChange;
  }, [onStatusChange]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    // Guards the socket's event handlers against firing after this effect
    // has already been torn down (unmount, or clusterId/nodeId changing) —
    // without it, a late event from an old, already-replaced socket could
    // overwrite the status a newer one already reported.
    let torndown = false;

    onStatusChangeRef.current?.("connecting");

    const term = new Terminal({
      cursorBlink: true,
      convertEol: true,
      fontSize: 13,
    });
    const fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(container);
    fitAddon.fit();
    termRef.current = term;
    fitAddonRef.current = fitAddon;

    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(
      `${proto}//${window.location.host}/api/v1/clusters/${clusterId}/nodes/${nodeId}/terminal`,
    );
    ws.binaryType = "arraybuffer";

    const sendResize = () => {
      if (ws.readyState !== WebSocket.OPEN) return;
      ws.send(
        JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }),
      );
    };

    let errored = false;

    ws.onopen = () => {
      sendResize();
      term.focus();
      if (!torndown) onStatusChangeRef.current?.("connected");
    };
    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") return; // reserved for future control frames
      term.write(new Uint8Array(ev.data as ArrayBuffer));
    };
    ws.onerror = () => {
      errored = true;
      term.writeln("\r\n\x1b[31mConnection error.\x1b[0m");
      if (!torndown) onStatusChangeRef.current?.("error");
    };
    ws.onclose = () => {
      term.writeln("\r\n\x1b[33mSession closed.\x1b[0m");
      if (!torndown) onStatusChangeRef.current?.(errored ? "error" : "closed");
    };

    const dataDisposable = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(new TextEncoder().encode(data));
      }
    });

    // Going inactive hides this pane (display:none), which ResizeObserver
    // reports as a resize to 0x0 — fit()-ing to that would collapse the
    // terminal to ~1 column, and any output that arrives while hidden
    // would wrap/garble permanently (xterm doesn't re-wrap old buffer
    // content when cols changes back). Skip entirely while inactive.
    const resizeObserver = new ResizeObserver(() => {
      if (!activeRef.current) return;
      fitAddon.fit();
      sendResize();
    });
    resizeObserver.observe(container);

    return () => {
      torndown = true;
      resizeObserver.disconnect();
      dataDisposable.dispose();
      ws.close();
      term.dispose();
      termRef.current = null;
      fitAddonRef.current = null;
    };
  }, [clusterId, nodeId]);

  // Re-fit and refocus whenever this pane becomes the visible one — its
  // container has zero size while hidden, so xterm's own dimensions go
  // stale until we explicitly re-measure.
  useEffect(() => {
    if (!active) return;
    fitAddonRef.current?.fit();
    termRef.current?.focus();
  }, [active]);

  return (
    <div
      ref={containerRef}
      className={cn(
        "h-full w-full overflow-hidden rounded-lg bg-black p-2",
        !active && "hidden",
      )}
    />
  );
}
