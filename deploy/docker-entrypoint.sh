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
#
# Inserts a well-known demo key so README curl commands work verbatim.
#
#   Key:    aegis-demo-quickstart
#   Hash:   sha256("aegis-demo-quickstart")
#   Prefix: aegis-demo-quicksta
#
# ON CONFLICT makes it idempotent across container restarts.
seed_demo_key() {
  if [ "${AEGIS_SEED_DEMO_KEY:-}" != "true" ]; then
    return
  fi

  echo "seeding demo API key…"
  psql "$DATABASE_URL" -q <<'SQL'
    INSERT INTO api_keys (
      key_hash, key_prefix, organization_id, team_id, name,
      max_classification, allowed_models, expires_at
    ) VALUES (
      '0c8c82332e8aa010e44766a860fa6d7c6b7e9bb45ead65c11b0a13ab0c391e4e',
      'aegis-demo-quicksta',
      'demo-org', 'demo-team', 'quickstart-demo-key',
      'INTERNAL', '[]'::jsonb,
      NOW() + INTERVAL '1 year'
    ) ON CONFLICT (key_hash) DO NOTHING;
SQL
  echo "demo key: aegis-demo-quickstart"
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
