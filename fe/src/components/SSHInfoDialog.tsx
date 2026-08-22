import { useState } from "react";
import { Copy, Eye, EyeOff, KeyRound } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import type { ClusterNode } from "@/lib/types";

interface CopyableFieldProps {
  label: string;
  value: string;
  maskable?: boolean;
}

function CopyableField({ label, value, maskable = false }: CopyableFieldProps) {
  const [revealed, setRevealed] = useState(!maskable);

  async function handleCopy() {
    await navigator.clipboard.writeText(value);
    toast.success(`${label} copied`);
  }

  return (
    <div className="space-y-1">
      <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      <div className="flex items-center gap-1 rounded-md border bg-muted px-3 py-2">
        <code className="flex-1 truncate text-sm">
          {revealed ? value : "•".repeat(Math.min(value.length, 20))}
        </code>
        {maskable && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-6 w-6 shrink-0"
            onClick={() => setRevealed((v) => !v)}
            aria-label={revealed ? `Hide ${label}` : `Show ${label}`}
          >
            {revealed ? (
              <EyeOff className="h-3.5 w-3.5" />
            ) : (
              <Eye className="h-3.5 w-3.5" />
            )}
          </Button>
        )}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="h-6 w-6 shrink-0"
          onClick={handleCopy}
          aria-label={`Copy ${label}`}
        >
          <Copy className="h-3.5 w-3.5" />
        </Button>
      </div>
    </div>
  );
}

interface SSHInfoDialogProps {
  node: ClusterNode;
}

// A random password for the VM image's "ubuntu" user is set during
// provisioning (see be/internal/jobs/node.go's setupSSHPassword — no
// password is baked into the image itself) and stored on the node so it
// can be shown here at any time, not just once at creation.
export function SSHInfoDialog({ node }: SSHInfoDialogProps) {
  const available =
    node.status === "running" && Boolean(node.ip) && Boolean(node.sshPassword);

  const disabledReason =
    node.status !== "running"
      ? "The node must be running to SSH into it"
      : !node.sshPassword
        ? "SSH access isn't available for this node (setup may have failed)"
        : undefined;

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          disabled={!available}
          title={disabledReason ?? `SSH into ${node.name}`}
          aria-label={`SSH into ${node.name}`}
        >
          <KeyRound className="h-4 w-4" />
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>SSH into "{node.name}"</DialogTitle>
          <DialogDescription>
            A random password was generated for the "ubuntu" user when this
            node was provisioned.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          {node.ip && (
            <CopyableField label="Command" value={`ssh ubuntu@${node.ip}`} />
          )}
          <CopyableField label="Username" value="ubuntu" />
          {node.sshPassword && (
            <CopyableField label="Password" value={node.sshPassword} maskable />
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
