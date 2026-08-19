#!/usr/bin/env bash
set -euo pipefail

# Resolve the script directory so compose can be run from anywhere.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$SCRIPT_DIR/docker-compose.yml"
ENV_FILE="$SCRIPT_DIR/.env"
SETIPTABLES="true"
HEALTH_TIMEOUT=300
COMPOSE_MODE=""

if [ -t 1 ]; then
    GREEN=$'\e[0;32m'; YELLOW=$'\e[0;33m'; RED=$'\e[0;31m'; NC=$'\e[0m'
else
    GREEN=''; YELLOW=''; RED=''; NC=''
fi
info() { echo "${GREEN}[+]${NC} $*"; }
warn() { echo "${YELLOW}[!]${NC} $*"; }
fail() { echo "${RED}[x]${NC} $*" >&2; exit 1; }

check_docker() {
    command -v docker >/dev/null 2>&1 || fail "Docker is not installed or not in PATH."
    docker info >/dev/null 2>&1 || fail "Docker daemon is not accessible."
    
    if docker compose version >/dev/null 2>&1; then
        COMPOSE_MODE="plugin"
        elif command -v docker-compose >/dev/null 2>&1; then
        COMPOSE_MODE="legacy"
    else
        fail "Neither docker compose nor docker-compose is available."
    fi
}

KVM_AVAILABLE=false
KVM_GID=""

check_kvm() {
    if [ -e /dev/kvm ]; then
        KVM_AVAILABLE=true
        info "/dev/kvm is available."
    else
        fail "/dev/kvm must exist before compose starts."
    fi
    
    if grep -qE 'vmx|svm' /proc/cpuinfo; then
        info "CPU virtualization extensions detected."
    else
        warn "No vmx/svm flags in /proc/cpuinfo."
    fi
}

get_kvm_gid() {
    [ "$KVM_AVAILABLE" = true ] || return 0
    KVM_GID="$(getent group kvm | cut -d: -f3 || true)"
    if [ -z "$KVM_GID" ]; then
        KVM_GID="$(stat -c '%g' /dev/kvm 2>/dev/null || true)"
    fi
    if [ -n "$KVM_GID" ]; then
        info "Using KVM_GID=$KVM_GID"
    else
        warn "Could not determine the kvm group GID — omitting KVM_GID."
    fi
}

refresh_compose_env() {
    [ -f "$COMPOSE_FILE" ] || fail "Compose file not found: $COMPOSE_FILE"

    if [ ! -f "$ENV_FILE" ]; then
        cat > "$ENV_FILE" <<EOF
KVM_GID=$KVM_GID
SETIPTABLES=$SETIPTABLES
EOF
        return
    fi

    # .env may already have other vars set by quickstart.sh or by hand
    # (POSTGRES_PASSWORD, JWT_SECRET, COOKIE_SECURE, ...) — update only
    # KVM_GID/SETIPTABLES in place instead of overwriting the whole file.
    # Re-running this script used to reset those other values back to
    # compose's defaults, silently breaking the DB connection and
    # invalidating every session.
    set_env_var KVM_GID "$KVM_GID"
    set_env_var SETIPTABLES "$SETIPTABLES"
}

set_env_var() {
    local key="$1" value="$2"
    if grep -q "^${key}=" "$ENV_FILE"; then
        sed -i "s|^${key}=.*|${key}=${value}|" "$ENV_FILE"
    else
        echo "${key}=${value}" >> "$ENV_FILE"
    fi
}

compose() {
    if [ "$COMPOSE_MODE" = "plugin" ]; then
        docker compose "$@"
    else
        docker-compose "$@"
    fi
}

start_stack() {
    info "Starting via compose..."
    (
        cd "$SCRIPT_DIR"
        compose up -d --wait --wait-timeout "$HEALTH_TIMEOUT" --remove-orphans
    ) || fail "compose up failed."
}


main() {
    check_docker
    check_kvm
    get_kvm_gid
    refresh_compose_env
    start_stack
}
main "$@"
