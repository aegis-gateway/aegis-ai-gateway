#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

DEMO_KEY="aegis-demo-quickstart"
GATEWAY_PORT="${GATEWAY_PORT:-8080}"
WEBUI_PORT="${WEBUI_PORT:-3000}"
METRICS_PORT="${METRICS_PORT:-9090}"
BASE_URL="http://localhost:${GATEWAY_PORT}"
WEBUI_URL="http://localhost:${WEBUI_PORT}"

# ── Ensure .env ──────────────────────────────────────────────────
if [ ! -f .env ]; then
  cp ../shared/.env.example .env
  echo "Created .env — add at least one provider API key:"
  echo "  OPENAI_API_KEY=sk-proj-..."
  echo "  ANTHROPIC_API_KEY=sk-ant-..."
  echo ""
  echo "Then re-run: ./run.sh"
  exit 1
fi

if ! grep -qE '^(OPENAI_API_KEY|ANTHROPIC_API_KEY)=.+' .env; then
  echo "ERROR: set at least one provider API key in .env" >&2
  exit 1
fi

# ── Start ────────────────────────────────────────────────────────
echo "Building and starting AEGIS quickstart demo…"
docker compose up --build -d

../shared/wait-for-gateway.sh "${BASE_URL}"

# ── Ready ────────────────────────────────────────────────────────
cat <<EOF

============================================
  AEGIS Quickstart Demo
============================================

  Web UI:   ${WEBUI_URL}  (create an account on first visit)
  Gateway:  ${BASE_URL}
  Metrics:  http://localhost:${METRICS_PORT}/metrics
  Demo key: ${DEMO_KEY}

Try these in the chat UI or with curl:

  1. Pick a model (aegis-fast, aegis-gpt4, aegis-reasoning)
     and send a message — each routes to a different provider.

  2. Paste an AWS key (AKIAIOSFODNN7EXAMPLE) in a message
     — the gateway blocks it before it reaches the provider.

  3. Check cost tracking:
     docker exec aegis-demo-postgres psql -U aegis -d aegis \\
       -c "SELECT model_served, COUNT(*), SUM(estimated_cost_usd) FROM usage_records GROUP BY model_served;"

  4. View Prometheus metrics:
     curl http://localhost:${METRICS_PORT}/metrics | grep aegis_

Or use curl directly:

  curl ${BASE_URL}/v1/chat/completions \\
    -H 'Authorization: Bearer ${DEMO_KEY}' \\
    -H 'Content-Type: application/json' \\
    -d '{"model":"aegis-fast","messages":[{"role":"user","content":"Hello!"}]}' | jq

Stop: docker compose down -v

EOF
