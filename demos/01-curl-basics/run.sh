#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

DEMO_KEY="aegis-demo-quickstart"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:${GATEWAY_PORT:-8080}}"

# ── Preflight: gateway must be running ───────────────────────────
if ! curl -sf "${GATEWAY_URL}/aegis/v1/health" > /dev/null 2>&1; then
  echo "ERROR: gateway not reachable at ${GATEWAY_URL}" >&2
  echo ""
  echo "Start demos/00-quickstart first:"
  echo "  cd demos/00-quickstart && ./run.sh"
  exit 1
fi
echo "Gateway is running at ${GATEWAY_URL}"
echo ""

# ── Helpers ──────────────────────────────────────────────────────
pause() {
  echo ""
  read -r -p "Press enter to continue…"
  echo ""
}

step() {
  echo "============================================"
  echo "  $1"
  echo "============================================"
  echo ""
}

# ── Step 1 — Health check ────────────────────────────────────────
step "Step 1 — Health check (no auth required)"

echo '$ curl '"${GATEWAY_URL}"'/aegis/v1/health'
echo ""
curl -s "${GATEWAY_URL}/aegis/v1/health" | jq .

pause

# ── Step 2 — List models ─────────────────────────────────────────
step "Step 2 — List available models"

echo '$ curl '"${GATEWAY_URL}"'/v1/models -H "Authorization: Bearer …"'
echo ""
curl -s "${GATEWAY_URL}/v1/models" \
  -H "Authorization: Bearer ${DEMO_KEY}" | jq .

pause

# ── Step 3 — Chat completion ─────────────────────────────────────
step "Step 3 — Chat completion (aegis-fast)"

echo '$ curl -X POST '"${GATEWAY_URL}"'/v1/chat/completions \'
echo '    -H "Authorization: Bearer …" \'
echo '    -d '"'"'{"model":"aegis-fast","messages":[…]}'"'"
echo ""
curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "aegis-fast",
    "messages": [{"role": "user", "content": "What is 2+2? Reply in one word."}]
  }' | jq .

echo ""
echo "Note: estimated_cost_usd shows the per-request cost."

pause

# ── Step 4 — Error: missing auth ─────────────────────────────────
step "Step 4 — Error: missing Authorization header (HTTP 401)"

echo '$ curl -s -w "\nHTTP %{http_code}" '"${GATEWAY_URL}"'/v1/models'
echo ""
curl -s -w "\nHTTP %{http_code}\n" "${GATEWAY_URL}/v1/models" | jq . 2>/dev/null || true

pause

# ── Step 5 — Error: unknown model ────────────────────────────────
step "Step 5 — Error: unknown model (HTTP 503)"

echo '$ curl -X POST … -d '"'"'{"model":"does-not-exist",…}'"'"
echo ""
curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "does-not-exist",
    "messages": [{"role": "user", "content": "hi"}]
  }' | jq .

pause

# ── Step 6 — Error: missing messages ─────────────────────────────
step "Step 6 — Error: missing messages field (HTTP 400)"

echo '$ curl -X POST … -d '"'"'{"model":"aegis-fast"}'"'"
echo ""
curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"model": "aegis-fast"}' | jq .

pause

# ── Step 7 — Check database records ──────────────────────────────
step "Step 7 — Usage records in PostgreSQL"

echo '$ docker exec aegis-demo-postgres psql -U aegis -d aegis -c "SELECT …"'
echo ""
docker exec aegis-demo-postgres psql -U aegis -d aegis -c \
  "SELECT model_requested, model_served, prompt_tokens, completion_tokens, estimated_cost_usd
   FROM usage_records ORDER BY created_at DESC LIMIT 3;"

echo ""
echo "============================================"
echo "  Done! All 7 steps complete."
echo "============================================"
echo ""
echo "The gateway accepts the same request format as the OpenAI API."
echo "Any existing OpenAI client library works by changing base_url:"
echo ""
echo '  client = OpenAI(base_url="'"${GATEWAY_URL}"'/v1", api_key="'"${DEMO_KEY}"'")'
echo ""
