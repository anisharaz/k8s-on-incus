#!/bin/bash

# Trap SIGTERM so we can shut down incusd and lxcfs gracefully
trap "cleanup; exit" SIGTERM

cleanup() {
    echo "Stopping incusd..."
    # `incus admin shutdown` gracefully stops every running instance first
    # (Kubernetes VMs included), which can legitimately take a while — but
    # with no timeout at all it can also block indefinitely on a wedged
    # guest, and Docker's default 10s stop grace period would then SIGKILL
    # this whole container mid-shutdown (docker-compose.yml raises that
    # grace period to comfortably exceed this timeout). Bounding it here
    # means a stuck VM causes an ungraceful stop of just that VM after 90s,
    # not a SIGKILL of incusd itself and every VM at once.
    incus admin shutdown --timeout 90
    pkill -TERM incusd
    echo "Stopped incusd."
    
    echo "Stopping lxcfs..."
    pkill -TERM lxcfs
    fusermount -u /var/lib/incus-lxcfs
    echo "Stopped lxcfs."
    
    CHILD_PIDS=$(pgrep -P $$)
    if [ -n "$CHILD_PIDS" ]; then
        pkill -TERM -P $$
        echo "Stopped child processes with PIDs: $CHILD_PIDS"
    else
        echo "No child processes found."
    fi
}

# Map KVM GID if provided as an Environment Variable
if [ -n "$KVM_GID" ]; then
    echo "Updating container kvm group to GID $KVM_GID to match host..."
    groupmod -g "$KVM_GID" kvm || true
fi

# Environment Exports
export PATH="/opt/incus/bin/:${PATH}"
export INCUS_EDK2_PATH="/opt/incus/share/qemu/"
export LD_LIBRARY_PATH="/opt/incus/lib/"
export INCUS_LXC_TEMPLATE_CONFIG="/opt/incus/share/lxc/config/"
export INCUS_DOCUMENTATION="/opt/incus/doc/"
export INCUS_LXC_HOOK="/opt/incus/share/lxc/hooks/"
export INCUS_AGENT_PATH="/opt/incus/agent/"
export INCUS_UI="/opt/incus/ui/"

# Iptables handling
if [ "$SETIPTABLES" = "true" ]; then
    if ! iptables-legacy -C DOCKER-USER -j ACCEPT &>/dev/null; then
        iptables-legacy -I DOCKER-USER -j ACCEPT
    fi
    if ! ip6tables-legacy -C DOCKER-USER -j ACCEPT &>/dev/null; then
        ip6tables-legacy -I DOCKER-USER -j ACCEPT
    fi
    if ! iptables -C DOCKER-USER -j ACCEPT &>/dev/null; then
        iptables -I DOCKER-USER -j ACCEPT
    fi
    if ! ip6tables -C DOCKER-USER -j ACCEPT &>/dev/null; then
        ip6tables -I DOCKER-USER -j ACCEPT
    fi
fi

# Start services
mkdir -p /var/lib/incus-lxcfs
/opt/incus/bin/lxcfs /var/lib/incus-lxcfs --enable-loadavg --enable-cfs &
/usr/lib/systemd/systemd-udevd &
UDEVD_PID=$!
/opt/incus/bin/incusd &

# Wait for incusd to become ready before running the preseed. Checking for
# the socket file's existence isn't enough: incusd creates/binds it early in
# startup, well before it's actually serving API requests, so a preseed
# launched right after the file appears can still hit "connection refused"
# and fail (harmless — the marker is only set on success, so it retries on
# next start — but it needlessly delays first-boot readiness). Probing the
# API directly waits for the daemon to actually be up.
echo "Waiting for incusd to become ready..."
for i in $(seq 1 60); do
    if incus query /1.0 >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

# Share the Incus API socket with sibling containers via a shared volume.
# Proxied through socat so the daemon sees root peer credentials and the
# incus-admin/root authorization check passes for any client.
if [ -d /shared-socket ]; then
    echo "Starting socket proxy at /shared-socket/incus.sock..."
    socat UNIX-LISTEN:/shared-socket/incus.sock,fork,unlink-early,reuseaddr \
    UNIX-CONNECT:/var/lib/incus/unix.socket &
fi

# Complete the initial setup via preseed (only once).
# The marker file lives in /var/lib/incus so, with a persistent volume,
# it survives container restarts and recreates.
INIT_MARKER="/var/lib/incus/.preseed-done"
if [ -f "$INIT_MARKER" ]; then
    echo "Incus already initialized — skipping preseed."
else
    echo "Running incus admin init --preseed to complete setup..."
    if incus admin init --preseed < /incus_admin_config.yaml; then
        touch "$INIT_MARKER"
        echo "Preseed completed successfully."
    else
        echo "Preseed failed — will retry on next start."
    fi
fi

# Import the prebuilt k8s VM image (baked into the image at /incus-images),
# if it isn't already present. Checked independently of the preseed marker
# above — not folded into that one-time block — so a failed import (disk
# full, corrupt data, an I/O hiccup) is retried on every subsequent start
# instead of being silently skipped forever just because the marker above
# already exists from a prior successful preseed.
if incus image alias list -c a --format csv 2>/dev/null | grep -qx k8s; then
    echo "k8s VM image already imported — skipping."
elif [ -f /incus-images/incus.tar.xz ] && [ -f /incus-images/disk.qcow2 ]; then
    echo "Importing k8s VM image (alias 'k8s')..."
    if incus image import /incus-images/incus.tar.xz /incus-images/disk.qcow2 --alias k8s; then
        echo "Imported VM image with alias 'k8s'."
    else
        echo "WARNING: Failed to import VM image — will retry on next start. You can also import it manually:"
        echo "  incus image import /incus-images/incus.tar.xz /incus-images/disk.qcow2 --alias k8s"
    fi
else
    echo "VM image files not found in image — skipping import."
fi

# Keep the container alive
sleep infinity