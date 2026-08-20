import { useEffect, useMemo, useState } from "react";
import { Navigate } from "react-router";
import { Users as UsersIcon, AlertCircle, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { CreateUserDialog } from "@/components/CreateUserDialog";
import { api, ApiError } from "@/lib/api";
import { useAuth, useJobs } from "@/context";
import type { User } from "@/lib/types";

export function Users() {
  const { user: currentUser } = useAuth();
  const { activeJobs } = useJobs();
  const [users, setUsers] = useState<User[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchUsers = async (silent = false) => {
    try {
      if (!silent) setLoading(true);
      setError(null);
      const data = await api.get<{ users: User[] }>("/api/v1/users");
      setUsers(data.users ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to fetch users");
    } finally {
      if (!silent) setLoading(false);
    }
  };

  useEffect(() => {
    // No polling — users are created rarely and only by an admin action in
    // this same UI, so a manual refetch after creating one is enough.
    let isMounted = true;
    const load = async () => {
      if (isMounted) await fetchUsers();
    };
    load();
    return () => {
      isMounted = false;
    };
  }, []);

  // Deleting a user runs as a background job (it tears down every VM/
  // network they own first) — track which users currently have one in
  // flight via the same job list JobProvider already polls every 3s
  // app-wide, and silently resync the table once it's no longer active so
  // the row disappears on its own instead of needing a manual refresh.
  const deletingUserIds = useMemo(
    () =>
      new Set(
        activeJobs
          .filter((j) => j.type === "user_deletion")
          .map((j) => j.metadata?.userId)
          .filter((id): id is string => Boolean(id)),
      ),
    [activeJobs],
  );

  useEffect(() => {
    const resync = async () => {
      if (deletingUserIds.size > 0) await fetchUsers(true);
    };
    resync();
  }, [deletingUserIds]);

  async function handleDeleteUser(user: User) {
    try {
      await api.delete(`/api/v1/users/${user.id}`);
      toast.success(`Deleting "${user.username}" and everything they own`);
      fetchUsers(true);
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : "Failed to delete user",
      );
    }
  }

  // Backend already 403s non-admins, but redirect before even trying —
  // there's nothing useful to show a regular user on this page.
  if (currentUser?.role !== "admin") {
    return <Navigate to="/clusters" replace />;
  }

  return (
    <div className="space-y-6">
      <section className="rounded-3xl border bg-card p-6 shadow-sm">
        <div className="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
          <div>
            <div className="mb-3 inline-flex items-center gap-2 rounded-full bg-muted px-3 py-1 text-xs font-medium uppercase tracking-[0.2em] text-muted-foreground">
              <UsersIcon className="h-3.5 w-3.5" />
              Users
            </div>
            <h2 className="text-3xl font-semibold tracking-tight text-foreground">
              User accounts
            </h2>
            <p className="mt-2 max-w-2xl text-sm text-muted-foreground">
              Regular users you've created. Each owns their own networks,
              clusters, and nodes, separate from everyone else's.
            </p>
          </div>
          <CreateUserDialog onSuccess={() => fetchUsers()} />
        </div>
      </section>

      {error && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {loading ? (
        <div className="space-y-2">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : users.length === 0 ? (
        <div className="rounded-3xl border bg-card p-12 text-center">
          <p className="text-sm text-muted-foreground">No users yet.</p>
          <p className="mt-2 text-sm text-muted-foreground">
            Click "Create User" to add one.
          </p>
        </div>
      ) : (
        <div className="rounded-3xl border bg-card p-2 shadow-sm">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Username</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {users.map((user) => {
                const isDeleting = deletingUserIds.has(user.id);
                return (
                  <TableRow key={user.id}>
                    <TableCell className="font-medium">
                      {user.username}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={
                          user.role === "admin" ? "default" : "secondary"
                        }
                        className="capitalize"
                      >
                        {user.role}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      {new Date(user.createdAt).toLocaleDateString()}
                    </TableCell>
                    <TableCell className="text-right">
                      {user.role === "admin" ? null : isDeleting ? (
                        <Badge variant="outline" className="gap-1">
                          Deleting...
                        </Badge>
                      ) : (
                        <AlertDialog>
                          <AlertDialogTrigger asChild>
                            <Button
                              variant="destructive"
                              size="sm"
                              title={`Delete ${user.username}`}
                            >
                              <Trash2 className="h-4 w-4" />
                            </Button>
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>
                                Delete user "{user.username}"?
                              </AlertDialogTitle>
                              <AlertDialogDescription>
                                This deletes every network, cluster, and
                                node's VM this user owns, then the account
                                itself. This can't be undone.
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>Cancel</AlertDialogCancel>
                              <AlertDialogAction
                                onClick={() => handleDeleteUser(user)}
                              >
                                Delete
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}
