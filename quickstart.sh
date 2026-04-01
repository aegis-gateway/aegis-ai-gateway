#!/usr/bin/env bash
set -euo pipefail

# AEGIS AI Gateway — Quickstart
# Starts the full stack and prints ready-to-use curl commands.

COMPOSE_FILE="deploy/docker-compose.quickstart.yaml"
DEMO_KEY="aegis-demo-quickstart"
HOST_PORT="${GATEWAY_HOST_PORT:-8080}"
BASE_URL="http://localhost:${HOST_PORT}"

# ── Preflight ────────────────────────────────────────────────────
if [ ! -f .env ]; then
  if [ -f .env.example ]; then
    cp .env.example .env
    echo "Created .env from .env.example — add at least one provider API key:"
    echo "  OPENAI_API_KEY=sk-proj-..."
    echo "  ANTHROPIC_API_KEY=sk-ant-..."
    echo ""
    echo "Then re-run: ./quickstart.sh"
    exit 1
  else
    echo "ERROR: .env.example not found. Are you in the repo root?" >&2
    exit 1
  fi
fi

# Check that at least one key is set
if ! grep -qE '^(OPENAI_API_KEY|ANTHROPIC_API_KEY)=.+' .env; then
  echo "ERROR: Set at least one provider API key in .env" >&2
  echo "  OPENAI_API_KEY=sk-proj-..."
  echo "  ANTHROPIC_API_KEY=sk-ant-..."
  exit 1
fi

# ── Start ────────────────────────────────────────────────────────
echo "Starting AEGIS AI Gateway…"
docker compose -f "$COMPOSE_FILE" up --build -d

echo "Waiting for gateway to become healthy…"
retries=60
until curl -sf "${BASE_URL}/aegis/v1/health" > /dev/null 2>&1 || [ $retries -eq 0 ]; do
  retries=$((retries - 1))
  sleep 2
done

if [ $retries -eq 0 ]; then
  echo "Gateway did not become healthy in time. Check logs:"
  echo "  docker compose -f $COMPOSE_FILE logs gateway"
  exit 1
fi

# ── Ready ────────────────────────────────────────────────────────
echo ""
echo "============================================"
echo "  AEGIS AI Gateway is running!"
echo "============================================"
echo ""
echo "  Gateway:  ${BASE_URL}"
echo "  Metrics:  http://localhost:${METRICS_HOST_PORT:-9090}/metrics"
echo "  Demo key: ${DEMO_KEY}"
echo ""
echo "Try it:"
echo ""
echo "  # Health check"
echo "  curl ${BASE_URL}/aegis/v1/health | jq"
echo ""
echo "  # Chat completion"
echo "  curl ${BASE_URL}/v1/chat/completions \\"
echo "    -H 'Authorization: Bearer ${DEMO_KEY}' \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"model\":\"aegis-fast\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}' | jq"
echo ""
echo "Stop with: docker compose -f $COMPOSE_FILE down -v"
