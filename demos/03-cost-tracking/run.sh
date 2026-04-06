#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

DEMO_KEY="aegis-demo-quickstart"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:${GATEWAY_PORT:-8080}}"
METRICS_URL="http://localhost:${METRICS_PORT:-9090}"

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

# ── Step 1 — Make 3 requests to different model tiers ────────────
step "Step 1 — Same prompt, three cost tiers"

PROMPT="Say hi in one word"

echo "Request A: aegis-fast (cheapest — Claude Haiku / GPT-4o-mini)"
echo ""
RESP_A=$(curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d "{
    \"model\": \"aegis-fast\",
    \"messages\": [{\"role\": \"user\", \"content\": \"${PROMPT}\"}]
  }")
COST_A=$(echo "${RESP_A}" | jq -r '.usage.estimated_cost_usd // .estimated_cost_usd // "n/a"')
echo "  estimated_cost_usd: ${COST_A}"
echo ""

echo "Request B: aegis-gpt4 (mid-tier — GPT-4o)"
echo ""
RESP_B=$(curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d "{
    \"model\": \"aegis-gpt4\",
    \"messages\": [{\"role\": \"user\", \"content\": \"${PROMPT}\"}]
  }")
COST_B=$(echo "${RESP_B}" | jq -r '.usage.estimated_cost_usd // .estimated_cost_usd // "n/a"')
echo "  estimated_cost_usd: ${COST_B}"
echo ""

echo "Request C: aegis-reasoning (most expensive — Claude Opus / o3)"
echo ""
RESP_C=$(curl -s -X POST "${GATEWAY_URL}/v1/chat/completions" \
  -H "Authorization: Bearer ${DEMO_KEY}" \
  -H "Content-Type: application/json" \
  -d "{
    \"model\": \"aegis-reasoning\",
    \"messages\": [{\"role\": \"user\", \"content\": \"${PROMPT}\"}]
  }")
COST_C=$(echo "${RESP_C}" | jq -r '.usage.estimated_cost_usd // .estimated_cost_usd // "n/a"')
echo "  estimated_cost_usd: ${COST_C}"
echo ""

echo "Same prompt, three different cost tiers."

pause

# ── Step 2 — Per-model cost breakdown ────────────────────────────
step "Step 2 — Per-model cost breakdown (PostgreSQL)"

echo '$ docker exec aegis-demo-postgres psql -U aegis -d aegis -c "SELECT …"'
echo ""

docker exec aegis-demo-postgres psql -U aegis -d aegis -c "
  SELECT model_served,
         COUNT(*)                              AS requests,
         SUM(prompt_tokens + completion_tokens) AS total_tokens,
         ROUND(SUM(estimated_cost_usd)::numeric, 8) AS total_cost_usd
  FROM usage_records
  GROUP BY model_served
  ORDER BY total_cost_usd DESC;"

pause

# ── Step 3 — Cost in Prometheus metrics ──────────────────────────
step "Step 3 — Cost in Prometheus metrics"

echo '$ curl -s '"${METRICS_URL}"'/metrics | grep aegis_cost'
echo ""

COST_METRICS=$(curl -s "${METRICS_URL}/metrics" | grep -E 'aegis_cost' || true)

if [ -z "${COST_METRICS}" ]; then
  echo "(No cost metrics yet — they appear after the first request"
  echo " is fully processed and usage is recorded.)"
else
  echo "${COST_METRICS}"
fi

echo ""
echo "These metrics can be scraped by Grafana or any"
echo "Prometheus-compatible dashboard."

pause

# ── Step 4 — Session total ───────────────────────────────────────
step "Step 4 — Session total (all requests since gateway started)"

docker exec aegis-demo-postgres psql -U aegis -d aegis -c "
  SELECT COUNT(*)                               AS total_requests,
         ROUND(SUM(estimated_cost_usd)::numeric, 8) AS session_cost_usd
  FROM usage_records;"

echo ""
echo "============================================"
echo "  Done! All 4 steps complete."
echo "============================================"
echo ""
echo "Key takeaways:"
echo "  - Every request records estimated_cost_usd automatically"
echo "  - Costs are broken down by model, org, and team in PostgreSQL"
echo "  - Prometheus counter aegis_cost_usd_total enables real-time dashboards"
echo "  - Query usage_records for per-request attribution"
echo ""
