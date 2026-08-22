import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import {
  ArrowLeft,
  Download,
  Loader2,
  ServerCog,
  AlertCircle,
  Terminal as TerminalIcon,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertTitle, AlertDescription } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { AddNodeDialog } from "@/components/AddNodeDialog";
import { SSHInfoDialog } from "@/components/SSHInfoDialog";
import { api, ApiError } from "@/lib/api";
import type {
  Cluster,
  ClusterNode,
  ClusterStatus,
  Job,
  NodeStatus,
} from "@/lib/types";

const clusterStatusVariant: Record<
  ClusterStatus,
  "default" | "secondary" | "destructive" | "outline"
> = {
  creating: "secondary",
  ready: "default",
  failed: "destructive",
  deleting: "outline",
};

const nodeStatusVariant: Record<
  NodeStatus,
  "default" | "secondary" | "destructive" | "outline"
> = {
  creating: "secondary",
  running: "default",
  stopped: "outline",
  failed: "destructive",
  deleting: "outline",
};

const jobTypeTitle: Record<string, string> = {
  node_provision: "Creation Progress",
  cluster_deletion: "Deletion Progress",
  node_deletion: "Deletion Progress",
};

export function ClusterDetail() {
  const { clusterId } = useParams();
  const navigate = useNavigate();
  const [cluster, setCluster] = useState<Cluster | null>(null);
  const [nodes, setNodes] = useState<ClusterNode[]>([]);
  const [nodeJobs, setNodeJobs] = useState<Record<string, Job>>({});
  const [clusterJob, setClusterJob] = useState<Job | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const lastClusterStatus = useRef<ClusterStatus | null>(null);

  useEffect(() => {
    if (!clusterId) return;
    let isMounted = true;

    const fetchAll = async () => {
      try {
        setError(null);
        const [clusterData, nodesData] = await Promise.all([
          api.get<{ cluster: Cluster }>(`/api/v1/clusters/${clusterId}`),
          api.get<{ nodes: ClusterNode[] }>(
            `/api/v1/clusters/${clusterId}/nodes`,
          ),
        ]);
        if (!isMounted) return;
        lastClusterStatus.current = clusterData.cluster.status;
        setCluster(clusterData.cluster);
        setNodes(nodesData.nodes ?? []);

        if (
          clusterData.cluster.jobId &&
          clusterData.cluster.status === "deleting"
        ) {
          try {
            const jobData = await api.get<{ job: Job }>(
              `/api/v1/jobs/${clusterData.cluster.jobId}`,
            );
            if (isMounted) setClusterJob(jobData.job);
          } catch {
            // ignore — the progress card just won't update this tick
          }
        } else if (isMounted) {
          setClusterJob(undefined);
        }

        // Only nodes still provisioning or being deleted need their job
        // polled for stage/progress — a finished node's outcome is fully
        // captured by its own status/message already.
        const activeNodes = (nodesData.nodes ?? []).filter(
          (n) => n.status === "creating" || n.status === "deleting",
        );
        const jobEntries = await Promise.all(
          activeNodes.map(async (node) => {
            try {
              const jobData = await api.get<{ job: Job }>(
                `/api/v1/jobs/${node.jobId}`,
              );
              return [node.id, jobData.job] as const;
            } catch {
              return null;
            }
          }),
        );
        if (!isMounted) return;
        setNodeJobs((prev) => {
          const next = { ...prev };
          for (const entry of jobEntries) {
            if (entry) next[entry[0]] = entry[1];
          }
          return next;
        });
      } catch (err) {
        if (!isMounted) return;
        if (
          err instanceof ApiError &&
          err.code === 404 &&
          lastClusterStatus.current === "deleting"
        ) {
          toast.success("Cluster deleted");
          navigate("/clusters");
          return;
        }
        setError(
          err instanceof Error ? err.message : "Failed to fetch cluster",
        );
      } finally {
        if (isMounted) setLoading(false);
      }
    };

    fetchAll();
    const interval = setInterval(fetchAll, 3000);
    return () => {
      isMounted = false;
      clearInterval(interval);
    };
  }, [clusterId, navigate]);

  if (loading) {
    return (
      <div className="rounded-3xl border bg-card p-6 shadow-sm space-y-4">
        <Skeleton className="h-8 w-1/3" />
        <Skeleton className="h-4 w-1/2" />
      </div>
    );
  }

  if (error || !cluster) {
    return (
      <div className="rounded-3xl border bg-card p-6 shadow-sm">
        <p className="text-sm uppercase tracking-[0.2em] text-muted-foreground">
          Cluster
        </p>
        <h2 className="mt-2 text-2xl font-semibold text-foreground">
          Cluster not found
        </h2>
        <p className="mt-2 text-muted-foreground">
          {error ||
            "The cluster id you opened does not match any known cluster."}
        </p>
        <Link
          to="/clusters"
          className="mt-6 inline-flex items-center gap-2 rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
        >
          <ArrowLeft className="h-4 w-4" />
          Back to clusters
        </Link>
      </div>
    );
  }

  const master = nodes.find((n) => n.role === "master");
  const masterJob = master ? nodeJobs[master.id] : undefined;
  const canAddWorker =
    cluster.status === "ready" && master?.status === "running";
  const isCreatingCluster = cluster.status === "creating";
  const isDeletingCluster = cluster.status === "deleting";
  const activeJob = clusterJob ?? masterJob;

  async function handleDeleteCluster() {
    if (!cluster) return;
    try {
      await api.delete(`/api/v1/clusters/${cluster.id}`);
      toast.success("Cluster deletion started");
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : "Failed to delete cluster",
      );
    }
  }

  async function handleDeleteNode(node: ClusterNode) {
    if (!cluster) return;
    try {
      await api.delete(`/api/v1/clusters/${cluster.id}/nodes/${node.id}`);
      toast.success(`Deleting "${node.name}" started`);
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : "Failed to delete node",
      );
    }
  }

  return (
    <div className="space-y-6">
      <Link
        to="/clusters"
        className="inline-flex items-center gap-2 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" />
        Back to clusters
      </Link>

      <section className="rounded-3xl border bg-card p-6 shadow-sm">
        <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
          <div>
            <div className="mb-3 inline-flex items-center gap-2 rounded-full bg-muted px-3 py-1 text-xs font-medium uppercase tracking-[0.2em] text-muted-foreground">
              <ServerCog className="h-3.5 w-3.5" />
              Cluster detail
            </div>
            <h2 className="text-3xl font-semibold tracking-tight text-foreground">
              {cluster.name}
            </h2>
            <p className="mt-2 text-sm text-muted-foreground">
              Cluster ID: {cluster.id}
            </p>
          </div>
          <div className="flex flex-col items-start gap-3 md:items-end">
            <div
              className={cn(
                "relative rounded-2xl bg-muted px-4 py-3 text-sm text-muted-foreground",
                isCreatingCluster && "ring-1 ring-primary/40",
              )}
            >
              {isCreatingCluster && (
                <span className="pointer-events-none absolute inset-0 -z-10 animate-pulse rounded-2xl bg-primary/10 blur-md" />
              )}
              <p className="flex items-center gap-2 font-medium text-foreground">
                Status:
                <Badge
                  variant={clusterStatusVariant[cluster.status]}
                  className="gap-1 capitalize"
                >
                  {isCreatingCluster && (
                    <Loader2 className="h-3 w-3 animate-spin" />
                  )}
                  {cluster.status}
                </Badge>
              </p>
              {master?.ip && <p className="mt-1">Master IP: {master.ip}</p>}
            </div>
            <div className="flex items-center gap-2">
              {master?.status === "running" ? (
                <Button variant="outline" size="sm" asChild>
                  <a
                    href={`/api/v1/clusters/${cluster.id}/kubeconfig`}
                    download={`${cluster.name}-kubeconfig.yaml`}
                  >
                    <Download className="mr-2 h-4 w-4" />
                    Download kubeconfig
                  </a>
                </Button>
              ) : (
                <Button
                  variant="outline"
                  size="sm"
                  disabled
                  title="The master must be running to fetch its kubeconfig"
                >
                  <Download className="mr-2 h-4 w-4" />
                  Download kubeconfig
                </Button>
              )}
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button
                  variant="destructive"
                  size="sm"
                  disabled={isCreatingCluster || isDeletingCluster}
                  title={
                    isCreatingCluster
                      ? "Wait for the cluster to finish creating before deleting it"
                      : undefined
                  }
                >
                  <Trash2 className="mr-2 h-4 w-4" />
                  Delete cluster
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>
                    Delete cluster "{cluster.name}"?
                  </AlertDialogTitle>
                  <AlertDialogDescription>
                    This destroys every node's VM, including the master. This
                    can't be undone.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction onClick={handleDeleteCluster}>
                    Delete
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
            </div>
          </div>
        </div>

        <div className="mt-6 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <DetailCard label="Status" value={cluster.status} />
          <DetailCard label="CNI" value={cluster.cni} />
          <DetailCard
            label="Created"
            value={new Date(cluster.createdAt).toLocaleString()}
          />
          {master?.ip && <DetailCard label="Master IP" value={master.ip} />}
          <DetailCard
            label="Last Updated"
            value={new Date(cluster.updatedAt).toLocaleString()}
          />
        </div>

        {cluster.message && (
          <Alert className="mt-6">
            <AlertDescription>{cluster.message}</AlertDescription>
          </Alert>
        )}
      </section>

      {activeJob && activeJob.status !== "succeeded" && (
        <section className="rounded-3xl border bg-card p-6 shadow-sm">
          <h3 className="text-lg font-semibold text-foreground mb-4">
            {jobTypeTitle[activeJob.type] ?? "Job Progress"}
          </h3>

          <div className="space-y-4">
            <div>
              <div className="flex items-center justify-between mb-2">
                <span className="text-sm font-medium text-muted-foreground">
                  {activeJob.stage}
                </span>
                <span className="text-sm font-medium text-foreground">
                  {activeJob.progress}%
                </span>
              </div>
              <Progress value={activeJob.progress} />
            </div>

            <div>
              <p className="text-sm text-muted-foreground font-medium">
                Status
              </p>
              <p className="mt-1 text-sm text-foreground capitalize">
                {activeJob.status}
              </p>
            </div>

            <div>
              <p className="text-sm text-muted-foreground font-medium">
                Message
              </p>
              <p className="mt-1 text-sm text-foreground">
                {activeJob.message}
              </p>
            </div>

            {activeJob.error && (
              <Alert variant="destructive">
                <AlertCircle className="h-4 w-4" />
                <AlertTitle>Operation failed</AlertTitle>
                <AlertDescription className="whitespace-pre-wrap wrap-break-word">
                  {activeJob.error}
                </AlertDescription>
              </Alert>
            )}
          </div>
        </section>
      )}

      <section className="rounded-3xl border bg-card p-6 shadow-sm">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-lg font-semibold text-foreground">Nodes</h3>
          <AddNodeDialog
            clusterId={cluster.id}
            disabled={!canAddWorker}
            disabledReason={
              !canAddWorker
                ? "The cluster's master must be ready and running first"
                : undefined
            }
          />
        </div>

        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>IP</TableHead>
              <TableHead>Message</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {nodes.map((node) => {
              const job = nodeJobs[node.id];
              const nodeBusy =
                node.status === "creating" || node.status === "deleting";
              const canDeleteNode = !nodeBusy && master?.status === "running";
              return (
                <TableRow key={node.id}>
                  <TableCell className="font-medium">{node.name}</TableCell>
                  <TableCell>
                    <Badge variant="outline" className="capitalize">
                      {node.role}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-col gap-1">
                      <Badge
                        variant={nodeStatusVariant[node.status]}
                        className="w-fit gap-1 capitalize"
                      >
                        {nodeBusy && <Loader2 className="h-3 w-3 animate-spin" />}
                        {node.status}
                      </Badge>
                      {nodeBusy && job && (
                        <span className="text-xs text-muted-foreground">
                          {job.stage} ({job.progress}%)
                        </span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>{node.ip || "—"}</TableCell>
                  <TableCell className="max-w-xs truncate text-sm text-muted-foreground">
                    {node.message}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex items-center justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      disabled={node.status !== "running"}
                      title={
                        node.status !== "running"
                          ? "The node must be running to open a terminal"
                          : `Open terminal on ${node.name}`
                      }
                      aria-label={`Open terminal on ${node.name}`}
                      onClick={() =>
                        navigate(`/terminal/${cluster.id}/${node.id}`)
                      }
                    >
                      <TerminalIcon className="h-4 w-4" />
                    </Button>
                    <SSHInfoDialog node={node} />
                    {node.role === "master" ? null : (
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="Delete node"
                            disabled={!canDeleteNode}
                            title={
                              !canDeleteNode
                                ? "The master must be running and the node must not already have an operation in progress"
                                : undefined
                            }
                          >
                            <Trash2 className="h-4 w-4 text-destructive" />
                          </Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>
                              Delete node "{node.name}"?
                            </AlertDialogTitle>
                            <AlertDialogDescription>
                              This drains the node and destroys its VM. This
                              can't be undone.
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>Cancel</AlertDialogCancel>
                            <AlertDialogAction
                              onClick={() => handleDeleteNode(node)}
                            >
                              Delete
                            </AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    )}
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </section>
    </div>
  );
}

function DetailCard({ label, value }: { label: string; value: string }) {
  return (
    <Card size="sm">
      <CardContent>
        <p className="text-xs uppercase tracking-wide text-muted-foreground">
          {label}
        </p>
        <p className="mt-2 text-base font-medium text-foreground">{value}</p>
      </CardContent>
    </Card>
  );
}
