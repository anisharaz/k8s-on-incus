// Types matching the backend API exactly — see API.md at the repo root.

export type UserRole = "admin" | "user";

export interface User {
  id: string;
  username: string;
  role: UserRole;
  createdAt: string;
  updatedAt: string;
}

export type ClusterNetworkStatus = "creating" | "ready" | "failed";

export interface ClusterNetwork {
  id: string;
  ownerId: string;
  name: string;
  incusName: string;
  cidr: string;
  gateway: string;
  status: ClusterNetworkStatus;
  message?: string;
  createdAt: string;
  updatedAt: string;
}

export type ClusterStatus = "creating" | "ready" | "failed" | "deleting";

// The only implemented value today; more may be added later.
export type CNIType = "cilium";

export interface Cluster {
  id: string;
  ownerId: string;
  networkId: string;
  jobId?: string;
  name: string;
  cni: CNIType;
  status: ClusterStatus;
  message?: string;
  createdAt: string;
  updatedAt: string;
}

export type NodeRole = "master" | "worker";
export type NodeStatus = "creating" | "running" | "stopped" | "failed" | "deleting";

// Named ClusterNode (not "Node") to avoid colliding with the DOM Node type.
export interface ClusterNode {
  id: string;
  clusterId: string;
  jobId: string;
  name: string;
  incusName: string;
  role: NodeRole;
  status: NodeStatus;
  ip?: string;
  message?: string;
  /** Random password set for the image's "ubuntu" user during
   * provisioning (see be/internal/jobs/node.go). Empty if provisioning
   * never got far enough to set it, or the SSH setup step itself failed. */
  sshPassword?: string;
  createdAt: string;
  updatedAt: string;
}

export type JobStatus = "queued" | "running" | "succeeded" | "failed";

export interface Job {
  id: string;
  ownerId: string;
  type: string;
  name?: string;
  status: JobStatus;
  progress: number;
  stage?: string;
  message?: string;
  result?: Record<string, unknown>;
  error?: string;
  metadata?: Record<string, string>;
  createdAt: string;
  updatedAt: string;
  completedAt?: string;
}

export interface AuthStatusResponse {
  adminCreated: boolean;
}

// Request payloads

export interface CreateNetworkInput {
  name: string;
  cidr?: string;
}

export interface CreateClusterInput {
  networkId: string;
  name: string;
  cni?: CNIType;
  cpu?: number;
  memory?: string;
  disk?: string;
}

export interface CreateNodeInput {
  cpu?: number;
  memory?: string;
  disk?: string;
}

export interface LoginInput {
  username: string;
  password: string;
}

export interface RegisterAdminInput {
  username: string;
  password: string;
}

export interface CreateUserInput {
  username: string;
  password: string;
}
