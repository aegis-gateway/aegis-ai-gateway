#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

DEMO_KEY="aegis-demo-quickstart"
GATEWAY_PORT="${GATEWAY_PORT:-8080}"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:${GATEWAY_PORT}}"

# ── Ensure provider keys ─────────────────────────────────────────
if [ ! -f .env ]; then
  if [ -n "${OPENAI_API_KEY:-}" ] || [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    ../shared/write-env.sh > .env
  else
    cp ../shared/.env.example .env
    echo "Created .env — add at least one provider API key:"
    echo "  OPENAI_API_KEY=sk-proj-..."
    echo "  ANTHROPIC_API_KEY=sk-ant-..."
    echo ""
    echo "Or export them in your shell and re-run: ./run.sh"
    exit 1
  fi
fi

if ! grep -qE '^(OPENAI_API_KEY|ANTHROPIC_API_KEY)=.+' .env && \
   [ -z "${OPENAI_API_KEY:-}" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "ERROR: set at least one provider API key in .env or environment" >&2
  exit 1
fi

# ── Start ────────────────────────────────────────────────────────
echo "Building and starting AEGIS custom-policies demo…"
docker compose up --build -d

../shared/wait-for-gateway.sh "${GATEWAY_URL}"

cat <<'BANNER'

============================================
  AEGIS Custom Policies Demo
============================================

BANNER

# ── ACT 1 — Competitor mention policy ────────────────────────────
echo "=== ACT 1: Competitor Mention Policy ==="
echo ""
echo "Policy file: policies/competitor-mention.rego"
cat policies/competitor-mention.rego
echo ""

echo "--- Blocked request (mentions a competitor) ---"
curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","messages":[{"role":"user","content":"How does AEGIS compare to Portkey for enterprise use?"}]}' \
  | jq '{status: .error.code, reason: .error.message}'
echo ""

echo "--- Clean request (no competitor mention) ---"
curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","messages":[{"role":"user","content":"What are the benefits of an AI gateway?"}]}' \
  | jq '{model: .model, content: .choices[0].message.content[:120]}'
echo ""

# ── ACT 2 — Topic restriction policy ─────────────────────────────
echo "=== ACT 2: Topic Restriction Policy ==="
echo ""
echo "Policy file: policies/topic-restriction.rego"
cat policies/topic-restriction.rego
echo ""

echo "--- Blocked request (financial topic from non-finance team) ---"
curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model":"aegis-fast","messages":[{"role":"user","content":"What is the best trading strategy for tech stocks?"}]}' \
  | jq '{status: .error.code, reason: .error.message}'
echo ""

# ── ACT 3 — Audit log ────────────────────────────────────────────
echo "=== ACT 3: Audit Log ==="
echo ""
echo "--- All policy violations logged ---"
docker exec aegis-demo-postgres psql -U aegis -d aegis -c "
  SELECT event_type, error_message, timestamp
  FROM audit_events
  WHERE event_type = 'filter_block'
  ORDER BY timestamp DESC LIMIT 5;" 2>/dev/null || echo "(audit table not yet populated)"
echo ""
echo "Every denial is recorded regardless of which policy fired."
echo ""

cat <<'EOF'
============================================
  Demo complete!
============================================

  Stop: docker compose down -v

EOF
