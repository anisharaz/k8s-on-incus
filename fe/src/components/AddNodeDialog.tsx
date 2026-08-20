import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import * as z from "zod";
import { Plus } from "lucide-react";
import { toast } from "sonner";

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
import { api, ApiError } from "@/lib/api";
import type { ClusterNode } from "@/lib/types";

const sizeRegex = /^\d+(B|kB|MB|GB|TB|PB|EB|KiB|MiB|GiB|TiB|PiB|EiB)$/;

const formSchema = z.object({
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

interface AddNodeDialogProps {
  clusterId: string;
  disabled?: boolean;
  disabledReason?: string;
  onSuccess?: (node: ClusterNode) => void;
}

export function AddNodeDialog({
  clusterId,
  disabled,
  disabledReason,
  onSuccess,
}: AddNodeDialogProps) {
  const [open, setOpen] = useState(false);
  const [isLoading, setIsLoading] = useState(false);

  const form = useForm<FormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: { cpu: "", memory: "", disk: "" },
  });

  async function onSubmit(values: FormValues) {
    setIsLoading(true);
    try {
      const result = await api.post<{ node: ClusterNode }>(
        `/api/v1/clusters/${clusterId}/nodes`,
        {
          ...(values.cpu ? { cpu: Number(values.cpu) } : {}),
          ...(values.memory ? { memory: values.memory } : {}),
          ...(values.disk ? { disk: values.disk } : {}),
        },
      );
      setOpen(false);
      form.reset();
      toast.success("Worker node creation started");
      onSuccess?.(result.node);
    } catch (err) {
      // A 409 here means the cluster/master state changed between the
      // button becoming enabled and the request landing — a toast is
      // enough, no need to keep the dialog open on a stale form.
      if (err instanceof ApiError && err.code === 409) {
        toast.error(err.message);
        setOpen(false);
      } else {
        form.setError("root", {
          message:
            err instanceof ApiError ? err.message : "Failed to add worker",
        });
      }
    } finally {
      setIsLoading(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) form.reset();
      }}
    >
      <DialogTrigger asChild>
        <Button disabled={disabled} title={disabled ? disabledReason : undefined}>
          <Plus className="mr-2 h-4 w-4" />
          Add Worker
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-[425px]">
        <DialogHeader>
          <DialogTitle>Add Worker Node</DialogTitle>
          <DialogDescription>
            Launches a new VM and joins it to this cluster. Runs in the
            background.
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
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setOpen(false)}
                disabled={isLoading}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={isLoading}>
                {isLoading ? "Adding..." : "Add Worker"}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  );
}
