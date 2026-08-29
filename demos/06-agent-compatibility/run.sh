#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

DEMO_KEY="aegis-demo-quickstart"
GATEWAY_PORT="${GATEWAY_PORT:-8080}"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:${GATEWAY_PORT}}"

# ── Preflight: provider keys ─────────────────────────────────────
if [ -n "${OPENAI_API_KEY:-}" ] || [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  ../shared/write-env.sh > .env
elif [ ! -f .env ] || ! grep -qE '^(OPENAI_API_KEY|ANTHROPIC_API_KEY)=.+' .env; then
  [ -f .env ] || cp ../shared/.env.example .env
  echo "Created .env — add at least one provider API key:"
  echo "  OPENAI_API_KEY=sk-proj-..."
  echo "  ANTHROPIC_API_KEY=sk-ant-..."
  echo ""
  echo "Or export them in your shell and re-run: ./run.sh"
  exit 1
fi

# ── Preflight: Python and openai package ─────────────────────────
if ! command -v python3 &>/dev/null; then
  echo "ERROR: python3 not found — required to run agent.py" >&2
  exit 1
fi
if ! python3 -c "import openai" 2>/dev/null; then
  echo "Installing openai package…"
  pip install openai --quiet
fi

../shared/ensure-pepper.sh .env

# ── Model selection ───────────────────────────────────────────────
#
# aegis-fast is Anthropic-primary with an OpenAI fallback. Either key works: a
# provider with no api_key is left unregistered, so an uncredentialed primary
# falls through to the fallback instead of failing on a 401.
AGENT_MODEL="aegis-fast"

if ! grep -qE '^(OPENAI_API_KEY|ANTHROPIC_API_KEY)=.+' .env; then
  echo "ERROR: this demo needs a provider key — set OPENAI_API_KEY or ANTHROPIC_API_KEY." >&2
  echo "  The compatibility findings come from real provider responses, so the" >&2
  echo "  mock provider would not exercise what this demo measures." >&2
  exit 1
fi

# ── Start stack ───────────────────────────────────────────────────
echo "Building and starting AEGIS agent-compatibility demo…"
docker compose up --build -d

../shared/wait-for-gateway.sh "${GATEWAY_URL}"

cat <<BANNER

============================================
  AEGIS Agent Compatibility Demo
============================================

  Gateway:    ${GATEWAY_URL}
  Demo key:   ${DEMO_KEY}
  Model:      ${AGENT_MODEL}

  The agent.py script uses the openai Python SDK pointed at AEGIS.
  No code changes — only base_url and api_key differ from production.

  Running six test scenarios:
    1. Secrets     — AWS key in prompt → blocked before provider
    2. Policy      — exfiltration-shaped task → Rego denial
    3. Streaming   — token delivery via SSE
    4. Tool use    — a full tool call loop on the shipped alias
    5. Long ctx    — large payload passes through
    6. Denial loop — agent stops on 451, no retry

BANNER

# ── Run agent ────────────────────────────────────────────────────
GATEWAY_URL="${GATEWAY_URL}/v1" \
  DEMO_KEY="${DEMO_KEY}" \
  AGENT_MODEL="${AGENT_MODEL}" \
  python3 agent.py

# ── Audit tail ───────────────────────────────────────────────────
echo ""
echo "============================================"
echo "  Audit log (last 10 events)"
echo "============================================"
echo ""
docker exec aegis-demo-postgres psql -U aegis -d aegis \
  -c "SELECT event_type, error_message, timestamp FROM audit_events ORDER BY timestamp DESC LIMIT 10;" \
  2>/dev/null || echo "(audit table query failed — stack may have just started)"

echo ""
echo "  Note: error_message contains the filter/policy reason."
echo "  The prompt text and AWS key are NOT stored in audit_events."
echo ""
echo "  Stop: docker compose down -v"
echo ""
