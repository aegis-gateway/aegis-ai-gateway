#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

DEMO_KEY="aegis-demo-quickstart"
GATEWAY_PORT="${GATEWAY_PORT:-8080}"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:${GATEWAY_PORT}}"

# ── Preflight: ensure provider keys ──────────────────────────────
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

# ── Start stack ───────────────────────────────────────────────────
echo "Building and starting AEGIS secrets-filter demo…"
docker compose up --build -d

../shared/wait-for-gateway.sh "${GATEWAY_URL}"

cat <<'BANNER'

============================================
  AEGIS Secrets Filter Demo
============================================

The secrets filter runs before every request reaches a provider.
Any message containing a recognisable credential pattern is rejected
with HTTP 400 and logged to audit_events.

BANNER

PASS=0
FAIL=0

check() {
  local label="$1"
  local expected_status="$2"
  local body="$3"

  actual_status=$(curl -s -o /dev/null -w "%{http_code}" \
    -X POST "${GATEWAY_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${DEMO_KEY}" \
    -H "Content-Type: application/json" \
    -d "${body}")

  if [ "${actual_status}" = "${expected_status}" ]; then
    printf "  %-40s → PASS (HTTP %s)\n" "${label}" "${actual_status}"
    PASS=$((PASS + 1))
  else
    printf "  %-40s → FAIL (expected %s, got %s)\n" "${label}" "${expected_status}" "${actual_status}"
    FAIL=$((FAIL + 1))
  fi
}

# ── Test cases ────────────────────────────────────────────────────
echo "[1/5] AWS key blocked"
check "[1/5] AWS key blocked" "400" \
  '{"model":"aegis-fast","messages":[{"role":"user","content":"My AWS key is AKIAIOSFODNN7EXAMPLE — is this valid?"}]}'

echo "[2/5] GitHub token blocked"
check "[2/5] GitHub token blocked" "400" \
  '{"model":"aegis-fast","messages":[{"role":"user","content":"Token: ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}]}'

echo "[3/5] Private key blocked"
check "[3/5] Private key blocked" "400" \
  '{"model":"aegis-fast","messages":[{"role":"user","content":"Here is my key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA..."}]}'

echo "[4/5] JWT blocked"
check "[4/5] JWT blocked" "400" \
  '{"model":"aegis-fast","messages":[{"role":"user","content":"JWT: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.fake"}]}'

echo "[5/5] Clean request passes"
check "[5/5] Clean request passes" "200" \
  '{"model":"aegis-fast","messages":[{"role":"user","content":"Hello from AEGIS!"}]}'

# ── Summary ───────────────────────────────────────────────────────
echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
echo ""

# ── Audit events ──────────────────────────────────────────────────
echo "Recent audit_events (blocked requests):"
docker compose exec postgres psql -U aegis -d aegis \
  -c "SELECT action, reason FROM audit_events ORDER BY created_at DESC LIMIT 10;" \
  2>/dev/null || echo "(audit_events not yet populated — run may need a moment)"

echo ""

# ── Tear down ─────────────────────────────────────────────────────
echo "Tearing down…"
docker compose down -v

echo ""
echo "============================================"
echo "  Done!"
echo "============================================"
echo ""
echo "Key takeaways:"
echo "  - Secrets are blocked before any data reaches an LLM provider"
echo "  - Every blocked request is logged to audit_events"
echo "  - Clean requests pass through unmodified"
echo "  - No configuration required — the filter is always on"
echo ""

if [ "${FAIL}" -gt 0 ]; then
  exit 1
fi
