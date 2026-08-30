#!/usr/bin/env bash
#
# SessionStart hook: bring up the infrastructure the database-gated tests need.
#
# Most of this repository's tests stub their dependencies and pass on a clean
# machine. The ones that do not are the audit tests: internal/audit/...,
# internal/audit/checkpoint, internal/audit/emitter and internal/purge all read
# TEST_DATABASE_URL, and internal/gateway/integration_test.go needs a Redis as
# well. Without those they skip, and .github/workflows/ci.yml treats a skip in
# the audit packages as a failure precisely because a skip reads as a pass. So a
# session without Postgres cannot tell you whether an audit change is sound.
#
# What this does, in order:
#   1. starts dockerd if it is not already up
#   2. brings up the postgres and redis services from deploy/docker-compose.yaml
#   3. runs the migrations, because the integration tests read the real schema
#      rather than creating their own
#   4. exports the variables those tests gate on
#
# It deliberately does not start deploy/docker-compose.yaml's aegis-filter-nlp
# service. That one is built from source rather than pulled, the build needs
# network egress this environment does not always have, and no test depends on
# it: internal/filter/pii stubs the gRPC client.
#
# Infrastructure failures here are reported and then tolerated. A session where
# dockerd will not start is still a useful session for the ~95% of packages that
# need no database, and failing the hook would take that away too.

set -uo pipefail

if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

# Matches deploy/docker-compose.yaml's postgres service and .mise.toml's [env].
DATABASE_URL="postgres://aegis:aegis-dev@localhost:5432/aegis?sslmode=disable"
REDIS_URL="localhost:6379"
# Local pepper only. The gateway and keygen refuse to start without one of at
# least 32 characters; a real deployment sets its own.
KEY_PEPPER="aegis-session-hook-pepper-not-for-production"

log() { echo "[session-start] $*"; }

emit_env() {
  [ -n "${CLAUDE_ENV_FILE:-}" ] || return 0
  {
    echo "export AEGIS_KEY_PEPPER=\"$KEY_PEPPER\""
    if [ "${1:-}" = "with-db" ]; then
      echo "export DATABASE_URL=\"$DATABASE_URL\""
      # The integration tests gate on TEST_DATABASE_URL, not DATABASE_URL.
      echo "export TEST_DATABASE_URL=\"$DATABASE_URL\""
      echo "export REDIS_URL=\"$REDIS_URL\""
      echo "export REDIS_HOST=\"localhost\""
      echo "export REDIS_PORT=\"6379\""
    fi
  } >> "$CLAUDE_ENV_FILE"
}

# ── Go modules ─────────────────────────────────────────────────────
# First, and unconditionally: this is the part every package needs.
log "downloading Go modules"
if ! go mod download; then
  log "WARNING: go mod download failed; builds may need network access"
fi

# ── dockerd ────────────────────────────────────────────────────────
start_dockerd() {
  if docker info >/dev/null 2>&1; then
    log "dockerd already running"
    return 0
  fi
  if [ "$(id -u)" -ne 0 ]; then
    log "WARNING: not root, cannot start dockerd"
    return 1
  fi
  command -v dockerd >/dev/null 2>&1 || { log "WARNING: dockerd is not installed"; return 1; }

  log "starting dockerd"
  dockerd >/var/log/dockerd.log 2>&1 &
  # 60s: an unseeded overlay2 filesystem takes noticeably longer than a warm one.
  for _ in $(seq 1 60); do
    docker info >/dev/null 2>&1 && { log "dockerd up"; return 0; }
    sleep 1
  done
  log "WARNING: dockerd did not become ready in 60s; see /var/log/dockerd.log"
  return 1
}

if ! start_dockerd; then
  log "continuing without Docker: the database-gated tests will skip"
  emit_env
  exit 0
fi

# ── Postgres and Redis ─────────────────────────────────────────────
log "bringing up postgres and redis from deploy/docker-compose.yaml"
if ! docker compose -f deploy/docker-compose.yaml up -d postgres redis; then
  log "WARNING: compose up failed; the database-gated tests will skip"
  emit_env
  exit 0
fi

# Both services declare a healthcheck, so wait on that rather than on a port
# being open: Postgres accepts connections for a moment before it will answer.
wait_healthy() {
  local name="$1" state
  for _ in $(seq 1 60); do
    state="$(docker inspect -f '{{.State.Health.Status}}' "$name" 2>/dev/null || echo missing)"
    [ "$state" = "healthy" ] && { log "$name healthy"; return 0; }
    sleep 1
  done
  log "WARNING: $name did not report healthy in 60s (last state: ${state:-unknown})"
  return 1
}

if ! wait_healthy aegis-postgres || ! wait_healthy aegis-redis; then
  log "continuing: the database-gated tests will skip"
  emit_env
  exit 0
fi

# ── Migrations ─────────────────────────────────────────────────────
# Before the tests, not during them: the integration tests read the real schema.
log "running migrations"
if ! DATABASE_URL="$DATABASE_URL" go run ./cmd/migrate -direction up; then
  log "WARNING: migrations failed; the database-gated tests will fail rather than skip"
  emit_env
  exit 0
fi

emit_env with-db
log "ready: TEST_DATABASE_URL and REDIS_URL are set for this session"
