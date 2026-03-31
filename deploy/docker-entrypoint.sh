#!/usr/bin/env bash
set -euo pipefail

CONFIG_DIR="${AEGIS_CONFIG_DIR:-/etc/aegis/configs}"
MIGRATIONS_DIR="${AEGIS_MIGRATIONS_DIR:-/etc/aegis/migrations}"

# ── Wait for Postgres ────────────────────────────────────────────
wait_for_postgres() {
  local retries=30
  until migrate -path "$MIGRATIONS_DIR" -direction up -db-url "$DATABASE_URL" 2>/dev/null || [ $retries -eq 0 ]; do
    echo "waiting for postgres… ($retries attempts left)"
    retries=$((retries - 1))
    sleep 1
  done
  if [ $retries -eq 0 ]; then
    echo "ERROR: postgres not reachable after 30s" >&2
    exit 1
  fi
}

# ── Run migrations ───────────────────────────────────────────────
run_migrations() {
  echo "running migrations…"
  migrate -path "$MIGRATIONS_DIR" -direction up
  echo "migrations complete"
}

# ── Seed demo key ────────────────────────────────────────────────
seed_demo_key() {
  if [ "${AEGIS_SEED_DEMO_KEY:-}" = "true" ]; then
    echo "seeding demo API key…"
    keygen \
      -org demo-org \
      -team demo-team \
      -name demo-key \
      -classification INTERNAL \
      -expires 365d \
      -env demo
  fi
}

# ── Main ─────────────────────────────────────────────────────────
case "${1:-gateway}" in
  gateway)
    wait_for_postgres
    run_migrations
    seed_demo_key
    echo "starting gateway…"
    exec gateway -config "$CONFIG_DIR"
    ;;
  migrate)
    shift
    exec migrate "$@"
    ;;
  keygen)
    shift
    exec keygen "$@"
    ;;
  *)
    exec "$@"
    ;;
esac
