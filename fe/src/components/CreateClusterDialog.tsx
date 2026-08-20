import { useEffect, useState } from "react";
import { useNavigate, Link } from "react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import * as z from "zod";
import { Plus, ChevronDown, ChevronRight } from "lucide-react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/components/ui/form";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { SizeInput } from "@/components/SizeInput";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { api, ApiError } from "@/lib/api";
import type { Cluster, ClusterNetwork } from "@/lib/types";

const sizeRegex = /^\d+(B|kB|MB|GB|TB|PB|EB|KiB|MiB|GiB|TiB|PiB|EiB)$/;

const formSchema = z.object({
  name: z
    .string()
    .min(1, "Name is required")
    .max(63, "Name must be at most 63 characters"),
  networkId: z.string().min(1, "Select a network"),
  cni: z.string().min(1, "Select a CNI"),
  cpu: z
    .string()
    .optional()
    .refine((v) => !v || (/^\d+$/.test(v) && Number(v) >= 2), {
      message: "Must be a whole number, at least 2",
    }),
  memory: z
    .string()
    .optional()
    .refine((v) => !v || sizeRegex.test(v), {
      message: 'Must look like "2GiB" or "1700MB"',
    }),
  disk: z
    .string()
    .optional()
    .refine((v) => !v || sizeRegex.test(v), {
      message: 'Must look like "20GiB"',
    }),
});

type FormValues = z.infer<typeof formSchema>;

interface CreateClusterDialogProps {
  onSuccess?: () => void;
}

export function CreateClusterDialog({ onSuccess }: CreateClusterDialogProps) {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [networks, setNetworks] = useState<ClusterNetwork[]>([]);
  const [networksLoading, setNetworksLoading] = useState(false);

  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: {
      name: "",
      networkId: "",
      cni: "cilium",
      cpu: "",
      memory: "",
      disk: "",
    },
  });

  useEffect(() => {
    if (!open) return;
    let isMounted = true;
    const loadNetworks = async () => {
      setNetworksLoading(true);
      try {
        const data = await api.get<{ networks: ClusterNetwork[] }>(
          "/api/v1/networks",
        );
        if (!isMounted) return;
        const fetchedNetworks = data.networks ?? [];
        setNetworks(fetchedNetworks);

        // Every user gets a "default" network auto-created on account
        // creation (see be/internal/handlers/networks.go's
        // createDefaultNetwork) — pre-select it so the common case needs
        // no extra click, without clobbering a choice the user already made.
        if (!form.getValues("networkId")) {
          const defaultNetwork = fetchedNetworks.find(
            (n) => n.name === "default",
          );
          if (defaultNetwork) form.setValue("networkId", defaultNetwork.id);
        }
      } catch {
        if (isMounted) setNetworks([]);
      } finally {
        if (isMounted) setNetworksLoading(false);
      }
    };
    loadNetworks();
    return () => {
      isMounted = false;
    };
  }, [open, form]);

  async function onSubmit(values: FormValues) {
    setIsLoading(true);
    try {
      const result = await api.post<{ cluster: Cluster }>("/api/v1/clusters", {
        networkId: values.networkId,
        name: values.name,
        cni: values.cni,
        ...(values.cpu ? { cpu: Number(values.cpu) } : {}),
        ...(values.memory ? { memory: values.memory } : {}),
        ...(values.disk ? { disk: values.disk } : {}),
      });
      setOpen(false);
      form.reset();
      onSuccess?.();
      navigate(`/clusters/${result.cluster.id}`);
    } catch (err) {
      form.setError("root", {
        message:
          err instanceof ApiError ? err.message : "Failed to create cluster",
      });
    } finally {
      setIsLoading(false);
    }
  }

  const noNetworks = !networksLoading && networks.length === 0;

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          form.reset();
          setShowAdvanced(false);
        }
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <Plus className="mr-2 h-4 w-4" />
          Create Cluster
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-106.25">
        <DialogHeader>
          <DialogTitle>Create New Cluster</DialogTitle>
          <DialogDescription>
            Launches a master node on the chosen network. Cluster creation runs
            in the background.
          </DialogDescription>
        </DialogHeader>

        <Form {...form}>
          <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
            {form.formState.errors.root && (
              <Alert variant="destructive">
                <AlertDescription>
                  {form.formState.errors.root.message}
                </AlertDescription>
              </Alert>
            )}

            <FormField
              control={form.control}
              name="name"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Cluster Name</FormLabel>
                  <FormControl>
                    <Input
                      placeholder="e.g., k8s-prod"
                      disabled={isLoading}
                      {...field}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name="networkId"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>Network</FormLabel>
                  <Select
                    onValueChange={field.onChange}
                    value={field.value}
                    disabled={isLoading || networksLoading || noNetworks}
                  >
                    <FormControl>
                      <SelectTrigger className="w-full">
                        <SelectValue
                          placeholder={
                            networksLoading
                              ? "Loading networks..."
                              : "Select a network"
                          }
                        />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent position="popper">
                      {networks.map((network) => (
                        <SelectItem key={network.id} value={network.id}>
                          {network.name} ({network.cidr})
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {noNetworks ? (
                    <FormDescription>
                      You don't have any networks yet.{" "}
                      <Link
                        to="/networks"
                        className="underline underline-offset-2"
                        onClick={() => setOpen(false)}
                      >
                        Create one first
                      </Link>
                      .
                    </FormDescription>
                  ) : (
                    <FormMessage />
                  )}
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name="cni"
              render={({ field }) => (
                <FormItem>
                  <FormLabel>CNI</FormLabel>
                  <Select
                    onValueChange={field.onChange}
                    value={field.value}
                    disabled={isLoading}
                  >
                    <FormControl>
                      <SelectTrigger className="w-full">
                        <SelectValue placeholder="Select a CNI" />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      <SelectItem value="cilium">Cilium</SelectItem>
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    Cilium is currently the only supported CNI.
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <button
              type="button"
              className="flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground"
              onClick={() => setShowAdvanced((v) => !v)}
            >
              {showAdvanced ? (
                <ChevronDown className="h-3.5 w-3.5" />
              ) : (
                <ChevronRight className="h-3.5 w-3.5" />
              )}
              Advanced (CPU / memory / disk)
            </button>

            {showAdvanced && (
              <div className="space-y-4 rounded-lg border p-3">
                <FormField
                  control={form.control}
                  name="cpu"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>CPU (vCPUs)</FormLabel>
                      <FormControl>
                        <Input
                          placeholder="Default: 2, minimum 2"
                          disabled={isLoading}
                          {...field}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="memory"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Memory</FormLabel>
                      <FormControl>
                        <SizeInput
                          placeholder="2"
                          disabled={isLoading}
                          value={field.value ?? ""}
                          onChange={field.onChange}
                          onBlur={field.onBlur}
                          name={field.name}
                        />
                      </FormControl>
                      <FormDescription>
                        Default 2 GiB, minimum 1700 MB.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name="disk"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Disk</FormLabel>
                      <FormControl>
                        <SizeInput
                          placeholder="20"
                          disabled={isLoading}
                          value={field.value ?? ""}
                          onChange={field.onChange}
                          onBlur={field.onBlur}
                          name={field.name}
                        />
                      </FormControl>
                      <FormDescription>
                        Default 20 GiB, minimum 20 GiB.
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>
            )}

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setOpen(false)}
                disabled={isLoading}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={isLoading || noNetworks}>
                {isLoading ? "Creating..." : "Create Cluster"}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  );
}
