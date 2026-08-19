FROM node:26-bookworm AS frontend
WORKDIR /src/fe
RUN npm install -g pnpm
COPY fe/package.json fe/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY fe/ ./
RUN pnpm build

FROM golang:1.26-bookworm AS backend
WORKDIR /src/be
COPY be/go.mod be/go.sum ./
RUN go mod download
COPY be/ ./
COPY --from=frontend /src/fe/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -o /out/koi ./cmd/server

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY --from=backend /out/koi /usr/local/bin/koi
EXPOSE 8000
# /health is a static "ok" with no DB/Incus dependency — it only answers
# once the binary has actually started serving, which is exactly what
# Caddy's reverse_proxy needs to know before sending it real traffic
# (without this, docker-compose.yml can only depend on koiapp having
# started at all, not having finished starting).
HEALTHCHECK --interval=5s --timeout=3s --start-period=30s --retries=6 \
    CMD curl -fsS http://localhost:8000/health >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/koi"]
