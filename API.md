# API Reference

REST API for the KOI (Kubernetes on Incus) backend (`be/`). This is the contract the
frontend (`fe/`) codes against.

> A machine-readable version of this same contract lives at
> [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0) — import it into Swagger UI/
> Redoc/Postman, or feed it to a client-code generator. Keep both in sync
> when the API changes.

- **Base URL (dev, running `be/` directly):** `http://localhost:8000` (`PORT` env var, see `be/.env.example`)
- **Base URL (docker compose):** `https://localhost` (or your server's address) — the bundled `proxy` (Caddy) service terminates TLS in front of the app; see [`meta/incusDocker/README.md#https`](../meta/incusDocker/README.md#https).
- **Format:** all requests/responses are JSON (`Content-Type: application/json`)
- **Auth:** cookie-based session (see [Authentication](#authentication)
  below). The app has exactly one **admin** (created once via a bootstrap
  flow) and any number of regular **users** (created by the admin).
  **Every** Cluster Network / Cluster / Node / Job endpoint requires a
  session and is scoped to the caller — you only ever see and can only ever
  act on resources you own, admin included (the admin's only special power
  is managing the Users list; it has no cross-user visibility into anyone
  else's clusters). There is no `ownerId` field in any request body
  anymore — the owner is always the logged-in user, never client-supplied.
  The session cookie is marked `Secure` whenever `COOKIE_SECURE=true`
  (the default in docker compose, since `proxy` terminates TLS) — browsers
  then require a real HTTPS connection to store/send it at all (an
  exception is made for `localhost`, which they treat as secure even over
  plain HTTP — this is why the same setup can silently "not log in" if
  accessed over plain HTTP by IP instead).
- **CORS (dev):** `http://localhost:5173`, `http://localhost:8000`,
  `http://localhost:3000` are allowed, with credentials
  (`be/internal/middleware/cors.go`) — required for the session cookie to
  be sent cross-origin. Vite's default port (5173) already works out of the
  box. **The frontend must send `fetch(..., { credentials: "include" })`
  (or the XHR/axios equivalent) on every request**, or the browser won't
  attach the session cookie at all.

---

## Conventions

### IDs

Every resource `id` is a server-generated UUID v4 string. Always sent by
the server, never chosen by the client.

### Timestamps

`createdAt`, `updatedAt`, `completedAt` are RFC 3339 timestamps with
nanosecond precision and a timezone offset, e.g.
`"2026-08-02T02:56:56.61614602+05:30"`. Treat as opaque strings and parse
with a standard date library — don't assume UTC or a fixed fractional-second
length.

### Two names on Networks and Nodes

`ClusterNetwork` and `Node` each carry **two** name fields:

- `name` — the display name you chose (or the server auto-generated, e.g.
  `worker-1`). Free-form, unique only within its own scope (see each
  resource below).
- `incusName` — a server-generated identifier used as the actual Incus
  resource name (bridge interface / VM instance name) and globally unique
  across the whole system. You'll rarely need it in the UI, but it's
  included since it's occasionally useful for debugging (it's what shows up
  if you ever shell into the Incus host).

**Never construct `incusName` yourself or use it as a form input** — it's
generated server-side and returned in every response for the resource.

### Errors

Every non-2xx response has this exact shape:

```json
{
  "error": "validation error",
  "message": "cpu must be at least 2, got 1",
  "code": 400
}
```

- `error` — short machine-ish category (e.g. `"validation error"`,
  `"not found"`, `"database error"`, `"incus error"`). Not a stable enum —
  treat it as a label for logging/display, not something to switch on.
  Switch on the HTTP status code instead.
- `message` — human-readable detail, safe to show directly in a UI toast/
  form error.
- `code` — repeats the HTTP status code.

Status codes used throughout the API:

| Code | Meaning here |
|---|---|
| `200` | Success (GET) |
| `201` | Created (resource fully created synchronously — Users, Cluster Networks) |
| `202` | Accepted (resource row created, but a background job is still provisioning it — Clusters, Nodes) |
| `204` | Success, no body (DELETE, logout) |
| `400` | Bad request body / validation failure — safe to show `message` next to the offending field |
| `401` | Not logged in, or session cookie missing/invalid/expired — show the login screen, not a form error |
| `403` | Logged in, but the account's role isn't allowed to do this (currently: non-admin hitting a `/users` endpoint) |
| `404` | Resource not found |
| `409` | Conflict — duplicate name, CIDR overlap, or an operation attempted in the wrong state (e.g. adding a worker before the master is ready) |
| `500` | Server/database/Incus error |

### The async job pattern (important for the UI)

Creating a **Cluster** or a **Node** does real work that takes anywhere from
~10 seconds (a worker joining) to several minutes (a master's first
`kubeadm init`, which pulls container images). These endpoints return
**`202 Accepted`** immediately with the resource in a non-final state, and
kick off a background **Job**. The UI must poll to find out when it's done:

1. `POST /api/v1/clusters` → get back a `Cluster` (`status: "creating"`).
2. `GET /api/v1/clusters/:id/nodes` → find the node (there's exactly one:
   the master), read its `jobId`.
3. Poll `GET /api/v1/jobs/:id` (e.g. every 3–5s) and show `stage` /
   `progress` / `message` as a progress indicator.
4. When the job's `status` becomes `"succeeded"` or `"failed"`, stop
   polling. Re-fetch the `Cluster`/`Node` — their `status`/`message` will
   reflect the outcome too (`ready`/`running` or `failed`).

The same pattern applies to `POST /api/v1/clusters/:id/nodes` (adding a
worker) — poll the returned `Node`'s `jobId`.

**Job `stage` values you'll see** (informational strings for progress UI,
not a fixed enum — don't hardcode exhaustive handling, just display them):

| Stage | Meaning | Applies to |
|---|---|---|
| `queued` | Job row created, goroutine not yet scheduled | both |
| `launching` | Creating and starting the Incus VM | both |
| `waiting-for-ip` | Waiting for DHCP to assign the VM an address | both |
| `waiting-for-agent` | Waiting for the in-VM guest agent to respond | both |
| `waiting-for-containerd` | Waiting for the container runtime to finish starting | both |
| `bootstrapping` | Running `kubeadm init` | master only |
| `configuring-kubeconfig` | Copying `admin.conf` for `kubectl` | master only |
| `joining` | Fetching a join token from the master and running `kubeadm join` | worker only |
| `verifying` | Polling the API server / node registration before declaring done | both |
| `complete` | Done — check `status` for success/failure | both |
| `failed` | Something errored — see the job's `error` field | both |

---

## Data model

```
User
 ├─ owns → ClusterNetwork (many)
 └─ owns → Cluster (many)
              ├─ references → ClusterNetwork (one; RESTRICT delete while referenced)
              └─ has → Node (many: exactly one "master", zero+ "worker")
                          └─ tracked by → Job (the node's provisioning job)
```

- A `Cluster` always has **exactly one** master node (enforced server-side —
  a cluster is created together with its master in one request).
- A `Cluster`'s own `status` reflects its **master's** provisioning outcome
  (`ready` once the master's `kubeadm init` succeeds and the API server is
  healthy). Worker outcomes don't change cluster status.
- Deleting a `ClusterNetwork` is blocked (`500`, underlying FK violation)
  while any `Cluster` references it.
- Every `ClusterNetwork`, `Cluster`, and `Job` carries an `ownerId` and is
  scoped to it on every read/write. A `Node` has no `ownerId` of its own —
  it inherits scoping from its parent `Cluster`.

### Enums

```ts
UserRole             = "admin" | "user"
ClusterNetworkStatus = "creating" | "ready" | "failed"
ClusterStatus        = "creating" | "ready" | "failed" | "deleting"
CNIType               = "cilium"  // the only implemented value today; more may be added later
NodeRole              = "master" | "worker"
NodeStatus            = "creating" | "running" | "stopped" | "failed" | "deleting"  // "stopped" defined but not yet used
JobStatus             = "queued" | "running" | "succeeded" | "failed"
```

---

## Health & Status

### `GET /health`

Liveness check, no `/api/v1` prefix.

```json
{ "status": "ok", "message": "Server is running" }
```

### `GET /api/v1/status`

Whether the Incus CLI is reachable from the backend process. Mostly a
backend-ops diagnostic, not something to build UI around.

```json
{ "status": { "incus": "running" } }
```

`incus` is one of `"running"`, `"stopped"`, `"not found"`.

---

## Authentication

Session is a JWT in an **HttpOnly cookie** (`auth_token`) — JavaScript
cannot read it, and the browser attaches it automatically on requests to
the API origin as long as `credentials: "include"` is set (see CORS note
above). There is no bearer-token header to manage client-side; you never
see the token value itself.

Two roles: **admin** (exactly one, created once via bootstrap) and
**user** (any number, created by the admin). `User.role` is `"admin"` or
`"user"`.

### First-run flow

On app load, the frontend should:
1. `GET /api/v1/auth/status` — if `adminCreated: false`, show a "create
   the admin account" screen and call `POST /api/v1/auth/register-admin`.
2. If `adminCreated: true`, show a normal login screen
   (`POST /api/v1/auth/login`) — unless already logged in (see `/auth/me`
   below), which happens if a valid session cookie is already present
   (e.g. page refresh).

### `GET /api/v1/auth/status`

Public — no session needed. Poll/check this before deciding which screen
to show; it's the only way to distinguish first-run from steady-state.

**Response `200`:**
```json
{ "adminCreated": false }
```

### `POST /api/v1/auth/register-admin`

Creates the app's **one** admin account and immediately logs it in (sets
the session cookie) — no separate login call needed right after. **Only
succeeds once**, ever; concurrent first-boot requests can't both win (the
backend row-locks the bootstrap state during the check). Also creates a
`"default"` cluster network for the new admin (see [Users](#users)'s
`POST /api/v1/users` for the same behavior on regular users).

**Request:**
```json
{ "username": "admin", "password": "supersecret123" }
```
- `username` — 1–63 chars.
- `password` — **minimum 8 characters** (this minimum applies to every
  account, admin or regular user).

**Response `201`:** `{ "user": User }` (see the User shape under Users
below — no password/hash ever appears in any response).

**Errors:**
- `400` — bad username length, or password under 8 characters
- `409` `"already bootstrapped"` — an admin already exists; use `/auth/login` instead

### `POST /api/v1/auth/login`

**Request:** `{ "username": "...", "password": "..." }` (works for the admin or any regular user)

**Response `200`:** `{ "user": User }`, session cookie set.

**Errors:** `401` — wrong username *or* wrong password. **Deliberately
the same error/message for both** ("invalid username or password") so a
client can't use this endpoint to enumerate valid usernames.

### `POST /api/v1/auth/logout`

Clears the session cookie. **Response `204`**, no body. No error cases —
safe to call even if not currently logged in.

### `GET /api/v1/auth/me`

Returns the currently authenticated user. Since the cookie is HttpOnly,
**this is the only way the frontend can find out who's logged in** (e.g.
on page load/refresh, to restore session state without asking the user to
log in again).

**Response `200`:** `{ "user": User }` · **`401`** if not logged in / session expired/invalid — treat this as "show the login screen," not as an error to surface to the user.

---

## Users

Admin-only — every endpoint below requires an authenticated **admin**
session (`401` if not logged in at all, `403` if logged in as a regular
user). Regular users don't self-register; the admin creates them here.

### User shape

```json
{
  "id": "2b9dc998-2c29-4aef-90af-58a938a3d013",
  "username": "alice",
  "role": "user",
  "createdAt": "2026-08-02T01:28:58.841899082+05:30",
  "updatedAt": "2026-08-02T01:28:58.841899082+05:30"
}
```
(Never includes a password or password hash — that field is excluded from JSON entirely, server-side.)

### `POST /api/v1/users`

Creates a regular user (`role` is always `"user"` — the one admin account
only ever comes from `/auth/register-admin`). Also creates a `"default"`
cluster network (auto-selected CIDR) for the new user, the same as the one
`/auth/register-admin` creates for the bootstrap admin — best-effort: if
Incus network creation fails, the user is still created, just without a
default network (a warning is logged server-side; the user can create one
by hand from the Networks page).

**Request:** `{ "username": "...", "password": "..." }` (same length/password rules as registering the admin)

**Response `201`:** `{ "user": User }`

**Errors:** `401` not logged in · `403` logged in but not admin · `400` bad username/password · `409` username taken.

### `GET /api/v1/users`

**Response `200`:** `{ "users": [ User, ... ] }` (newest first) · `401`/`403` as above.

### `GET /api/v1/users/:id`

**Response `200`:** `{ "user": User }` · `404` not found · `401`/`403` as above.

### `DELETE /api/v1/users/:id`

Starts a background job (`type: "user_deletion"`, owned by the admin who
triggered it — see [Jobs](#jobs)) that deletes every VM in every cluster
the target user owns, then those clusters, then every network they own,
and finally the user account itself. Only non-admin users can be deleted
this way — since the caller must already be an admin, this also means an
admin can never delete themselves or another admin through this endpoint.

**Response `202`:** `{ "job": Job }` — poll `GET /api/v1/jobs/:jobId` for
progress; `metadata.userId` identifies the target.

**Errors:** `401` not logged in · `403` logged in but not admin, or target
is an admin · `404` user not found.

---

## Cluster Networks

An Incus bridge network that cluster VMs are later launched onto. Must be
created before a cluster can be created. **Every endpoint below requires a
session and is scoped to the caller** — `401` if not logged in.

### `POST /api/v1/networks`

`cidr` is optional. If provided, it's validated against **every** network
Incus currently knows about (not just ones created through this API —
including the appliance's own bridge) to prevent an overlapping subnet. If
omitted, Incus picks an unused private subnet itself
(`"ipv4.address": "auto"`) — no client-side conflict-checking needed in
that case, since the daemon guarantees the pick doesn't collide with
anything it already manages. Either way, creation is synchronous (this
one's `201`, not `202` — no job/polling needed). Owned by the logged-in
user.

**Request:**
```json
{
  "name": "prod-net",
  "cidr": "10.10.0.0/24"
}
```

- `name` — free-form, 1–63 chars, unique **per owner** (two different users
  can both have a network named `"prod-net"`).
- `cidr` — **optional**. If provided: IPv4, must be the network address
  itself (no host bits — e.g. `10.10.0.5/24` is rejected with a
  suggestion), prefix length between `/8` and `/29`. If omitted, Incus
  auto-assigns one and the response reflects whatever it picked.

**Response `201`:**
```json
{
  "network": {
    "id": "5c701cdc-496d-42aa-802c-fa065e2a83a0",
    "ownerId": "2b9dc998-2c29-4aef-90af-58a938a3d013",
    "name": "prod-net",
    "incusName": "cn5c701cdc496d4",
    "cidr": "10.10.0.0/24",
    "gateway": "10.10.0.1",
    "status": "ready",
    "message": "Network created",
    "createdAt": "2026-08-02T01:29:09.073878164+05:30",
    "updatedAt": "2026-08-02T01:29:09.073878164+05:30"
  }
}
```

`ownerId` is always the logged-in user's id — informational, not something
you send. `gateway` is auto-derived as the first usable address in the
CIDR (network address + 1) — also not user-supplied.

**Errors:**
- `401` — not logged in
- `400` — bad `name` length, malformed/out-of-range `cidr`
- `409` `"network already exists"` — **you** already have a network with that `name` (a different user having the same name is fine)
- `409` `"cidr conflict"` — overlaps an existing Incus network; `message` names which one and its CIDR
- `500` — an Incus-side error

### `GET /api/v1/networks`

**Response `200`:** `{ "networks": [ ClusterNetwork, ... ] }` — only the caller's own networks, newest first.

### `GET /api/v1/networks/:id`

**Response `200`:** `{ "network": ClusterNetwork }` · **`404`** if not found *or* owned by someone else — the two look identical, by design (no existence leak).

### `DELETE /api/v1/networks/:id`

Deletes from both Incus and the database. **`204`** on success.

**Errors:** `404` not found/not yours · `500` if Incus refuses (e.g. still
referenced by a `Cluster` — message will mention it's in use).

---

## Clusters

Creating a cluster creates its **master node** and launches that node's VM
as a background job (see "The async job pattern" above) — **poll before
assuming the cluster is usable.** **Every endpoint below requires a session
and is scoped to the caller** — `401` if not logged in.

### `POST /api/v1/clusters`

Owned by the logged-in user, who must also own `networkId` — building a
cluster on someone else's network isn't possible even if you somehow know
its id (see Errors).

**Request:**
```json
{
  "networkId": "5c701cdc-496d-42aa-802c-fa065e2a83a0",
  "name": "prod-cluster",
  "cni": "cilium",
  "cpu": 2,
  "memory": "2GiB",
  "disk": "20GiB"
}
```

- `name` — free-form, 1–63 chars, unique **per owner**.
- `cni` — **optional**, the pod networking plugin the master installs after
  `kubeadm init`. Defaults to `"cilium"` if omitted/empty. `"cilium"` is
  currently the **only allowed value** — any other value is **rejected with
  `400`**, listing the allowed set in the message. The request/response
  shape is deliberately unchanged by this restriction, so adding more CNIs
  later won't require an API version bump.
- `cpu`, `memory`, `disk` — **all optional**, size the master's VM. Omit any
  of them (or send `0`/`""`) to use the default. If provided, each is
  checked against a minimum and the request is **rejected with `400`** if
  below it — nothing is silently clamped up.

  | Field | Type | Default | Minimum | Why |
  |---|---|---|---|---|
  | `cpu` | int (vCPUs) | `2` | `2` | kubeadm's own hard preflight check |
  | `memory` | string, [Incus size format](#size-string-format) | `"2GiB"` | `1700MB` | kubeadm's own hard preflight check (default is intentionally above the bare minimum — see note below) |
  | `disk` | string, [Incus size format](#size-string-format) | `"20GiB"` | `20GiB` | not kubeadm-enforced; this app's own floor for etcd + images |

  Note: the memory *default* (`2GiB`) is deliberately above the *minimum*
  (`1700MB`) — virtualization overhead can make the guest see slightly less
  RAM than configured, and kubeadm's check is a hard cutoff, so sitting
  exactly on the minimum risks failing it. If a user explicitly requests
  something between `1700MB` and `2GiB`, that's allowed (it passed the
  minimum); the risk only applies to the auto-default.

**Response `202`:**
```json
{
  "cluster": {
    "id": "cba22032-aca2-4d1e-902f-289459c91961",
    "ownerId": "2b9dc998-2c29-4aef-90af-58a938a3d013",
    "networkId": "5c701cdc-496d-42aa-802c-fa065e2a83a0",
    "name": "prod-cluster",
    "cni": "cilium",
    "status": "creating",
    "message": "Cluster creation started",
    "createdAt": "2026-08-02T01:43:41.23486713+05:30",
    "updatedAt": "2026-08-02T01:43:41.23486713+05:30"
  }
}
```

Immediately follow up with `GET /api/v1/clusters/:id/nodes` to get the
master node's `jobId` to poll (see below) — the create response itself
doesn't include the node or job. `Cluster` also has its own `jobId` field
(omitted/absent except while a `DELETE` is in progress — see below), not
used for creation.

When the master's job succeeds, a follow-up `GET /api/v1/clusters/:id` will
show:
```json
{ "status": "ready", "message": "Kubernetes control plane is ready", ... }
```
or on failure:
```json
{ "status": "failed", "message": "Master node provisioning failed", ... }
```
(the underlying error detail is on the **job**, not the cluster — see Jobs).

**Errors:**
- `401` — not logged in
- `400` — missing `networkId`, bad `name`, unrecognized `cni`, or `cpu`/`memory`/`disk` below minimum (message names which field and why)
- `404` `"cluster network not found"` — `networkId` doesn't exist, or belongs to someone else (identical response either way)
- `409` `"cluster already exists"` — **you** already have a cluster with that `name`
- `500` — a job-creation/database error

### `GET /api/v1/clusters`

**Response `200`:** `{ "clusters": [ Cluster, ... ] }` — only the caller's own clusters, newest first.

### `GET /api/v1/clusters/:id`

**Response `200`:** `{ "cluster": Cluster }` · **`404`** if not found *or* owned by someone else.

### `GET /api/v1/clusters/:id/nodes`

Lists the cluster's nodes — master first, then workers in the order they
were added. This is how the UI discovers node IDs, `jobId`s, IPs, and
per-node status. `404` if the cluster doesn't exist or belongs to someone else.

**Response `200`:**
```json
{
  "nodes": [
    {
      "id": "0f9bf446-fb31-474b-9edb-71fb8f29dfd9",
      "clusterId": "c0150a11-3a9b-4a96-8182-aacae98c33fd",
      "jobId": "cdd9ef13-b5c5-426f-9d1e-d3e07b69c81b",
      "name": "master",
      "incusName": "master-0f9bf446fb31",
      "role": "master",
      "status": "running",
      "ip": "10.44.0.192",
      "message": "Kubernetes control plane is ready",
      "sshPassword": "aB3xQ9zM2kLpR7Tw",
      "createdAt": "2026-08-02T03:08:02.276843+05:30",
      "updatedAt": "2026-08-02T03:09:14.547593+05:30"
    },
    {
      "id": "d6da0b00-9b36-47e5-89e2-7a3904199423",
      "clusterId": "c0150a11-3a9b-4a96-8182-aacae98c33fd",
      "jobId": "a0ed32d1-408a-4376-bd8e-5d54e9126bff",
      "name": "worker-1",
      "incusName": "worker-d6da0b009b36",
      "role": "worker",
      "status": "running",
      "ip": "10.44.0.9",
      "message": "Node joined the cluster",
      "sshPassword": "hN4vT8xY1qWzC6Ld",
      "createdAt": "2026-08-02T03:09:28.180597+05:30",
      "updatedAt": "2026-08-02T03:10:06.962443+05:30"
    }
  ]
}
```

`jobId` is present once the node's provisioning job has been created
(effectively always, immediately after the node row exists) — treat it as
required rather than optional in the UI. `ip` and `message` are empty until
the job progresses far enough to know them.

`sshPassword` is a random password set for the VM image's `ubuntu` user
during provisioning (no password is baked into the image itself — see
`meta/incusDocker/incusStuff/incus_distrobuilder.yaml`), stored in plain
text so it can be shown to the owner from the UI at any time, not just once
at creation. Empty if provisioning never got far enough to set it, or the
SSH setup step itself failed — it's best-effort and doesn't fail the node's
provisioning job. Connect with `ssh ubuntu@<ip>`.

> There is no `GET /api/v1/nodes/:id` (single node) yet — always fetch
> nodes through this cluster-scoped list.

### `GET /api/v1/clusters/:id/kubeconfig`

Downloads the cluster's admin kubeconfig, read live from the master's
`/root/.kube/config`. **Not the usual JSON envelope** — the response body
*is* the kubeconfig YAML, with:
- `Content-Type: application/yaml`
- `Content-Disposition: attachment; filename="<cluster-name>-kubeconfig.yaml"`

A plain `<a href="/api/v1/clusters/:id/kubeconfig" download>` works fine —
the session cookie rides along automatically for a same-origin request, no
extra auth header needed.

**Errors:**
- `401` — not logged in
- `404` `"cluster not found"` — doesn't exist, or belongs to someone else
- `409` `"master not running"` — wait for the cluster to finish provisioning
- `500` — Incus error reading the file (e.g. the master's agent is down)

---

## Nodes (worker management)

### `POST /api/v1/clusters/:id/nodes`

Adds a worker node to a cluster you own. Launches a VM on the cluster's
network, fetches a **fresh** join token from the master
(`kubeadm token create --print-join-command` — not the one `kubeadm init`
printed originally, which may be long expired), and runs `kubeadm join`.
Same async-job pattern as cluster creation.

**Preconditions** (checked before anything is created):
- The cluster must exist and belong to you (`404` otherwise).
- The cluster's `status` must be `"ready"` (`409` otherwise).
- The cluster's master node `status` must be `"running"` (`409` otherwise).

**Request:** entirely optional — `{}` or no body at all is valid and uses
every default.
```json
{ "cpu": 2, "memory": "2GiB", "disk": "20GiB" }
```
Same fields, same defaults/minimums/validation as cluster creation's
`cpu`/`memory`/`disk` (see the table above) — these are also enforced for
`kubeadm join`, not just `init`.

**Response `202`:**
```json
{
  "node": {
    "id": "d6da0b00-9b36-47e5-89e2-7a3904199423",
    "clusterId": "c0150a11-3a9b-4a96-8182-aacae98c33fd",
    "jobId": "a0ed32d1-408a-4376-bd8e-5d54e9126bff",
    "name": "worker-1",
    "incusName": "worker-d6da0b009b36",
    "role": "worker",
    "status": "creating",
    "message": "Node creation started",
    "createdAt": "2026-08-02T03:09:28.180597522+05:30",
    "updatedAt": "2026-08-02T03:09:28.188440653+05:30"
  }
}
```

`name` auto-increments per cluster: `worker-1`, `worker-2`, ... (based on a
count of existing workers — this numbering isn't collision-proof against
gaps left by deleted workers, e.g. deleting `worker-1` and adding a new
worker afterward produces a second `worker-1`; display names aren't
required to be sequential or gap-free, just unique per cluster).

Poll `GET /api/v1/jobs/:jobId`; on success the job's `message` becomes
`"Node joined the cluster"` and the node's `status` becomes `"running"`.

**Errors:**
- `401` — not logged in
- `404` `"cluster not found"` — doesn't exist, or belongs to someone else
- `409` `"cluster not ready"` — wait for the cluster's master to finish first
- `409` `"master not running"` — same idea, different point of failure
- `400` — `cpu`/`memory`/`disk` below minimum
- `500` — database/job-creation error

### `DELETE /api/v1/clusters/:id/nodes/:nodeId`

Deletes a single **worker** node; the rest of the cluster keeps running.
Drains the node (`kubectl drain --ignore-daemonsets --delete-emptydir-data
--force`) and removes its Node API object (both run against the master's
kubeconfig), runs `kubeadm reset --force` on the worker itself, then
deletes its VM. Same async-job pattern as creation — the node's `status`
becomes `"deleting"` immediately, and the node row is removed from `GET
.../nodes` only once the job succeeds.

Deleting the **master** isn't supported through this endpoint — delete the
whole cluster instead (see below).

**Response `202`:**
```json
{
  "node": {
    "id": "d6da0b00-9b36-47e5-89e2-7a3904199423",
    "clusterId": "c0150a11-3a9b-4a96-8182-aacae98c33fd",
    "jobId": "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
    "name": "worker-1",
    "incusName": "worker-d6da0b009b36",
    "role": "worker",
    "status": "deleting",
    "message": "Node deletion started",
    "createdAt": "2026-08-02T03:09:28.180597522+05:30",
    "updatedAt": "2026-08-02T03:11:02.180597522+05:30"
  }
}
```

Poll `GET /api/v1/jobs/:jobId` (`type: "node_deletion"`); on success the
node row is gone entirely — a subsequent `GET .../nodes` simply won't list
it anymore (there's no "deleted" status to observe).

**Errors:**
- `401` — not logged in
- `404` — cluster or node not found (or either belongs to someone else)
- `400` `"cannot delete master"` — delete the cluster instead
- `409` `"operation in progress"` — the node is already `creating` or `deleting`
- `409` `"master not running"` — the master must be up to drain the worker through it
- `500` — database/job-creation error

### `DELETE /api/v1/clusters/:id`

Deletes the entire cluster: every node's VM, then the cluster itself. This
is the **only** way to remove a master. No kubectl-graceful steps are run
(the whole control plane is going away, so draining has no lasting
benefit) — VMs are just torn down directly, fail-fast. The cluster and
every node are marked `"deleting"` immediately; the `Cluster` row (and all
its `Node` rows, cascaded) disappear only once the job succeeds — after
that, `GET /api/v1/clusters/:id` returns `404`, which is the success
signal to watch for.

Works from any cluster state, including `"failed"` — retrying a failed
deletion is safe (VM teardown is idempotent).

**Response `202`:**
```json
{
  "cluster": {
    "id": "cba22032-aca2-4d1e-902f-289459c91961",
    "ownerId": "2b9dc998-2c29-4aef-90af-58a938a3d013",
    "networkId": "5c701cdc-496d-42aa-802c-fa065e2a83a0",
    "jobId": "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
    "name": "prod-cluster",
    "cni": "cilium",
    "status": "deleting",
    "message": "Cluster deletion started",
    "createdAt": "2026-08-02T01:43:41.23486713+05:30",
    "updatedAt": "2026-08-02T03:12:00.23486713+05:30"
  }
}
```

Poll `GET /api/v1/jobs/:jobId` (`type: "cluster_deletion"`) until it
succeeds, then treat a subsequent `404` on `GET /api/v1/clusters/:id` as
confirmation the cluster is fully gone.

**Errors:**
- `401` — not logged in
- `404` `"cluster not found"` — doesn't exist, or belongs to someone else
- `409` `"deletion in progress"` — a deletion job is already running for this cluster
- `500` — database/job-creation error, or the cluster has no master row (shouldn't happen)

### `GET /api/v1/clusters/:id/nodes/:nodeId/terminal`

**WebSocket upgrade, not a normal REST call.** Opens an interactive `bash`
shell (running as root) inside the node's VM — works for either role,
master or worker. Auth/ownership/status checks happen on the initial
upgrade request (same cookie-based session as everywhere else — a plain
browser `new WebSocket(...)` carries it automatically), so a rejected
check returns a normal HTTP error and the socket never upgrades:
- `401` — not logged in
- `404` — cluster or node not found (or either belongs to someone else)
- `409` `"node not running"` — the node must be `running` to open a shell

**Wire protocol**, once upgraded:
- **Binary frames carry raw PTY bytes in both directions** — browser→server
  is keystrokes/input, server→browser is terminal output. Write these
  straight into/out of a terminal emulator (e.g. xterm.js) with no framing.
- **Text frames are a small JSON control envelope**, currently only sent
  browser→server: `{"type":"resize","cols":<int>,"rows":<int>}`, whenever
  the terminal's size changes — including once right after connecting, to
  correct the server's initial guessed PTY size (100x30).

There's no server-side idle timeout — the session runs until the browser
closes the socket (same lifetime model as SSH). No `result`/job/polling
involved; this is a raw, long-lived connection, not an async-job-pattern
endpoint.

---

## Jobs

Read-only — jobs are only ever created as a side effect of `POST
/api/v1/clusters` or `POST /api/v1/clusters/:id/nodes`. This is the primary
polling target for provisioning progress (see "The async job pattern").
**Requires a session; every job is scoped to the caller** — you'll never
see or be able to fetch someone else's job, even by guessing its id.

### `GET /api/v1/jobs`

**Response `200`:** `{ "jobs": [ Job, ... ] }` — only the caller's own jobs, newest first; no filter by node/cluster, so find the `jobId` you care about via the node/cluster endpoints first, then poll it individually.

### `GET /api/v1/jobs/:id`

**Response `200`:**
```json
{
  "job": {
    "id": "a0ed32d1-408a-4376-bd8e-5d54e9126bff",
    "ownerId": "2b9dc998-2c29-4aef-90af-58a938a3d013",
    "type": "node_provision",
    "name": "Provision worker node worker-d6da0b009b36",
    "status": "running",
    "progress": 80,
    "stage": "joining",
    "message": "Running kubeadm join...",
    "metadata": { "nodeId": "d6da0b00-9b36-47e5-89e2-7a3904199423", "role": "worker" },
    "createdAt": "2026-08-02T03:09:28.18648+05:30",
    "updatedAt": "2026-08-02T03:10:00.123456+05:30"
  }
}
```

On success, `status` becomes `"succeeded"`, `progress` is `100`, and
`result` is populated (currently just `{ "ip": "10.44.0.9" }`).

On failure:
```json
{
  "status": "failed",
  "stage": "failed",
  "message": "Node provisioning failed",
  "error": "command \"kubeadm init\" in instance \"master-...\" exited 1: ...(full kubeadm stderr)...",
  "completedAt": "2026-08-02T03:09:14.545476+05:30"
}
```
`error` is the raw underlying failure — often multi-line command output.
Fine to put in a collapsible "details" section, not meant as the headline
error message (use `message` for that).

`type` is one of `"node_provision"` (master or worker creation — check
`metadata.role` to distinguish which), `"node_deletion"` (worker deletion —
`metadata.nodeId`), `"cluster_deletion"` (whole-cluster deletion —
`metadata.clusterId`), or `"user_deletion"` (deleting a non-admin user and
everything they own — `metadata.userId`; owned by the admin who triggered
it, not the deleted user).

**Errors:** `401` not logged in · `404` `"job not found"` — doesn't exist, or belongs to someone else.

---

## Size string format

`memory` and `disk` fields use Incus's byte-size syntax: an integer
immediately followed by a unit suffix, no space.

- Decimal (powers of 1000): `B`, `kB`, `MB`, `GB`, `TB`, `PB`, `EB`
- Binary (powers of 1024): `KiB`, `MiB`, `GiB`, `TiB`, `PiB`, `EiB`

Examples: `"2GiB"`, `"1700MB"`, `"20GiB"`. An unparseable string (e.g.
`"lots"`, `"2 GB"` with a space, `"2gb"` wrong case) is rejected with `400`
and a message like `"memory: Invalid value: lots"`.

---

## Example: full create-cluster-with-worker flow

```
 0. GET  /api/v1/auth/status                                           → adminCreated
 0a. (first run) POST /api/v1/auth/register-admin  {username, password} → logged in as admin
 0b. (steady state) POST /api/v1/auth/login         {username, password} → logged in
 1. (admin only) POST /api/v1/users            {username, password}    → user.id
 1a. (as that user) POST /api/v1/auth/login    {username, password}    → logged in as that user
 2. POST /api/v1/networks                     {name, cidr}            → network.id  (owned by whoever is logged in)
 3. POST /api/v1/clusters                     {networkId, name}       → cluster.id (202, status: creating)
 4. GET  /api/v1/clusters/:id/nodes                                    → nodes[0].jobId (the master)
 5. poll GET /api/v1/jobs/:jobId  until status is succeeded|failed
 6. GET  /api/v1/clusters/:id                                          → confirm status: ready
 7. POST /api/v1/clusters/:id/nodes           {} (or sizing overrides) → node.id, node.jobId (202)
 8. poll GET /api/v1/jobs/:jobId  until status is succeeded|failed
 9. GET  /api/v1/clusters/:id/nodes                                    → confirm the new worker's status: running
10. DELETE /api/v1/clusters/:id/nodes/:nodeId (the worker)             → node.jobId (202, status: deleting)
11. poll GET /api/v1/jobs/:jobId  until status is succeeded|failed
12. DELETE /api/v1/clusters/:id                                        → cluster.jobId (202, status: deleting)
13. poll GET /api/v1/jobs/:jobId  until status is succeeded|failed
14. GET  /api/v1/clusters/:id                                          → 404 confirms the cluster is fully gone
```

Steps 2–9 all run as whichever user is currently logged in (steps 1/1a are
only relevant if that's a regular user the admin needs to create first —
the admin itself can also directly own networks/clusters by skipping
straight from 0a to step 2).

---

## Known gaps (don't build UI expecting these yet)

- Session TTL is 24h with no refresh — a user logged in for a full day
  gets a `401` on their next request and must log in again. No "remember
  me" / refresh token yet.
- No password reset / change-password / forgot-password flow.
- No delete for Users.
- No pagination on any list endpoint — expect small counts for now.
- Interrupted jobs (server restart mid-provisioning) are not recovered or
  retried — a job stuck in `"running"` after a backend restart is orphaned.
