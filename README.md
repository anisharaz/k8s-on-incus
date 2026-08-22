# KOI — Kubernetes on Incus

**Spin up real Kubernetes clusters on [Incus](https://linuxcontainers.org/incus/) VMs from a web UI — no `kubeadm` babysitting, no manual CNI installs, no SSH-ing around to check on things.**

Click a button, get a cluster. Click another, get a worker. Grab your kubeconfig or drop into a root shell on any node, right from the browser.

## Features

- **One-click clusters** — pick a network and a name, and a master VM is launched, `kubeadm init`-ed, and has a CNI ([Cilium](https://cilium.io/)) installed automatically, all as a background job you can watch progress on.
- **Workers on demand** — add worker nodes to a running cluster; they fetch a join token and join on their own.
- **Networks with zero config** — let Incus auto-pick a free subnet, or specify your own CIDR.
- **Kubeconfig download** — one click to grab a ready-to-use kubeconfig for any cluster.
- **In-browser terminal** — a full xterm.js terminal into any node's shell, with multiple sessions open side by side that survive switching between them.
- **SSH access** — every node gets a random password for its `ubuntu` user at provisioning time; click the SSH button on any running node to grab a ready-to-paste `ssh` command and password.
- **Clean teardown** — delete a worker (drained first) or an entire cluster, VMs and all.
- **Simple auth** — bootstrap one admin account on first run; the admin creates everyone else. Every resource is scoped to its owner.
- **Dark mode**, a collapsible sidebar, and live status indicators, because it should feel nice to use.

## Quick start

Everything — the Incus daemon, Postgres, and the app itself — runs as one `docker compose` stack, published as ready-to-pull images. No need to clone the repo.

```bash
curl -fsSL https://raw.githubusercontent.com/anisharaz/incus-k8s-manager/refs/heads/main/meta/incusDocker/quickstart.sh | bash
```

This downloads `docker-compose.yml` into an `incus-k8s-manager/` directory, generates a `.env` with your host's KVM group auto-detected, and runs `docker compose up -d`. Once the containers report healthy (`docker compose ps`), open **https://localhost** (or `https://<server-ip>` from another machine) — you'll land on a one-time "create admin account" screen. After that, log in, create a network, create a cluster, and watch it come up.

> First run pulls a few images and the Incus daemon initializes itself — give it a minute. See [`meta/incusDocker/README.md`](meta/incusDocker/README.md) if `KVM_GID`/KVM support needs troubleshooting on your host.
>
> The stack terminates TLS itself with a self-signed certificate — there's no domain name involved, so your browser will show a "not secure" warning the first time. That's expected; click through it. The connection is still genuinely encrypted, and (unlike plain HTTP) the login cookie actually works on anything other than `localhost` — see [`meta/incusDocker/README.md`](meta/incusDocker/README.md#https) for why that matters and how to swap in a real domain + Let's Encrypt later.

### Manual setup

Prefer to do it by hand, or the script doesn't fit your setup:

```bash
mkdir incus-k8s-manager && cd incus-k8s-manager
curl -fsSL https://raw.githubusercontent.com/anisharaz/incus-k8s-manager/refs/heads/main/meta/incusDocker/docker-compose.yml -o docker-compose.yml

cat > .env <<'EOF'
KVM_GID=0
SETIPTABLES=true

POSTGRES_USER=postgres
POSTGRES_PASSWORD=postgres
POSTGRES_DB=incus_k8s_manager
# JWT_SECRET=
COOKIE_SECURE=true
EOF

# Point KVM_GID at your host's actual kvm group
sed -i "s/^KVM_GID=.*/KVM_GID=$(getent group kvm | cut -d: -f3)/" .env

docker compose pull
docker compose up -d
```
## Screenshots

<img width="1896" height="949" alt="Screenshot from 2026-08-05 22-16-36" src="https://github.com/user-attachments/assets/3668a264-50cf-46c5-a405-5a9abdd220fc" />

<img width="1896" height="949" alt="Screenshot from 2026-08-05 22-16-53" src="https://github.com/user-attachments/assets/cd0aae7b-6e94-49ae-815e-9c1509e92034" />

<img width="1896" height="949" alt="Screenshot from 2026-08-05 22-16-58" src="https://github.com/user-attachments/assets/ec8cfea9-5b6d-4837-8af9-5d511046b8a9" />

<img width="1896" height="949" alt="Screenshot from 2026-08-05 22-17-22" src="https://github.com/user-attachments/assets/c6ef14be-e816-4cbc-aa22-9e62be6a81b6" />

<img width="1896" height="949" alt="Screenshot from 2026-08-05 22-17-32" src="https://github.com/user-attachments/assets/d1fb34a8-1bec-4423-8f11-484e4fb58a66" />

<img width="1896" height="949" alt="Screenshot from 2026-08-05 22-17-43" src="https://github.com/user-attachments/assets/5834bff5-59e3-43b2-9e0d-2819f8f26872" />

<img width="1896" height="949" alt="Screenshot from 2026-08-05 22-18-03" src="https://github.com/user-attachments/assets/54e88bcf-5a8c-4ee3-9e54-95b3f25a7574" />

<img width="1896" height="949" alt="Screenshot from 2026-08-05 22-18-10" src="https://github.com/user-attachments/assets/563d8ed5-5140-4a48-9bfa-c8faa81cc053" />

<img width="1896" height="949" alt="Screenshot from 2026-08-05 22-18-16" src="https://github.com/user-attachments/assets/4198dde0-5fc8-4ec5-a820-19e89ded81d4" />

## How it's put together

- **Backend** (`be/`) — Go/Fiber API backed by PostgreSQL. Talks to the Incus daemon over its Unix socket to drive VMs, and tracks every long-running operation (cluster/node create or delete) as a DB-persisted background job the UI polls for progress.
- **Frontend** (`fe/`) — React/Vite/shadcn UI, built and embedded directly into the Go binary — the backend serves it itself, so the whole app ships as a single container image.
- **Incus image** (`meta/incusDocker/`) — a Docker image that runs `incusd`, preloaded with a Kubernetes-ready VM image and the container images `kubeadm` needs, so cluster creation doesn't wait on package downloads.

```
be/    Go backend (API, job manager, Incus client)
fe/    React frontend
meta/  Incus-in-Docker image used as the VM host
API.md backend REST API reference
```

## Docs

- [`API.md`](API.md) — full REST API reference (auth, networks, clusters, nodes, jobs, terminal websocket).
- [`openapi.yaml`](openapi.yaml) — machine-readable OpenAPI 3.0 spec of the same API, for Swagger UI/Redoc/Postman/codegen.
- [`be/README.md`](be/README.md) — backend architecture, local (non-Docker) setup.
- [`meta/incusDocker/README.md`](meta/incusDocker/README.md) — building/running the Incus daemon image, KVM setup.

