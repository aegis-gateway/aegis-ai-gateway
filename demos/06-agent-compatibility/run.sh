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
# This demo needs an Anthropic key. Every alias in configs/models.yaml lists
# Anthropic as its primary route with OpenAI only as a fallback, and
# ResolveRoute does not fail over on an upstream auth error — a provider whose
# key is empty stays registered and eligible, returns 401, and the request
# fails. So there is no alias an OpenAI-only setup can drive today, and the
# previous fallback here (aegis-balanced) is Anthropic-primary like the rest.
AGENT_MODEL="aegis-fast"

# The tool-calling act needs a different alias. Tool definitions, tool calls and
# tool results are carried by the OpenAI adapter and not by the Anthropic one,
# and a tool request routed to a provider that cannot carry tools is refused
# with HTTP 400 rather than forwarded with its tools removed. config/models.yaml
# adds aegis-tools for exactly this, routed OpenAI-primary.
#
# Without an OpenAI key the act still runs and still produces a finding: the
# refusal, which is documented behaviour and is the more useful thing to see
# than nothing at all.
if grep -qE '^OPENAI_API_KEY=.+' .env; then
  AGENT_TOOL_MODEL="aegis-tools"
else
  AGENT_TOOL_MODEL="${AGENT_MODEL}"
  echo "NOTE: no OPENAI_API_KEY set. The tool-calling act will exercise the"
  echo "      Anthropic route, which refuses tool requests by design, and will"
  echo "      report that refusal rather than a completed tool loop."
  echo "      Set OPENAI_API_KEY to see the working path."
  echo ""
fi

if ! grep -qE '^ANTHROPIC_API_KEY=.+' .env; then
  echo "ERROR: this demo requires ANTHROPIC_API_KEY." >&2
  echo "" >&2
  echo "  Every model alias routes to Anthropic first, and the gateway does not" >&2
  echo "  fail over to the OpenAI fallback when the Anthropic key is missing —" >&2
  echo "  the request reaches Anthropic, gets a 401, and surfaces as a 500." >&2
  echo "  An OpenAI-only run would report transport failures rather than the" >&2
  echo "  compatibility findings this demo exists to collect." >&2
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
  Tool model: ${AGENT_TOOL_MODEL}

  The agent.py script uses the openai Python SDK pointed at AEGIS.
  No code changes — only base_url and api_key differ from production.

  Running six test scenarios:
    1. Secrets     — AWS key in prompt → blocked before provider
    2. Policy      — exfiltration-shaped task → Rego denial
    3. Streaming   — token delivery via SSE
    4. Tool use    — a full tool call loop, and the refusals around it
    5. Long ctx    — large payload passes through
    6. Denial loop — agent stops on 451, no retry

BANNER

# ── Run agent ────────────────────────────────────────────────────
GATEWAY_URL="${GATEWAY_URL}/v1" \
  DEMO_KEY="${DEMO_KEY}" \
  AGENT_MODEL="${AGENT_MODEL}" \
  AGENT_TOOL_MODEL="${AGENT_TOOL_MODEL}" \
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
