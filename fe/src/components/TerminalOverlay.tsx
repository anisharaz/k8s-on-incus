import { useEffect, useState } from "react";
import { Link, Navigate, useLocation, useNavigate } from "react-router";
import { AlertCircle, ArrowLeft, RotateCw, X } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { TerminalPane } from "@/components/TerminalPane";
import { FullPageSpinner } from "@/components/FullPageSpinner";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { useAuth, useTerminalSessions } from "@/context";
import type { Cluster, ClusterNode } from "@/lib/types";

const TERMINAL_ROUTE = /^\/terminal\/([^/]+)\/([^/]+)$/;

// Renders the terminal UI (header + panes) and is mounted once at the app
// root (see App.tsx), not as the /terminal/:clusterId/:nodeId route's own
// element — so it never unmounts on ordinary navigation, which is what lets
// TerminalSessionsProvider's sessions (and the TerminalPanes below, which
// hold the actual websockets/xterm buffers) survive it. Visibility is
// purely CSS (`invisible`, not `hidden`/display:none — see the note on the
// wrapping div) toggled by whether the current URL matches the terminal
// route; clusterId/nodeId come from parsing the URL directly rather than
// useParams(), since this component isn't rendered as part of a matched
// Route and so has no route params of its own.
export function TerminalOverlay() {
  const location = useLocation();
  const navigate = useNavigate();
  const { status } = useAuth();
  const {
    sessions,
    registerSession,
    closeSession,
    setSessionStatus,
    reconnectSession,
  } = useTerminalSessions();

  const match = TERMINAL_ROUTE.exec(location.pathname);
  const onRoute = match !== null;
  const clusterId = match?.[1];
  const nodeId = match?.[2];

  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [nodes, setNodes] = useState<ClusterNode[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!onRoute) return;
    let isMounted = true;
    api
      .get<{ clusters: Cluster[] }>("/api/v1/clusters")
      .then((data) => {
        if (isMounted) setClusters(data.clusters ?? []);
      })
      .catch(() => {
        // the per-cluster node fetch below surfaces the real error
      });
    return () => {
      isMounted = false;
    };
  }, [onRoute]);

  useEffect(() => {
    if (!onRoute || !clusterId) return;
    let isMounted = true;

    const load = async () => {
      setLoading(true);
      setError(null);
      try {
        const data = await api.get<{ nodes: ClusterNode[] }>(
          `/api/v1/clusters/${clusterId}/nodes`,
        );
        if (!isMounted) return;
        const fetchedNodes = data.nodes ?? [];
        setNodes(fetchedNodes);

        const current = fetchedNodes.find((n) => n.id === nodeId);
        if (!current) {
          const fallback =
            fetchedNodes.find((n) => n.status === "running") ??
            fetchedNodes[0];
          if (fallback) {
            navigate(`/terminal/${clusterId}/${fallback.id}`, {
              replace: true,
            });
          }
        }
      } catch (err) {
        if (isMounted) {
          setError(
            err instanceof Error ? err.message : "Failed to load nodes",
          );
        }
      } finally {
        if (isMounted) setLoading(false);
      }
    };

    load();
    return () => {
      isMounted = false;
    };
  }, [onRoute, clusterId, nodeId, navigate]);

  // Register the node named in the URL as an open session once we know
  // enough about it (its ClusterNode + cluster name) — this is what keeps
  // it alive in the background once the user navigates away from it.
  useEffect(() => {
    if (!onRoute || !clusterId || !nodeId) return;
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;

    const clusterName =
      clusters.find((c) => c.id === clusterId)?.name ?? clusterId;
    registerSession(clusterId, clusterName, node);
  }, [onRoute, clusterId, nodeId, nodes, clusters, registerSession]);

  function handleCloseSession(closedNodeId: string) {
    const remaining = sessions.filter((s) => s.node.id !== closedNodeId);
    closeSession(closedNodeId);
    if (closedNodeId === nodeId) {
      const next = remaining[remaining.length - 1];
      if (next) {
        navigate(`/terminal/${next.clusterId}/${next.node.id}`, {
          replace: true,
        });
      } else if (clusterId) {
        navigate(`/clusters/${clusterId}`);
      }
    }
  }

  const activeNode = nodes.find((n) => n.id === nodeId);

  return (
    <div
      // `invisible` (visibility:hidden), not `hidden` (display:none): the
      // latter would collapse this whole subtree to 0x0 while off-route,
      // which TerminalPane's ResizeObserver would see as a resize and
      // (were it not for the activeRef guard already keyed off `active`
      // going false at the same time) risk the same buffer-garbling bug
      // that guard was added for. `invisible` keeps the box viewport-sized
      // always, so nothing here ever actually resizes just from navigating.
      className={cn(
        "fixed inset-0 z-40 flex h-screen w-full flex-col bg-background",
        !onRoute && "invisible pointer-events-none",
      )}
      aria-hidden={!onRoute}
    >
      {onRoute && status === "loading" && <FullPageSpinner />}
      {onRoute && status !== "loading" && status !== "authenticated" && (
        <Navigate to="/login" replace />
      )}
      {onRoute && status === "authenticated" && (!clusterId || !nodeId) && (
        <Navigate to="/clusters" replace />
      )}

      {onRoute && status === "authenticated" && clusterId && nodeId && (
        <header className="flex shrink-0 flex-col gap-2 border-b bg-card px-4 py-3">
          <div className="flex flex-wrap items-center gap-3">
            <Link
              to={`/clusters/${clusterId}`}
              className="inline-flex items-center gap-1 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground"
            >
              <ArrowLeft className="h-4 w-4" />
              Back
            </Link>

            <Select
              value={clusterId}
              onValueChange={(next) => {
                if (next === clusterId) return;
                navigate(`/terminal/${next}/_`, { replace: true });
              }}
            >
              <SelectTrigger className="w-[220px]">
                <SelectValue placeholder="Select a cluster" />
              </SelectTrigger>
              <SelectContent>
                {clusters.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>

            <div className="flex flex-1 flex-wrap items-center gap-1">
              {nodes.map((node) => {
                const isActive = node.id === nodeId;
                const isRunning = node.status === "running";
                return (
                  <Button
                    key={node.id}
                    variant={isActive ? "secondary" : "ghost"}
                    size="sm"
                    disabled={!isRunning}
                    title={
                      !isRunning
                        ? "Node must be running to open a terminal"
                        : undefined
                    }
                    onClick={() =>
                      navigate(`/terminal/${clusterId}/${node.id}`, {
                        replace: true,
                      })
                    }
                    className={cn(
                      "gap-1.5",
                      isActive && "pointer-events-none",
                    )}
                  >
                    {node.name}
                    <Badge
                      variant="outline"
                      className="text-[10px] capitalize"
                    >
                      {node.role}
                    </Badge>
                  </Button>
                );
              })}
            </div>
          </div>

          {(sessions.length > 1 ||
            sessions.some(
              (s) => s.status === "error" || s.status === "closed",
            )) && (
            <div className="flex flex-wrap items-center gap-1.5 border-t pt-2">
              <span className="text-xs uppercase tracking-wide text-muted-foreground">
                Open sessions
              </span>
              {sessions.map((session) => {
                const isActive = session.node.id === nodeId;
                const isDead =
                  session.status === "error" || session.status === "closed";
                return (
                  <div
                    key={session.node.id}
                    className={cn(
                      "flex items-center gap-1 rounded-md border pl-2 pr-1 py-0.5 text-xs",
                      isActive
                        ? "border-primary/40 bg-primary/10"
                        : "border-transparent bg-muted",
                      isDead && "border-destructive/40",
                    )}
                  >
                    <span
                      aria-hidden
                      title={
                        session.status === "connected"
                          ? "Connected"
                          : session.status === "connecting"
                            ? "Connecting…"
                            : session.status === "error"
                              ? "Connection error"
                              : "Session closed"
                      }
                      className={cn(
                        "h-1.5 w-1.5 shrink-0 rounded-full",
                        session.status === "connected" && "bg-emerald-500",
                        session.status === "connecting" &&
                          "animate-pulse bg-amber-500",
                        isDead && "bg-destructive",
                      )}
                    />
                    <button
                      type="button"
                      className={cn(
                        "text-foreground hover:underline",
                        isDead && "text-muted-foreground",
                      )}
                      onClick={() =>
                        navigate(
                          `/terminal/${session.clusterId}/${session.node.id}`,
                          { replace: true },
                        )
                      }
                    >
                      {session.clusterName} / {session.node.name}
                    </button>
                    {isDead && (
                      <button
                        type="button"
                        aria-label={`Reconnect session on ${session.node.name}`}
                        title="Reconnect"
                        className="rounded-sm p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
                        onClick={() => reconnectSession(session.node.id)}
                      >
                        <RotateCw className="h-3 w-3" />
                      </button>
                    )}
                    <button
                      type="button"
                      aria-label={`Close session on ${session.node.name}`}
                      title="Close session"
                      className="rounded-sm p-0.5 text-muted-foreground hover:bg-muted hover:text-foreground"
                      onClick={() => handleCloseSession(session.node.id)}
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </div>
                );
              })}
            </div>
          )}
        </header>
      )}

      {/* Always rendered (never conditionally excluded) so the
          TerminalPanes below — and the websocket/xterm state they hold —
          are never unmounted just because we're off the terminal route. */}
      <main className="relative min-h-0 flex-1 p-3">
        {onRoute && error && (
          <Alert variant="destructive" className="mb-3">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}

        {sessions.map((session) => (
          <TerminalPane
            // generation changes on reconnectSession, forcing a full
            // remount (fresh WebSocket) instead of reusing the dead one.
            key={`${session.node.id}:${session.generation}`}
            clusterId={session.clusterId}
            nodeId={session.node.id}
            active={onRoute && session.node.id === nodeId}
            onStatusChange={(next) => setSessionStatus(session.node.id, next)}
          />
        ))}

        {onRoute && loading && !activeNode && (
          <Skeleton className="h-full w-full rounded-lg" />
        )}
        {onRoute && !loading && !activeNode && sessions.length === 0 && (
          <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
            No running nodes in this cluster.
          </div>
        )}
      </main>
    </div>
  );
}
